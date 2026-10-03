package commands

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// registryCommands are the commands for the registry credentials the agent keeps.
func (c *cli) registryCommands() []*cobra.Command {
	registry := &cobra.Command{
		Use:   "registry",
		Short: "Store the credentials the server pulls private images with",
		Long: `Store the credentials the server pulls private images with.

An image in a private registry needs a credential on the server. "registry
login" hands one to the agent, which checks it against the registry, keeps it
encrypted like a secret and uses it for every image it pulls from there —
deployments, rollbacks, jobs. Nobody reads the password back: "registry ls"
shows registries and usernames.

Use a token that may only read: the server pulls, it never pushes. Logging in
and out needs admin; listing needs read.`,
	}
	registry.AddCommand(c.registryLoginCommand(), c.registryListCommand(), c.registryLogoutCommand())
	return []*cobra.Command{registry}
}

func (c *cli) registryLoginCommand() *cobra.Command {
	var (
		username      string
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Use:   "login <registry> --username <name>",
		Short: "Store a registry credential; the password is asked without echo, or piped in",
		Long: `Store the credential for a registry, creating or replacing it. The registry is
named as image references name it: ghcr.io, registry.example.com:5000,
docker.io for Docker Hub.

The password or token is never an argument — arguments leak through ps and
shell history. In a terminal it is asked for without echo; otherwise it is
read from standard input:

  shipwick registry login ghcr.io --username octocat
  printf '%s' "$GHCR_TOKEN" | shipwick registry login ghcr.io --username octocat

The agent checks the credential against the registry before it stores it, so
a mistyped token is refused here and not by the next deployment.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := api.NormalizeRegistry(args[0])
			if err != nil {
				return err
			}
			if username == "" {
				return fmt.Errorf("say who to log in as: shipwick registry login %s --username <name>", registry)
			}
			password, err := c.readHidden("Password or token for "+username+" at "+registry, "", passwordStdin, api.MaxRegistryPasswordBytes,
				"no password on standard input\n\nPipe it in: printf '%s' \"$TOKEN\" | shipwick registry login "+registry+" --username "+username)
			if err != nil {
				return err
			}
			if err := api.ValidateRegistryCredential(username, password); err != nil {
				return err
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.SetRegistry(cmd.Context(), registry, username, password); err != nil {
				if client.IsCode(err, api.CodeEndpointNotFound) {
					return errors.New("the agent is older than this shipwick and keeps no registry credentials\n\nCompare versions with: shipwick server status")
				}
				return err
			}
			c.ui.Success("Logged in to %s as %s", registry, username)
			c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  The server pulls images from %s with this credential from now on.", registry)))
			return nil
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "", "the account, or what the registry wants in its place for a token")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from standard input even in a terminal")
	return cmd
}

func (c *cli) registryListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the registries the server has a credential for; never passwords",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			registries, err := cl.Registries(cmd.Context())
			if err != nil {
				return err
			}
			if len(registries) == 0 {
				c.ui.Println("No registry credentials on the server. Store one with: shipwick registry login ghcr.io --username <name>")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(registries))
			for _, r := range registries {
				rows = append(rows, []ui.Cell{
					ui.C(r.Registry),
					ui.C(r.Username),
					ui.C(ui.RelativeTime(r.UpdatedAt, now)),
				})
			}
			c.ui.Table([]string{"REGISTRY", "USERNAME", "UPDATED"}, rows)
			return nil
		},
	}
}

func (c *cli) registryLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <registry>",
		Short: "Remove a registry credential; images already on the server stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := api.NormalizeRegistry(args[0])
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.DeleteRegistry(cmd.Context(), registry); err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("no credential is stored for %s\n\nList them with: shipwick registry ls", registry)
				}
				return err
			}
			c.ui.Success("Logged out of %s", registry)
			c.ui.Println(c.ui.Styled(ui.Dim, "  Running applications are not affected; the server pulls from there without this credential from now on."))
			return nil
		},
	}
}

// agentKeyVariable is the variable the agent's key is set with when it is
// not kept in the data directory.
const agentKeyVariable = "SHIPWICK_ENCRYPTION_KEY"

// serverRotateKeyCommand is `shipwick server rotate-key`.
func (c *cli) serverRotateKeyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key",
		Short: "Replace the key the server encrypts stored secrets with",
		Long: `Replace the key the server encrypts stored secrets with.

Environment values, secrets and registry passwords are encrypted in the
agent's database with one key. This command has the running agent generate a
new one and re-encrypt everything under it. Nothing is deployed and nothing
restarts.

Where the agent keeps its key in the data directory (encryption.key, the
default), it replaces the file. Where the key is set as ` + agentKeyVariable + `,
the agent cannot change its own environment: the new key is shown once, for
you to put there before the agent restarts.

Back the new key up. Backups of the database made before the rotation still
need the old key. Needs admin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			rotation, err := cl.RotateKey(cmd.Context())
			if err != nil {
				if client.IsCode(err, api.CodeEndpointNotFound) {
					return errors.New("the agent is older than this shipwick and cannot rotate its key\n\nCompare versions with: shipwick server status")
				}
				return err
			}

			c.ui.Success("Rotated the encryption key: %s and %s re-encrypted",
				countOf(rotation.Values, "stored value"), countOf(rotation.Deployments, "deployment"))
			if rotation.KeySource != api.KeySourceEnvironment {
				c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  The new key is in %s on the server. Back it up: database backups made from now on need it, earlier ones the old key.", rotation.KeyFile)))
				return nil
			}
			c.ui.Println()
			c.ui.Println("    " + c.ui.Styled(ui.Bold, rotation.Key))
			c.ui.Println()
			c.ui.Println("The agent's key is set in its environment, which it cannot change. Put the new")
			c.ui.Println("key in /opt/shipwick/.env on the server before the agent restarts:")
			c.ui.Println()
			c.ui.Println("    " + agentKeyVariable + "=" + rotation.Key)
			c.ui.Println()
			c.ui.Println("With the old key there the agent refuses to start. Until it has started with the")
			c.ui.Println("new one it keeps a copy in " + rotation.KeyFile + ", and removes it then.")
			return nil
		},
	}
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
