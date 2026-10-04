package commands

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

func (c *cli) tokenUpdateCommand() *cobra.Command {
	var (
		apps              []string
		expires           string
		allApps, noExpiry bool
	)
	cmd := &cobra.Command{
		Use:   "update <name> [--app <application>... | --all-apps] [--expires <when> | --no-expiry]",
		Short: "Change the applications a token is limited to, or when it expires",
		Long: `Change the applications a token is limited to, or when it expires.

The token's value stays the same: whatever holds it keeps working, and may do
what the token may from its next request on. --app replaces the list, it does
not add to it; --all-apps lifts the limit. --expires moves the end, also of a
token that has expired already, which then works again; --no-expiry takes
the end away.

The role is not changed here: a token that is to do more than it was created
for is a new token. Every change is recorded in the audit trail with what it
was before.`,
		Example: `  shipwick token update ci --app my-api --app web --app worker
  shipwick token update ci --all-apps
  shipwick token update ci --expires 90d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == api.RootTokenName {
				return errors.New("the root token is the one the agent is configured with: it is not limited and does not expire; change " + cliconfig.EnvToken + " on the agent instead")
			}
			if err := api.ValidateTokenName(name); err != nil {
				return err
			}
			var req api.UpdateTokenRequest
			switch {
			case len(apps) > 0 && allApps:
				return errors.New("--app limits the token and --all-apps lifts the limit: use one")
			case expires != "" && noExpiry:
				return errors.New("--expires sets an end and --no-expiry takes it away: use one")
			case len(apps) > 0:
				// Whether the token is a deploy token only the agent knows.
				limited, err := api.ValidateTokenApplications(api.RoleDeploy, apps)
				if err != nil {
					return fmt.Errorf("--app: %w", err)
				}
				req.Applications = &limited
			case allApps:
				req.Applications = &[]string{}
			}
			switch {
			case expires != "":
				at, err := api.ParseExpiry(expires, c.now())
				if err != nil {
					return fmt.Errorf("--expires: %w", err)
				}
				req.ExpiresAt = &at
			case noExpiry:
				req.NeverExpires = true
			}
			if req.Empty() {
				return errors.New("say what to change: --app or --all-apps, --expires or --no-expiry")
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			token, err := cl.UpdateToken(cmd.Context(), name, req)
			if err != nil {
				var apiErr *client.APIError
				switch {
				case client.IsCode(err, api.CodeNotFound):
					return fmt.Errorf("there is no token named %s\n\nList the tokens with: shipwick token ls", name)
				// An agent from before answers the path with the methods it has.
				case errors.As(err, &apiErr) && apiErr.Status == http.StatusMethodNotAllowed:
					return errors.New("the agent is older than this shipwick and cannot change a token: nothing was changed\n\nCompare versions with: shipwick server status\nUntil it is upgraded, create a new token and revoke the old one")
				}
				return err
			}

			c.ui.Success("Changed token %s", token.Name)
			if req.Applications != nil {
				if len(token.Applications) == 0 {
					c.ui.Println("  It is not limited: it may " + roleVerb(token.Role) + " every application.")
				} else {
					c.ui.Println("  It is limited to " + strings.Join(token.Applications, ", ") + ".")
				}
			}
			switch {
			case token.ExpiresAt == nil && req.NeverExpires:
				c.ui.Println("  It does not expire.")
			case token.ExpiresAt != nil && req.ExpiresAt != nil:
				c.ui.Println("  It expires on " + token.ExpiresAt.Local().Format("2006-01-02 at 15:04") + ", in " + span(token.ExpiresAt.Sub(c.now())) + ".")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&apps, "app", nil, "limit the token to this application instead of the ones it had; repeat for several")
	cmd.Flags().BoolVar(&allApps, "all-apps", false, "lift the limit: the token may change every application")
	cmd.Flags().StringVar(&expires, "expires", "", "when the token stops working: days or hours from now (90d, 12h) or a date (2027-01-31)")
	cmd.Flags().BoolVar(&noExpiry, "no-expiry", false, "take the token's end away")
	return cmd
}

// roleVerb is what a role does to applications, for a sentence.
func roleVerb(role api.Role) string {
	if role == api.RoleRead {
		return "read"
	}
	return "change"
}
