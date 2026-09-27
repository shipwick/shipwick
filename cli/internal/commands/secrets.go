package commands

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// secretCommands returns the secret commands.
func (c *cli) secretCommands() []*cobra.Command {
	secret := &cobra.Command{
		Use:   "secret",
		Short: "Store values on the server for ${NAME} in deploy.yaml",
		Long: `Store values on the server for ${NAME} in deploy.yaml.

A secret is a value that must not be in deploy.yaml — a password, an API key.
Written as ${DATABASE_PASSWORD} in an env value, it is filled in when you
deploy: from your environment or --env-file if the name is set there, else by
the server from the secrets stored here. With the secrets on the server, no
laptop and no pipeline needs an env file.

Secrets are encrypted at rest like env values are. Nobody reads them back:
"secret ls" shows names and dates. A changed secret takes effect on the next
deployment; running containers keep the value they were started with, and a
rollback restores the value that deployment used. Setting and removing
secrets needs admin; listing them needs read.`,
	}
	secret.AddCommand(c.secretSetCommand(), c.secretListCommand(), c.secretRemoveCommand())
	return []*cobra.Command{secret}
}

func (c *cli) secretSetCommand() *cobra.Command {
	var fromFile string
	cmd := &cobra.Command{
		Use:   "set <NAME>",
		Short: "Store a secret; the value is asked without echo, or piped in",
		Long: `Store a secret, creating or replacing it. The value is never an argument —
arguments leak through ps and shell history. In a terminal it is asked for
without echo; otherwise it is read from standard input, or from a file:

  shipwick secret set DATABASE_PASSWORD
  printf '%s' "$DATABASE_PASSWORD" | shipwick secret set DATABASE_PASSWORD
  shipwick secret set TLS_KEY --from-file key.pem

One trailing newline is dropped, so that echo works too; nothing else is
trimmed. Values are at most 64 KB.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := api.ValidateSecretName(name); err != nil {
				return err
			}
			value, err := c.readSecretValue(name, fromFile)
			if err != nil {
				return err
			}
			if err := api.ValidateSecretValue(value); err != nil {
				return err
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.SetSecret(cmd.Context(), name, value); err != nil {
				return err
			}
			c.ui.Success("Stored secret %s", name)
			c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  Use it as ${%s} in an env value of deploy.yaml; it applies from the next deployment on.", name)))
			return nil
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read the value from this file instead of standard input")
	return cmd
}

// readSecretValue reads the value from the file given, from a terminal
// without echo, or from whatever standard input is. It never returns the
// value in an error.
func (c *cli) readSecretValue(name, fromFile string) (string, error) {
	var raw []byte
	var err error
	switch {
	case fromFile != "":
		raw, err = os.ReadFile(fromFile)
	case isTerminal(c.in):
		c.ui.Printf("Value for %s: ", name)
		raw, err = term.ReadPassword(int(c.in.(*os.File).Fd()))
		c.ui.Println()
	default:
		raw, err = io.ReadAll(io.LimitReader(c.in, api.MaxSecretValueBytes+2))
		if err == nil && len(raw) == 0 {
			return "", errors.New("no value on standard input\n\nPipe it in: printf '%s' \"$VALUE\" | shipwick secret set " + name + "\nor give a file with --from-file")
		}
	}
	if err != nil {
		return "", fmt.Errorf("read the value: %w", err)
	}
	raw = bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte("\n")), []byte("\r"))
	return string(raw), nil
}

func (c *cli) secretListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the secrets on the server: names and dates, never values",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			secrets, err := cl.Secrets(cmd.Context())
			if err != nil {
				return err
			}
			if len(secrets) == 0 {
				c.ui.Println("No secrets on the server. Store one with: shipwick secret set DATABASE_PASSWORD")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(secrets))
			for _, s := range secrets {
				rows = append(rows, []ui.Cell{
					ui.C(s.Name),
					ui.C(ui.RelativeTime(s.CreatedAt, now)),
					ui.C(ui.RelativeTime(s.UpdatedAt, now)),
				})
			}
			c.ui.Table([]string{"NAME", "CREATED", "UPDATED"}, rows)
			return nil
		},
	}
}

func (c *cli) secretRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <NAME>",
		Aliases: []string{"remove"},
		Short:   "Remove a secret; the next deployment that refers to it is refused",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := api.ValidateSecretName(name); err != nil {
				return err
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to remove without confirmation; pass --yes")
				}
				c.ui.Printf("Remove secret %s? Deployments already made keep their value. [y/N] ", name)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.DeleteSecret(cmd.Context(), name); err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("there is no secret named %s\n\nList them with: shipwick secret ls", name)
				}
				return err
			}
			c.ui.Success("Removed secret %s", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
