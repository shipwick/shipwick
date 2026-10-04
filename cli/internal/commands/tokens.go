package commands

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// tokenCommands returns the token commands.
func (c *cli) tokenCommands() []*cobra.Command {
	token := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens and their roles",
		Long: `Manage API tokens and their roles.

A token has one of three roles. read sees everything: status, logs, history,
metrics. deploy also changes what runs: deploy, redeploy, roll back, stop,
start. admin also does the rest: delete applications, manage tokens and
secrets. Give CI a
deploy token and keep admin tokens for people. Managing tokens needs admin.

A deploy token can be limited to applications with --app: it deploys, stops
and runs commands in those, reads everything like any token, and is refused
whatever is not about one of its applications. A token can be given an end
with --expires; from then on it is refused, and says that it expired. Both
can be changed later with "shipwick token update"; the role cannot.

The token the agent is configured with (SHIPWICK_AGENT_TOKEN, or the one it
generated on first start) is "root": admin, not listed here, not revocable
here — change it on the agent.`,
	}
	token.AddCommand(c.tokenCreateCommand(), c.tokenListCommand(), c.tokenRevokeCommand(), c.tokenUpdateCommand())
	return []*cobra.Command{token}
}

func (c *cli) tokenCreateCommand() *cobra.Command {
	var (
		role, expires string
		apps          []string
	)
	cmd := &cobra.Command{
		Use:   "create <name> --role read|deploy|admin",
		Short: "Create a token; its value is shown once",
		Example: `  shipwick token create laptop --role admin
  shipwick token create ci --role deploy --app my-api --app web --expires 90d
  shipwick token create auditor --role read --expires 2027-01-31`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := api.ValidateTokenName(name); err != nil {
				return err
			}
			if role == "" {
				return errors.New("choose what the token may do: --role read, deploy or admin")
			}
			if err := api.ValidateRole(api.Role(role)); err != nil {
				return err
			}
			req := api.CreateTokenRequest{Name: name, Role: api.Role(role)}
			if len(apps) > 0 {
				limited, err := api.ValidateTokenApplications(req.Role, apps)
				if err != nil {
					return fmt.Errorf("--app: %w", err)
				}
				req.Applications = limited
			}
			if expires != "" {
				at, err := api.ParseExpiry(expires, c.now())
				if err != nil {
					return fmt.Errorf("--expires: %w", err)
				}
				req.ExpiresAt = &at
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			created, err := cl.CreateToken(cmd.Context(), req)
			if err != nil {
				// An agent from before limits and expiry refuses the fields it
				// does not know, rather than create a token without them.
				if (len(req.Applications) > 0 || req.ExpiresAt != nil) && client.IsCode(err, api.CodeInvalidRequest) && strings.Contains(err.Error(), "unknown field") {
					return errors.New("the agent is older than this shipwick: it knows neither --app nor --expires, and created nothing\n\nCompare versions with: shipwick server status")
				}
				return err
			}

			summary := fmt.Sprintf("Created token %s with the %s role", created.Name, created.Role)
			if len(req.Applications) > 0 {
				summary += ", limited to " + strings.Join(req.Applications, ", ")
			}
			c.ui.Success("%s", summary)
			if req.ExpiresAt != nil {
				c.ui.Println("  It expires on " + req.ExpiresAt.Local().Format("2006-01-02 at 15:04") + ", in " + span(req.ExpiresAt.Sub(c.now())) + ".")
			}
			c.ui.Println()
			c.ui.Println("    " + c.ui.Styled(ui.Bold, created.Token))
			c.ui.Println()
			c.ui.Println("Store it now: it will not be shown again.")
			c.ui.Println(c.ui.Styled(ui.Dim, "In CI, set "+cliconfig.EnvToken+" to it. On a machine you work from, save it with: shipwick login"))
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "what the token may do: read, deploy or admin")
	cmd.Flags().StringArrayVar(&apps, "app", nil, "limit a deploy token to this application; repeat for several")
	cmd.Flags().StringVar(&expires, "expires", "", "when the token stops working: days or hours from now (90d, 12h) or a date (2027-01-31)")
	return cmd
}

func (c *cli) tokenListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the tokens, what each may do, when it expires and when it was last used",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			tokens, err := cl.Tokens(cmd.Context())
			if err != nil {
				return err
			}
			if len(tokens) == 0 {
				c.ui.Println("No tokens besides the one the agent is configured with. Create one with: shipwick token create ci --role deploy")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(tokens))
			var lapsing []string
			for _, t := range tokens {
				lastUsed := "never"
				if t.LastUsedAt != nil {
					lastUsed = ui.RelativeTime(*t.LastUsedAt, now)
				}
				applications := "all"
				if len(t.Applications) > 0 {
					applications = strings.Join(t.Applications, ", ")
				}
				expires, style := expiryText(t.ExpiresAt, now)
				if style != ui.Plain {
					lapsing = append(lapsing, t.Name)
				}
				rows = append(rows, []ui.Cell{
					ui.C(t.Name),
					ui.C(string(t.Role)),
					ui.C(applications),
					{Text: expires, Style: style},
					ui.C(ui.RelativeTime(t.CreatedAt, now)),
					ui.C(lastUsed),
				})
			}
			c.ui.Table([]string{"NAME", "ROLE", "APPLICATIONS", "EXPIRES", "CREATED", "LAST USED"}, rows)
			if len(lapsing) > 0 {
				c.ui.Println()
				c.ui.Println("Expired, or expiring within 14 days: " + strings.Join(lapsing, ", ") + ". Move an end with: shipwick token update <name> --expires 90d")
			}
			return nil
		},
	}
}

func (c *cli) tokenRevokeCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a token; whatever uses it is refused from then on",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == api.RootTokenName {
				return errors.New("the root token is the one the agent is configured with; change " + cliconfig.EnvToken + " on the agent instead")
			}
			if err := api.ValidateTokenName(name); err != nil {
				return err
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to revoke without confirmation; pass --yes")
				}
				c.ui.Printf("This revokes token %s: whatever uses it is refused from now on.\nType the token name to confirm: ", name)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if strings.TrimSpace(answer) != name {
					return errors.New("cancelled: the name did not match")
				}
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.RevokeToken(cmd.Context(), name); err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("there is no token named %s\n\nList the tokens with: shipwick token ls", name)
				}
				return err
			}
			c.ui.Success("Revoked token %s", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// renderForbidden tells the user which role they have and which one they
// need. The agent puts both in the error's details; an agent that does not
// is quoted instead.
func renderForbidden(e *client.APIError) string {
	role, _ := e.Details["role"].(string)
	required, _ := e.Details["required"].(string)
	if role == "" || required == "" {
		return "This token may not do that: " + e.Message
	}
	return fmt.Sprintf("This token may not do that: it has the %s role.\n\n"+
		"Use a token with the %s role, or create one with: shipwick token create <name> --role %s", role, required, required)
}
