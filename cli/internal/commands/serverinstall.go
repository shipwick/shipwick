package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// installOptions are the flags of `server install`, validated before
// anything is sent to the server.
type installOptions struct {
	agentDomain, dashboardDomain string
	context                      string
	version                      string
}

func (o *installOptions) validate() error {
	o.agentDomain = strings.ToLower(strings.TrimSpace(o.agentDomain))
	o.dashboardDomain = strings.ToLower(strings.TrimSpace(o.dashboardDomain))
	for _, d := range []struct{ flag, domain string }{{"--agent-domain", o.agentDomain}, {"--dashboard-domain", o.dashboardDomain}} {
		if d.domain == "" {
			continue
		}
		if err := spec.ValidateDomain(d.domain); err != nil {
			return fmt.Errorf("%s: %w — give a bare hostname such as agent.example.com, no https://, port or path", d.flag, err)
		}
	}
	if o.agentDomain != "" && o.agentDomain == o.dashboardDomain {
		return errors.New("the API and the dashboard need two different hostnames")
	}
	if o.version != "" {
		if _, ok := version.Parse(o.version); !ok || strings.ContainsAny(o.version, " '\"") {
			return fmt.Errorf("--version %q is not a release version; releases look like v0.4.0", o.version)
		}
	}
	return nil
}

// sshTarget is the user@host to log in as. Both parts are checked here, not
// because ssh would misread them, but because they are the only two values
// from the command line that reach a command line of their own.
type sshTarget struct{ user, host string }

func (t sshTarget) String() string { return t.user + "@" + t.host }

var sshUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func parseSSHTarget(s string) (sshTarget, error) {
	user, host, ok := strings.Cut(s, "@")
	if !ok || !sshUserPattern.MatchString(user) {
		return sshTarget{}, fmt.Errorf("give the server as user@host, e.g. root@203.0.113.10 (got %q)", s)
	}
	host = strings.ToLower(host)
	if net.ParseIP(host) == nil {
		if err := spec.ValidateDomain(host); err != nil {
			return sshTarget{}, fmt.Errorf("%q is neither a hostname nor an IP address", host)
		}
	}
	return sshTarget{user: user, host: host}, nil
}

// sshArgv runs remote on the target. BatchMode makes ssh fail instead of
// asking for a password; the "--" keeps the command from being read as
// options.
func sshArgv(t sshTarget, remote string) []string {
	return []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", t.String(), "--", remote}
}

// Remote commands are fixed strings. The installer's is the one exception:
// installerRemoteCommand substitutes the validated hostnames and version.
const (
	remoteUname         = "uname -sm"
	remoteHasDocker     = "command -v docker >/dev/null 2>&1"
	remoteDockerVersion = "docker version --format '{{.Server.Version}}'"
	remoteInstallDocker = "curl -fsSL https://get.docker.com | sh"
)

// installerRemoteCommand is the installer as the handbook shows it, with its
// settings as environment assignments. Every value passed the validation
// above, so none can hold a quote or anything else the remote shell would
// read; the quotes are there so that what is sent reads as what is meant.
func installerRemoteCommand(o installOptions) string {
	env := []string{
		"SHIPWICK_AGENT_DOMAIN='" + o.agentDomain + "'",
		"SHIPWICK_DASHBOARD_DOMAIN='" + o.dashboardDomain + "'",
	}
	if o.version != "" {
		env = append(env, "SHIPWICK_VERSION='"+o.version+"'")
	}
	return "curl -fsSL https://get.shipwick.com | " + strings.Join(env, " ") + " sh"
}

