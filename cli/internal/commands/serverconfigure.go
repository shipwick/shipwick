package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/pkg/spec"
)

// installerVariables are the agent's settings the installer carries from its
// environment into a new /opt/shipwick/.env: the list in write_env of
// scripts/install.sh, in its order. A test reads that list from the script
// and fails when the two drift apart.
var installerVariables = []string{
	"SHIPWICK_HTTP_PORT", "SHIPWICK_HTTPS_PORT",
	"SHIPWICK_AGENT_IMAGE", "SHIPWICK_DASHBOARD_IMAGE", "SHIPWICK_CADDY_IMAGE", "SHIPWICK_SOCKET_PROXY_IMAGE",
	"SHIPWICK_WEBHOOK_URL", "SHIPWICK_WEBHOOK_SECRET",
	"SHIPWICK_CLOUDFLARE_API_TOKEN",
	"SHIPWICK_ALERT_MEMORY_PERCENT", "SHIPWICK_ALERT_DISK_PERCENT",
	"SHIPWICK_BACKUP_DIR", "SHIPWICK_BACKUP_PASSPHRASE",
	"SHIPWICK_BACKUP_S3_ENDPOINT", "SHIPWICK_BACKUP_S3_BUCKET", "SHIPWICK_BACKUP_S3_ACCESS_KEY_ID", "SHIPWICK_BACKUP_S3_SECRET_ACCESS_KEY", "SHIPWICK_BACKUP_S3_REGION", "SHIPWICK_BACKUP_S3_PREFIX",
	"SHIPWICK_EXPORT_SCHEDULE", "SHIPWICK_EXPORT_KEEP",
	"SHIPWICK_STANDBY_SCHEDULE",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
	"SHIPWICK_CA_FILE", "SHIPWICK_DNS_RESOLVERS", "SHIPWICK_ACME_DIRECTORY",
	"SHIPWICK_OIDC_ISSUER", "SHIPWICK_OIDC_CLIENT_ID", "SHIPWICK_OIDC_CLIENT_SECRET", "SHIPWICK_OIDC_SCOPES", "SHIPWICK_OIDC_GROUPS_CLAIM", "SHIPWICK_OIDC_NAME_CLAIM", "SHIPWICK_OIDC_TENANTS",
	"SHIPWICK_AGENTS",
	"SHIPWICK_LOG_RETENTION_DAYS", "SHIPWICK_LOG_RETENTION_SIZE",
	"SHIPWICK_UPDATE_CHECK",
}

// The two hostnames are written by the installer whatever the environment
// says, so they are not in its list; they are settings all the same.
const (
	agentDomainVariable     = "SHIPWICK_AGENT_DOMAIN"
	dashboardDomainVariable = "SHIPWICK_DASHBOARD_DOMAIN"
	cloudflareTokenVariable = "SHIPWICK_CLOUDFLARE_API_TOKEN"
)

// configurable reports whether name is a setting `server configure` writes.
func configurable(name string) bool {
	return name == agentDomainVariable || name == dashboardDomainVariable || slices.Contains(installerVariables, name)
}

// envChange is one line of .env to set, or to remove when value is "".
type envChange struct{ name, value string }

var variableName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// parseEnvChanges validates KEY=value arguments and --unset names. Every
// name must be one the agent reads: a misspelling would otherwise sit in
// .env and change nothing, in silence. Values are checked as the installer
// checks them, in its words, so that what it would refuse at installation
// is refused here too.
func parseEnvChanges(assignments, unset []string) ([]envChange, error) {
	var changes []envChange
	seen := map[string]bool{}
	add := func(name, value string) error {
		switch {
		case name == "SHIPWICK_AGENT_TOKEN":
			return errors.New("SHIPWICK_AGENT_TOKEN is the root token, not a setting: tokens for people and pipelines are made with shipwick token create")
		case name == "SHIPWICK_ENCRYPTION_KEY":
			return errors.New("SHIPWICK_ENCRYPTION_KEY is replaced with: shipwick server rotate-key")
		case !variableName.MatchString(name) || !configurable(name):
			return fmt.Errorf("%s is not a setting of the agent; the settings are the ones install.sh --help lists, and the two hostnames", name)
		case seen[name]:
			return fmt.Errorf("%s is given twice", name)
		}
		seen[name] = true
		if value == "" {
			changes = append(changes, envChange{name: name})
			return nil
		}
		if err := checkValue(name, value); err != nil {
			return err
		}
		changes = append(changes, envChange{name: name, value: value})
		return nil
	}
	for _, a := range assignments {
		name, value, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not KEY=value; to remove a setting, give KEY= or --unset KEY", a)
		}
		if err := add(name, value); err != nil {
			return nil, err
		}
	}
	for _, name := range unset {
		if err := add(name, ""); err != nil {
			return nil, err
		}
	}
	if len(changes) == 0 {
		return nil, errors.New("nothing to change: give at least one KEY=value, or --unset KEY")
	}
	var agentDomain, dashboardDomain string
	for _, ch := range changes {
		switch ch.name {
		case agentDomainVariable:
			agentDomain = ch.value
		case dashboardDomainVariable:
			dashboardDomain = ch.value
		}
	}
	if agentDomain != "" && agentDomain == dashboardDomain {
		return nil, errors.New("The API and the dashboard need two different hostnames.")
	}
	return changes, nil
}

var cloudflareTokenChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// checkValue is write_env's checks of scripts/install.sh, one per setting
// it checks; and a line break, which the file could not carry, for all.
func checkValue(name, value string) error {
	if strings.ContainsAny(value, "\n\r\x00") {
		return fmt.Errorf("%s: the value holds a line break, which a line of .env cannot carry", name)
	}
	switch name {
	case cloudflareTokenVariable:
		if !cloudflareTokenChars.MatchString(value) {
			return errors.New("SHIPWICK_CLOUDFLARE_API_TOKEN is not a Cloudflare API token: letters, digits, - and _ only, without quotes.")
		}
	case agentDomainVariable, dashboardDomainVariable:
		if spec.ValidateDomain(strings.ToLower(value)) != nil || strings.ToLower(value) != value {
			return fmt.Errorf("%s: %q is not a hostname. Give a bare name such as agent.example.com — no https://, port or path.", name, value)
		}
	}
	return nil
}

// remoteConfigure is the one remote command of `server configure`, a fixed
// string like the installer's. The changes arrive on its standard input as
// KEY=value lines — the file's own format — and never as part of the
// command: nothing of a value is read by a shell. It rewrites .env through a
// file made under umask 077, so the token in it is never readable to
// anyone else even for a moment, reports each name on standard output, and
// restarts the agent only when a line changed.
const remoteConfigure = `set -eu
dir=/opt/shipwick
[ -f "$dir/.env" ] || exit 10
cd "$dir"
umask 077
changes="$(mktemp "$dir/.env.changes.XXXXXX")"
new="$(mktemp "$dir/.env.new.XXXXXX")"
trap 'rm -f "$changes" "$new"' EXIT
cat > "$changes"
report="$(awk -v out="$new" '
NR == FNR { key = $0; sub(/=.*/, "", key); want[key] = substr($0, length(key) + 2); order[n++] = key; next }
/^[A-Za-z_][A-Za-z0-9_]*=/ {
    key = $0; sub(/=.*/, "", key)
    if (key in want) {
        if (key in seen) next
        seen[key] = 1
        if (want[key] == "") { print "removed " key; next }
        print (want[key] == substr($0, length(key) + 2) ? "unchanged " : "changed ") key
        print key "=" want[key] > out
        next
    }
}
{ print > out }
END {
    for (i = 0; i < n; i++) {
        key = order[i]
        if (key in seen) continue
        if (want[key] == "") print "absent " key
        else { print "added " key; print key "=" want[key] > out }
    }
}' "$changes" .env)"
printf '%s\n' "$report"
printf '%s\n' "$report" | grep -Eq '^(changed|added|removed) ' || exit 0
mv "$new" .env
chmod 0600 .env
echo restarting
docker compose up -d >&2 || exit 12
i=0
until docker compose exec -T agent shipwick-agent healthcheck >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || exit 11
    sleep 1
done
echo healthy
`

// The script's exit statuses, each one sentence here.
const (
	exitNotInstalled  = 10
	exitNotHealthy    = 11
	exitComposeFailed = 12
)

