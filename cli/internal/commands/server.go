package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

func (c *cli) serverCommand() *cobra.Command {
	server := &cobra.Command{
		Use:   "server",
		Short: "Inspect or install the Shipwick server",
	}
	server.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether the agent is reachable, and what it runs on",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			// Health needs no token, so it separates "cannot reach the
			// agent" from "reached it, but the token is wrong".
			health, err := cl.Health(ctx)
			if err != nil {
				return err
			}
			where := c.ui.Styled(ui.Bold, cl.URL())
			if c.severalContexts && c.target.Context != "" {
				where += c.ui.Styled(ui.Dim, "  context "+c.target.Context)
			}
			c.ui.Println(where + "  " + c.ui.Styled(ui.Green, "● reachable"))
			c.ui.Println()

			info, err := cl.Server(ctx)
			if err != nil {
				c.ui.Fields([][2]string{{"Agent", health.Version}})
				c.ui.Println()
				return err
			}
			fields := [][2]string{
				{"Agent", c.describeAgent(info)},
				{"CLI", version.Version},
				{"Host", info.Hostname},
				{"OS", fmt.Sprintf("%s (%s, kernel %s)", info.OS, info.Architecture, info.Kernel)},
				{"Docker", info.DockerVersion},
				{"CPUs", fmt.Sprint(info.CPUs)},
				{"Memory", describeMemory(info)},
				{"Applications", fmt.Sprint(info.Applications)},
				{"Containers", fmt.Sprintf("%d running", info.Containers)},
				{"Proxy", c.describeProxy(info.Proxy)},
				{"Notifications", c.describeNotifications(info.Notifications)},
			}
			// An agent from before tokens had names leaves this empty.
			if info.Token.Name != "" {
				fields = append(fields, [2]string{"Token", describeCaller(info.Token, c.now())})
			}
			fields = append(fields, [2]string{"Dashboard", c.describeDashboard(info.DashboardURL)})
			// People sign in to the dashboard only where a provider is configured.
			if info.SignIn.Configured {
				fields = append(fields, [2]string{"Sign-in", signInText(info.SignIn)})
			}
			// Absent from an older agent, and where the agent cannot measure it.
			if info.Disk != nil {
				fields = append(fields, [2]string{"Disk", describeDisk(*info.Disk)})
			}
			// Only a server that does not reach the internet the plain way has one.
			if network := describeNetwork(info.Network); network != "" {
				fields = append(fields, [2]string{"Network", network})
			}
			// Absent from an agent older than the log archive.
			if info.LogArchive != nil {
				fields = append(fields, [2]string{"Log archive", describeLogArchive(*info.LogArchive)})
			}
			c.ui.Fields(fields)
			c.printAlerts(info.Alerts)
			return nil
		},
	})
	server.AddCommand(c.serverInstallCommand())
	server.AddCommand(c.serverRotateKeyCommand())
	server.AddCommand(c.serverBackupCommand())
	server.AddCommand(c.serverBundleCommand())
	return server
}

func (c *cli) describeProxy(p api.ProxyStatus) string {
	switch {
	case !p.Enabled:
		return c.ui.Styled(ui.Yellow, "not configured") + c.ui.Styled(ui.Dim, "  (domains are not served; set SHIPWICK_CADDY_ADMIN on the agent)")
	case !p.Reachable:
		return c.ui.Styled(ui.Red, "unreachable") + "  " + p.Error
	case p.Routes == 1:
		return c.ui.Styled(ui.Green, "ok") + "  serving 1 route"
	}
	return c.ui.Styled(ui.Green, "ok") + fmt.Sprintf("  serving %d routes", p.Routes)
}

func (c *cli) describeNotifications(n api.NotificationStatus) string {
	if n.Webhook {
		return "webhook configured"
	}
	return "none" + c.ui.Styled(ui.Dim, "  (set SHIPWICK_WEBHOOK_URL on the agent)")
}

func (c *cli) describeDashboard(url string) string {
	if url != "" {
		return url
	}
	return "no hostname" + c.ui.Styled(ui.Dim, "  (set SHIPWICK_DASHBOARD_DOMAIN on the agent)")
}