func (c *cli) serverInstallCommand() *cobra.Command {
	var opts installOptions
	cmd := &cobra.Command{
		Use:   "install <user@host>",
		Short: "Install or upgrade the Shipwick server over SSH, from here",
		Long: `Install or upgrade the Shipwick server over SSH, from this machine.

The server is a Linux machine you can log in to with an SSH key, as root or a
user who may run Docker. Docker is installed when it is missing, then the
installer runs, the token it prints is saved as a context here, and the DNS
records to create are printed:

  shipwick server install root@203.0.113.10 --agent-domain agent.example.com --dashboard-domain dashboard.example.com

Without --agent-domain the API is not exposed; reach it through an SSH tunnel
(see the handbook, Installation). Running it again upgrades the server; the
token is unchanged then, and not printed again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.serverInstall(cmd.Context(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.agentDomain, "agent-domain", "", "hostname for the API, e.g. agent.example.com")
	cmd.Flags().StringVar(&opts.dashboardDomain, "dashboard-domain", "", "hostname for the dashboard, e.g. dashboard.example.com")
	cmd.Flags().StringVar(&opts.context, "context", "", "name to save the server under (default: its hostname)")
	cmd.Flags().StringVar(&opts.version, "version", "", "release to install, e.g. v0.4.0 (default: the latest)")
	return cmd
}

func (c *cli) serverInstall(ctx context.Context, target string, opts installOptions) error {
	t, err := parseSSHTarget(target)
	if err != nil {
		return err
	}
	if err := opts.validate(); err != nil {
		return err
	}
	local := c.local.withDefaults()
	ssh := func(remote string, stdout, stderr io.Writer) error {
		return local.exec(ctx, sshArgv(t, remote), stdout, stderr)
	}

	var uname, sshErr bytes.Buffer
	c.ui.Progress("Connecting to %s", t)
	err = ssh(remoteUname, &uname, &sshErr)
	c.ui.Done()
	if err != nil {
		return describeSSHError(err, t, sshErr.String())
	}
	system := strings.Fields(uname.String())
	if len(system) != 2 || system[0] != "Linux" {
		return fmt.Errorf("%s runs %q; the Shipwick server runs on Linux", t, strings.TrimSpace(uname.String()))
	}
	c.ui.Success("Connected to %s (%s)", t, system[1])

	// The installer's output is shown as it arrives, dimmed: it is the
	// server talking, and it takes a while.
	dim := &dimmedLines{c: c}
	switch err := ssh(remoteHasDocker, io.Discard, io.Discard); {
	case err == nil:
		var v bytes.Buffer
		if err := ssh(remoteDockerVersion, &v, io.Discard); err != nil {
			return fmt.Errorf("Docker is installed on %s but does not answer; start it (systemctl start docker) and run this again", t)
		}
		c.ui.Success("Docker %s", strings.TrimSpace(v.String()))
	case sshExitCode(err) == sshConnectionFailed:
		return describeSSHError(err, t, "")
	default:
		c.ui.Println("Docker is not installed. Installing it with get.docker.com; this takes a minute.")
		if err := ssh(remoteInstallDocker, dim, dim); err != nil {
			dim.flush()
			return fmt.Errorf("installing Docker on %s failed; its output is above\n\nInstall it there yourself (%s) and run this again", t, remoteInstallDocker)
		}
		dim.flush()
		c.ui.Success("Installed Docker")
	}

	c.ui.Println("Running the Shipwick installer...")
	installer := &dimmedLines{c: c}
	err = ssh(installerRemoteCommand(opts), installer, installer)
	installer.flush()
	if err != nil {
		return fmt.Errorf("the installer failed on %s; its output is above", t)
	}
	c.ui.Success("Shipwick is running on %s", t)

	if err := c.saveInstalledContext(t, opts, installerToken(installer.lines)); err != nil {
		return err
	}
	c.printDNSRecords(ctx, local, t, opts)
	return nil
}

// saveInstalledContext makes the new server the current one. A token the
// installer did not print — it prints one only when it creates one — is
// never fetched from the server; what the context already held is kept.
func (c *cli) saveInstalledContext(t sshTarget, opts installOptions, token string) error {
	path, saved, err := c.loadSaved()
	if err != nil {
		return err
	}
	name := opts.context
	if name == "" {
		name = t.host
	}
	url := cliconfig.DefaultURL
	if opts.agentDomain != "" {
		url = "https://" + opts.agentDomain
	}
	entry := cliconfig.Context{URL: url, Token: token}
	if token == "" {
		if prev, ok := saved.Contexts[name]; ok && prev.URL == url {
			entry.Token = prev.Token
		}
	}
	saved.Set(name, entry)
	if err := cliconfig.Save(path, saved); err != nil {
		return err
	}
	switch {
	case token != "":
		c.ui.Success("Saved the API token as context %s (%s), now current", name, url)
	case entry.Token != "":
		c.ui.Success("Context %s (%s) is current; its token is unchanged", name, url)
	default:
		c.ui.Println(c.ui.Styled(ui.Yellow, "!") + fmt.Sprintf(" Context %s (%s) was saved without a token: the server already had one, and the installer does not print it again.", name, url))
		c.ui.Println(fmt.Sprintf("  Copy SHIPWICK_AGENT_TOKEN from /opt/shipwick/.env on the server, then run: shipwick login --context %s", name))
	}
	return nil
}

// printDNSRecords spells out what the hostnames need: one A record each,
// pointing straight at the server. A CDN's proxy in between would take the
// TLS handshake away from Caddy, and no certificate could be issued.
func (c *cli) printDNSRecords(ctx context.Context, local localOptions, t sshTarget, opts installOptions) {
	var hostnames []string
	for _, h := range []string{opts.agentDomain, opts.dashboardDomain} {
		if h != "" {
			hostnames = append(hostnames, h)
		}
	}
	c.ui.Println()
	if len(hostnames) > 0 {
		addrs := []string{t.host}
		if net.ParseIP(t.host) == nil {
			resolved, err := local.lookupHost(ctx, t.host)
			if err != nil || len(resolved) == 0 {
				addrs = []string{"the address of " + t.host}
			} else {
				addrs = resolved
			}
		}
		c.ui.Println("Create these DNS records, DNS only (not proxied):")
		for _, h := range hostnames {
			for _, addr := range addrs {
				kind := "A"
				if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
					kind = "AAAA"
				}
				c.ui.Println(fmt.Sprintf("  %-5s %s  →  %s", kind, h, addr))
			}
		}
		c.ui.Println()
	}
	if opts.agentDomain == "" {
		c.ui.Println("No API hostname was given, so the API is reachable only through an SSH tunnel; the installer's output above says how to set one up.")
		c.ui.Println()
	}
	c.ui.Println("Next: in your project, run: shipwick init")
	c.ui.Println("      once the records exist, check the setup with: shipwick doctor")
}

// sshConnectionFailed is ssh's own exit status; a remote command's status
// is anything else.
const sshConnectionFailed = 255

func sshExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func describeSSHError(err error, t sshTarget, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("ssh was not found on this machine\n\nInstall the OpenSSH client (Debian/Ubuntu: apt install openssh-client; Windows: Settings → System → Optional features → OpenSSH Client) and run this again")
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("could not log in to %s over SSH:\n  %s\n\nThe login must work without a password: add your key with ssh-copy-id %s, or check the host in ~/.ssh/config, then run this again", t, detail, t)
}

// installerToken finds the token in the installer's summary: the one line
// that holds nothing but the token, after the "API token" heading. On an
// upgrade the installer prints no token, and none is found.
func installerToken(lines []string) string {
	heading := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "API token"):
			heading = true
		case heading && tokenLine.MatchString(line):
			return line
		}
	}
	return ""
}

// tokenLine is what random_token in scripts/install.sh produces.
var tokenLine = regexp.MustCompile(`^[0-9a-f]{64}$`)

// dimmedLines prints a program's output line by line, indented and dimmed,
// and keeps the lines for a look afterwards. The token line is kept but not
// shown: it is saved to the config, and a terminal is read over shoulders.
// One instance serves as both stdout and stderr of a program, which makes
// os/exec deliver both through one goroutine.
type dimmedLines struct {
	c       *cli
	partial []byte
	lines   []string
}

func (d *dimmedLines) Write(p []byte) (int, error) {
	d.partial = append(d.partial, p...)
	for {
		i := bytes.IndexByte(d.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		d.line(string(d.partial[:i]))
		d.partial = d.partial[i+1:]
	}
}

// flush prints a last line that ended without a newline.
func (d *dimmedLines) flush() {
	if len(d.partial) > 0 {
		d.line(string(d.partial))
		d.partial = nil
	}
}

func (d *dimmedLines) line(s string) {
	s = strings.TrimRight(s, "\r")
	d.lines = append(d.lines, s)
	if tokenLine.MatchString(strings.TrimSpace(s)) {
		s = "      (the API token — saved to your shipwick config, not shown)"
	}
	d.c.ui.Println("  " + d.c.ui.Styled(ui.Dim, s))
}