func (c *cli) serverConfigureCommand() *cobra.Command {
	var unset []string
	cmd := &cobra.Command{
		Use:   "configure <user@host> KEY=value [KEY=value ...]",
		Short: "Change the agent's settings on the server over SSH, and restart it",
		Long: `Change the agent's settings on the server over SSH, from this machine.

The settings are the SHIPWICK_* variables of /opt/shipwick/.env, the ones the
installer takes from its environment; the handbook's table (Installation)
lists them. Each KEY=value sets or replaces its line, KEY= or --unset KEY
removes it, then the agent is restarted and waited for:

  shipwick server configure root@203.0.113.10 SHIPWICK_WEBHOOK_URL=https://hooks.example.com/shipwick
  shipwick server configure root@203.0.113.10 --unset SHIPWICK_WEBHOOK_URL

Values travel to the server as data, never as part of a command, and are not
printed. The token in .env is never touched; the file stays readable to root
only.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.serverConfigure(cmd.Context(), args[0], args[1:], unset)
		},
	}
	cmd.Flags().StringArrayVar(&unset, "unset", nil, "remove this setting from .env (repeatable)")
	return cmd
}

func (c *cli) serverConfigure(ctx context.Context, target string, assignments, unset []string) error {
	t, err := parseSSHTarget(target)
	if err != nil {
		return err
	}
	changes, err := parseEnvChanges(assignments, unset)
	if err != nil {
		return err
	}
	local := c.local.withDefaults()

	var input strings.Builder
	for _, ch := range changes {
		input.WriteString(ch.name + "=" + ch.value + "\n")
	}
	// Compose talks on stderr while it recreates the agent; shown dimmed, as
	// the installer's output is. The report is ours to read.
	var report bytes.Buffer
	dim := &dimmedLines{c: c}
	c.ui.Progress("Connecting to %s", t)
	err = local.execInput(ctx, sshArgv(t, remoteConfigure), strings.NewReader(input.String()), &report, dim)
	c.ui.Done()
	dim.flush()
	if err != nil {
		switch remoteExitCode(err) {
		case exitNotInstalled:
			return fmt.Errorf("%s has no /opt/shipwick/.env: Shipwick is not installed there. Install it with: shipwick server install %s", t.host, t)
		case exitComposeFailed:
			return fmt.Errorf("docker compose up failed on %s; its output is above. The settings were written to /opt/shipwick/.env; once the cause is fixed, on the server: cd /opt/shipwick && docker compose up -d", t)
		case exitNotHealthy:
			return fmt.Errorf("the agent on %s did not become healthy within a minute of the change. Look at: ssh %s 'cd /opt/shipwick && docker compose logs agent'; to go back, run this again with the previous values", t, t)
		}
		return c.explainSSHFailure(ctx, local, t, err, strings.Join(dim.lines, "\n"))
	}
	c.printConfigured(t, report.String())
	return nil
}

// printConfigured turns the script's report into the summary: what changed,
// by name, and what to do next for the settings that have a next step.
func (c *cli) printConfigured(t sshTarget, report string) {
	var changed, same, absent []string
	restarted, healthy := false, false
	for _, line := range strings.Split(report, "\n") {
		verb, name, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch verb {
		case "changed", "added", "removed":
			changed = append(changed, name+" "+verb)
		case "unchanged":
			same = append(same, name)
		case "absent":
			absent = append(absent, name)
		case "restarting":
			restarted = true
		case "healthy":
			healthy = true
		}
	}
	if len(changed) == 0 {
		c.ui.Success("Nothing to change in /opt/shipwick/.env on %s; the agent was not restarted", t)
	} else {
		c.ui.Success("/opt/shipwick/.env on %s: %s", t, strings.Join(changed, ", "))
	}
	if len(same) > 0 {
		c.ui.Println("  " + strings.Join(same, ", ") + ": already that value")
	}
	if len(absent) > 0 {
		c.ui.Println("  " + strings.Join(absent, ", ") + ": not in the file")
	}
	switch {
	case healthy:
		c.ui.Success("The agent is healthy")
	case restarted:
		c.ui.Println("The agent was restarted.")
	}
	for _, entry := range changed {
		name, verb, _ := strings.Cut(entry, " ")
		switch {
		case name == cloudflareTokenVariable && verb != "removed":
			c.ui.Println("Next: check that the token can do its job with: shipwick doctor")
		case name == agentDomainVariable && verb != "removed":
			c.ui.Println("Next: point an A record for the API's hostname at the server, then sign in to it: shipwick login --url https://<hostname>")
		case name == dashboardDomainVariable && verb != "removed":
			c.ui.Println("Next: point an A record for the dashboard's hostname at the server; shipwick doctor checks it")
		}
	}
}

// remoteExitCode is the remote command's exit status, from the process or,
// where the program was stood in for, from the words ssh would use.
func remoteExitCode(err error) int {
	if code := sshExitCode(err); code >= 0 {
		return code
	}
	if rest, ok := strings.CutPrefix(err.Error(), "exit status "); ok {
		if code, convErr := strconv.Atoi(rest); convErr == nil {
			return code
		}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
