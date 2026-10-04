package commands

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// accessCommands returns `shipwick access`.
func (c *cli) accessCommands() []*cobra.Command {
	access := &cobra.Command{
		Use:   "access",
		Short: "Say who may sign in to the dashboard, and as what",
		Long: `Say who may sign in to the dashboard, and as what.

With an OpenID Connect provider configured on the agent (SHIPWICK_OIDC_ISSUER
and the variables next to it), people sign in to the dashboard with the
company's accounts. These rules say what each of them may do here: a role for
an e-mail address, for a group the provider reports, or for everyone at a
domain.

  ada@example.com       one person
  group:platform        everyone the provider puts in that group
  *@example.com         everyone with an address at that domain
  name:248289761001     one person whose name is not an address

The most specific rule that matches decides: the name, else the address, else the groups,
else the domain. Of several groups the highest role counts. Nobody without a
matching rule gets in.

People are named by the e-mail address the provider reports, unless the
agent is told to read another claim (SHIPWICK_OIDC_NAME_CLAIM: a user name,
or the provider's own identifier for the account). Rules by address and by
domain then apply to names that are addresses; name: is for the others, and
has to match the name exactly, capitals included. "shipwick server status"
says which claim it is.

A session lasts ten hours. Revoking or changing the rule a session rests on
ends it with its next request. Managing access needs admin; tokens are not
affected by any of this.`,
	}
	access.AddCommand(c.accessListCommand(), c.accessGrantCommand(), c.accessRevokeCommand(), c.accessSessionsCommand(), c.accessSignOutCommand())
	return []*cobra.Command{access}
}

// olderThanSignIn is what an agent from before sign-in is told apart by.
func olderThanSignIn(err error) error {
	if client.IsCode(err, api.CodeEndpointNotFound) {
		return errors.New("the agent is older than this shipwick and has no sign-in: it accepts tokens only\n\nCompare versions with: shipwick server status")
	}
	return err
}

func (c *cli) accessListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the rules: who gets which role",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			rules, err := cl.AccessRules(cmd.Context())
			if err != nil {
				return olderThanSignIn(err)
			}
			if len(rules) == 0 {
				c.ui.Println("No rules: nobody can sign in. Grant access with: shipwick access grant ada@example.com --role deploy")
			} else {
				now := c.now()
				rows := make([][]ui.Cell, 0, len(rules))
				for _, r := range rules {
					applications := "all"
					if len(r.Applications) > 0 {
						applications = strings.Join(r.Applications, ", ")
					}
					rows = append(rows, []ui.Cell{ui.C(r.Who()), ui.C(string(r.Role)), ui.C(applications), ui.C(ui.RelativeTime(r.CreatedAt, now)), ui.C(r.CreatedBy)})
				}
				c.ui.Table([]string{"WHO", "ROLE", "APPLICATIONS", "GRANTED", "BY"}, rows)
			}
			// The rules are kept whether or not there is a provider to sign
			// in with; say so, or they look like they do something.
			if info, err := cl.Server(cmd.Context()); err == nil && !info.SignIn.Configured {
				c.ui.Println()
				c.ui.Println(c.ui.Styled(ui.Dim, "Signing in is not configured on this agent, so no rule applies yet: set SHIPWICK_OIDC_ISSUER, SHIPWICK_OIDC_CLIENT_ID and SHIPWICK_OIDC_CLIENT_SECRET in /opt/shipwick/.env."))
			}
			return nil
		},
	}
}