func (c *cli) loginCommand() *cobra.Command {
	var tokenStdin, noCheck bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save the agent URL and API token for later commands",
		Long: `Save the agent URL and API token for later commands.

The token is asked for without echo, verified against the agent, and stored in
a config file only you can read. In scripts, pipe it in:

  printf %s "$TOKEN" | shipwick login --url https://agent.example.com --token-stdin

CI jobs usually need no login at all: set SHIPWICK_AGENT_URL and
SHIPWICK_AGENT_TOKEN instead.

A second server gets a name of its own and becomes the current one:

  shipwick login --context staging --url https://staging.example.com

Later commands take --context, or SHIPWICK_CONTEXT, to pick one.

--no-check saves the URL and the token as given, without a request to the
agent. The installer uses it on the server, where the token is known to be
right before the API's hostname has a DNS record or a certificate.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := cliconfig.Path(c.getenv)
			if err != nil {
				return err
			}
			saved, err := cliconfig.Load(path)
			if err != nil {
				return err
			}
			name := cliconfig.ContextName(c.flagContext, c.getenv, saved)
			if name == "" {
				name = cliconfig.DefaultContext
			}

			// Only the URL is resolved from the environment and the context
			// being (re)created; the token must be given afresh, that being
			// the point of logging in.
			url := saved.Contexts[name].URL
			if url == "" {
				url = cliconfig.DefaultURL
			}
			if v := c.getenv(cliconfig.EnvURL); v != "" {
				url = v
			}
			if c.flagURL != "" {
				url = c.flagURL
			}
			interactive := isTerminal(c.in)
			if c.flagURL == "" && interactive {
				c.ui.Printf("Agent URL [%s]: ", url)
				line, _ := bufio.NewReader(c.in).ReadString('\n')
				if line = strings.TrimSpace(line); line != "" {
					url = line
				}
			}

			token, err := c.readToken(tokenStdin, interactive)
			if err != nil {
				return err
			}

			cl, err := client.New(url, token)
			if err != nil {
				return err
			}
			if cl.SendsTokenInCleartext() {
				c.ui.Warn("%s is unencrypted HTTP: the token can be read by anyone on the network path. Prefer HTTPS or an SSH tunnel.", cl.URL())
			}
			if noCheck {
				saved.Set(name, cliconfig.Context{URL: cl.URL(), Token: token})
				if err := cliconfig.Save(path, saved); err != nil {
					return err
				}
				c.ui.Success("Saved %s as context %s in %s", cl.URL(), name, path)
				c.ui.Println(c.ui.Styled(ui.Dim, "  not checked against the agent; try it with: shipwick server status"))
				return nil
			}
			info, err := cl.Server(cmd.Context())
			if err != nil {
				if client.IsCode(err, api.CodeUnauthorized) {
					return errors.New("the agent rejected this token; nothing was saved")
				}
				return err
			}

			saved.Set(name, cliconfig.Context{URL: cl.URL(), Token: token})
			if err := cliconfig.Save(path, saved); err != nil {
				return err
			}
			c.ui.Success("Logged in to %s (%s, agent %s)", cl.URL(), info.Hostname, info.AgentVersion)
			c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  saved as context %s in %s", name, path)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the token from standard input")
	cmd.Flags().BoolVar(&noCheck, "no-check", false, "save without asking the agent whether the token is right")
	return cmd
}

func (c *cli) readToken(fromStdin, interactive bool) (string, error) {
	var raw []byte
	var err error
	switch {
	case fromStdin:
		raw, err = io.ReadAll(io.LimitReader(c.in, 4096))
	case interactive:
		c.ui.Printf("API token: ")
		raw, err = term.ReadPassword(int(c.in.(*os.File).Fd()))
		c.ui.Println()
	default:
		return "", errors.New("no terminal to ask for the token; pipe it in with --token-stdin")
	}
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("the token is empty")
	}
	return token, nil
}

// signInText is the provider people sign in with, and what it names them by
// where that is not the address.
func signInText(s api.SignInStatus) string {
	if s.NameClaim == "" || s.NameClaim == api.DefaultNameClaim {
		return s.Issuer
	}
	return s.Issuer + " (people are named by the " + s.NameClaim + " claim)"
}

// describeMemory is the server's memory and, where the agent could tell, its
// swap: none is worth saying.
func describeMemory(info api.Server) string {
	memory := spec.FormatMemory(info.MemoryBytes)
	switch {
	case info.SwapBytes == nil:
		return memory
	case *info.SwapBytes == 0:
		return memory + ", no swap"
	default:
		return memory + ", " + spec.FormatMemory(*info.SwapBytes) + " swap"
	}
}
