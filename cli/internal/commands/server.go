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
		Short: "Inspect the Shipwick server",
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
			c.ui.Println(c.ui.Styled(ui.Bold, cl.URL()) + "  " + c.ui.Styled(ui.Green, "● reachable"))
			c.ui.Println()

			info, err := cl.Server(ctx)
			if err != nil {
				c.ui.Fields([][2]string{{"Agent", health.Version}})
				c.ui.Println()
				return err
			}
			c.ui.Fields([][2]string{
				{"Agent", info.AgentVersion},
				{"CLI", version.Version},
				{"Host", info.Hostname},
				{"OS", fmt.Sprintf("%s (%s, kernel %s)", info.OS, info.Architecture, info.Kernel)},
				{"Docker", info.DockerVersion},
				{"CPUs", fmt.Sprint(info.CPUs)},
				{"Memory", spec.FormatMemory(info.MemoryBytes)},
				{"Applications", fmt.Sprint(info.Applications)},
				{"Containers", fmt.Sprintf("%d running", info.Containers)},
				{"Proxy", c.describeProxy(info.Proxy)},
			})
			return nil
		},
	})
	return server
}

func (c *cli) describeProxy(p api.ProxyStatus) string {
	switch {
	case !p.Enabled:
		return c.ui.Styled(ui.Yellow, "not configured") + c.ui.Styled(ui.Dim, "  (domains are not served; set SHIPWICK_CADDY_ADMIN on the agent)")
	case !p.Reachable:
		return c.ui.Styled(ui.Red, "unreachable") + "  " + p.Error
	case p.Routes == 1:
		return c.ui.Styled(ui.Green, "ok") + "  serving 1 domain"
	}
	return c.ui.Styled(ui.Green, "ok") + fmt.Sprintf("  serving %d domains", p.Routes)
}

func (c *cli) loginCommand() *cobra.Command {
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save the agent URL and API token for later commands",
		Long: `Save the agent URL and API token for later commands.

The token is asked for without echo, verified against the agent, and stored in
a config file only you can read. In scripts, pipe it in:

  printf %s "$TOKEN" | deployctl login --url https://agent.example.com --token-stdin

CI jobs usually need no login at all: set SHIPWICK_AGENT_URL and
SHIPWICK_AGENT_TOKEN instead.`,
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

			// Only the URL is resolved from the environment; the token must
			// be given afresh, that being the point of logging in.
			url := cliconfig.Resolve(c.flagURL, c.getenv, saved).URL
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
			info, err := cl.Server(cmd.Context())
			if err != nil {
				if client.IsCode(err, api.CodeUnauthorized) {
					return errors.New("the agent rejected this token; nothing was saved")
				}
				return err
			}

			if err := cliconfig.Save(path, cliconfig.Config{URL: cl.URL(), Token: token}); err != nil {
				return err
			}
			c.ui.Success("Logged in to %s (%s, agent %s)", cl.URL(), info.Hostname, info.AgentVersion)
			c.ui.Println(c.ui.Styled(ui.Dim, "  saved to "+path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the token from standard input")
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