func (c *cli) accessGrantCommand() *cobra.Command {
	var (
		role string
		apps []string
	)
	cmd := &cobra.Command{
		Use:   "grant <address | *@domain | group:name | name:value> --role read|deploy|admin",
		Short: "Give a person, a group or a domain a role; granting again changes it",
		Example: `  shipwick access grant ada@example.com --role admin
  shipwick access grant group:backend --role deploy --app my-api --app worker
  shipwick access grant '*@example.com' --role read
  shipwick access grant name:248289761001 --role deploy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, subject, err := api.ParseAccessSubject(args[0])
			if err != nil {
				return err
			}
			if role == "" {
				return errors.New("choose what they may do: --role read, deploy or admin")
			}
			if err := api.ValidateRole(api.Role(role)); err != nil {
				return err
			}
			req := api.GrantAccessRequest{Kind: kind, Subject: subject, Role: api.Role(role)}
			if len(apps) > 0 {
				if req.Role != api.RoleDeploy {
					return errors.New("--app: only the deploy role can be limited to applications: read changes nothing, and admin is for the whole server")
				}
				limited, err := api.ValidateTokenApplications(req.Role, apps)
				if err != nil {
					return fmt.Errorf("--app: %s", strings.Replace(err.Error(), "a token", "a rule", 1))
				}
				req.Applications = limited
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			rule, err := cl.GrantAccess(cmd.Context(), req)
			if err != nil {
				return olderThanSignIn(err)
			}
			summary := fmt.Sprintf("%s has the %s role", rule.Who(), rule.Role)
			if len(rule.Applications) > 0 {
				summary += ", limited to " + strings.Join(rule.Applications, ", ")
			}
			c.ui.Success("%s", summary)
			if info, err := cl.Server(cmd.Context()); err == nil {
				if note := ruleNote(rule, info.SignIn); note != "" {
					c.ui.Println(c.ui.Styled(ui.Dim, note))
				}
			}
			c.ui.Println(c.ui.Styled(ui.Dim, "Whoever is signed in and gets something else by this is signed out with their next request, and signs in again."))
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "what they may do: read, deploy or admin")
	cmd.Flags().StringArrayVar(&apps, "app", nil, "limit the deploy role to this application; repeat for several")
	return cmd
}

func (c *cli) accessRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <address | *@domain | group:name | name:value>",
		Short: "Remove a rule; sessions that rested on it end at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, subject, err := api.ParseAccessSubject(args[0])
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			rules, err := cl.AccessRules(cmd.Context())
			if err != nil {
				return olderThanSignIn(err)
			}
			for _, r := range rules {
				if r.Kind != kind || r.Subject != subject {
					continue
				}
				if err := cl.RevokeAccess(cmd.Context(), r.ID); err != nil && !client.IsCode(err, api.CodeNotFound) {
					return err
				}
				c.ui.Success("Revoked the rule for %s", r.Who())
				c.ui.Println(c.ui.Styled(ui.Dim, "Whoever signed in through it is signed out with their next request, unless another rule gives them the same."))
				return nil
			}
			return fmt.Errorf("there is no rule for %s\n\nList the rules with: shipwick access ls", args[0])
		},
	}
}

func (c *cli) accessSessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List who is signed in right now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			sessions, err := cl.Sessions(cmd.Context())
			if err != nil {
				return olderThanSignIn(err)
			}
			if len(sessions) == 0 {
				c.ui.Println("Nobody is signed in.")
				return nil
			}
			now := c.now()
			rows := make([][]ui.Cell, 0, len(sessions))
			for _, s := range sessions {
				applications, lastUsed := "all", "never"
				if len(s.Applications) > 0 {
					applications = strings.Join(s.Applications, ", ")
				}
				if s.LastUsedAt != nil {
					lastUsed = ui.RelativeTime(*s.LastUsedAt, now)
				}
				rows = append(rows, []ui.Cell{ui.C(s.Email), ui.C(string(s.Role)), ui.C(applications), ui.C(ui.RelativeTime(s.CreatedAt, now)),
					ui.C("in " + span(s.ExpiresAt.Sub(now).Round(time.Minute))), ui.C(lastUsed)})
			}
			c.ui.Table([]string{"WHO", "ROLE", "APPLICATIONS", "SIGNED IN", "ENDS", "LAST USED"}, rows)
			return nil
		},
	}
}

func (c *cli) accessSignOutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "signout <address | name>",
		Short: "End every session of a person",
		Long: `End every session of a person.

This signs the person out; it does not keep them out. While a rule covers
them they can sign in again. To keep someone out, disable the account at the
provider, or revoke the rule with: shipwick access revoke`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			who := strings.TrimSpace(args[0])
			if strings.HasPrefix(who, "group:") || strings.HasPrefix(who, "*@") {
				return fmt.Errorf("%s is not a person: sessions are ended for one person at a time, as shipwick access sessions lists them", who)
			}
			who = strings.TrimPrefix(who, "name:")
			cl, err := c.connect()
			if err != nil {
				return err
			}
			// An address is brought to lowercase, as the agent keeps it. Where
			// the agent names people by another claim the name is sent as it
			// was typed: there, capitals tell two people apart.
			email, err := api.NormalizeEmail(who)
			if info, serr := cl.Server(cmd.Context()); serr == nil && info.SignIn.NameClaim != "" && info.SignIn.NameClaim != api.DefaultNameClaim {
				email, err = who, api.ValidatePersonName(who)
			}
			if err != nil {
				return fmt.Errorf("%q is %w", args[0], err)
			}
			n, err := cl.EndSessions(cmd.Context(), email)
			if err != nil {
				return olderThanSignIn(err)
			}
			if n == 0 {
				c.ui.Println(email + " was not signed in.")
				return nil
			}
			c.ui.Success("Signed %s out of %s", email, plural(n, "session"))
			return nil
		},
	}
}

// renderSessionOver tells whoever runs the CLI with a session that it is
// over. A session is the dashboard's; the CLI is meant to be given a token.
func renderSessionOver(e *client.APIError) string {
	return "The session this command ran with no longer works: " + e.Message + ".\n\nThe CLI and CI are meant to be given a token; an admin creates one with: shipwick token create"
}

// ruleNote says when a rule that was just granted cannot match anyone the
// way the agent names people, or matches fewer than it reads as if it did.
func ruleNote(rule api.AccessRule, signIn api.SignInStatus) string {
	claim := signIn.NameClaim
	if !signIn.Configured || claim == "" {
		return ""
	}
	switch {
	case rule.Kind == api.AccessName && claim == api.DefaultNameClaim:
		return "This agent names people by their e-mail address: a rule by name matches an address written exactly as the agent keeps it, in lowercase. A rule for the address itself says the same more plainly."
	case (rule.Kind == api.AccessEmail || rule.Kind == api.AccessDomain) && claim != api.DefaultNameClaim:
		return "This agent names people by the " + claim + " claim: the rule applies to those whose " + claim + " is an address. For anyone else use name:<" + claim + ">."
	}
	return ""
}
