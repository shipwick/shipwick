package commands

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
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

A secret belongs to one application (--app) or to the whole server. An
application's ${NAME} is answered by its own secret first and by the server's
otherwise, so two applications can each have a DATABASE_PASSWORD, and one
REGISTRY_TOKEN can serve them all. Store what an application owns with --app.

Secrets are encrypted at rest like env values are. Nobody reads them back:
"secret ls" shows names, scopes and dates. A changed secret takes effect on the
next deployment; running containers keep the value they were started with, and
a rollback restores the value that deployment used. Setting and removing
secrets needs admin; listing them needs read.`,
	}
	secret.AddCommand(c.secretSetCommand(), c.secretListCommand(), c.secretRemoveCommand())
	return []*cobra.Command{secret}
}

func (c *cli) secretSetCommand() *cobra.Command {
	var fromFile, application string
	cmd := &cobra.Command{
		Use:   "set <NAME>",
		Short: "Store a secret; the value is asked without echo, or piped in",
		Long: `Store a secret, creating or replacing it. The value is never an argument —
arguments leak through ps and shell history. In a terminal it is asked for
without echo; otherwise it is read from standard input, or from a file:

  shipwick secret set DATABASE_PASSWORD --app api
  printf '%s' "$DATABASE_PASSWORD" | shipwick secret set DATABASE_PASSWORD --app api
  shipwick secret set TLS_KEY --from-file key.pem

With --app the secret is the application's own: only its ${NAME} resolves to
it, before a server-wide secret of the same name. Without --app it is the
server's, which every application without one of its own falls back to. The
application need not exist yet.

One trailing newline is dropped, so that echo works too; nothing else is
trimmed. Values are at most 64 KB. A value with characters a URL cannot carry
unescaped (@ : / ? # % and whitespace) is stored with a warning: pasted into a
connection string, it breaks it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := api.ValidateSecretName(name); err != nil {
				return err
			}
			if application != "" {
				if err := spec.ValidateName(application); err != nil {
					return err
				}
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
			if err := cl.SetSecret(cmd.Context(), name, value, application); err != nil {
				if application != "" && refusedScopedSecret(err) {
					return errors.New("the agent is older than this shipwick and keeps no secrets per application; nothing was stored\n\nUpgrade the server (curl -fsSL https://get.shipwick.com | sh), or store the secret for every application without --app. Compare versions with: shipwick server status")
				}
				return err
			}
			if application == "" {
				c.ui.Success("Stored secret %s", name)
				c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  Use it as ${%s} in an env value or a proxy.basic_auth password of deploy.yaml; it applies from the next deployment on.", name)))
			} else {
				c.ui.Success("Stored secret %s for %s", name, application)
				c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  ${%s} in %s's deploy.yaml resolves to it from the next deployment on, before a server-wide %s; other applications do not see it.", name, application, name)))
			}
			if unsafe := urlUnsafe(value); unsafe != "" {
				c.ui.Warn("this value has characters a URL cannot carry unescaped (%s); if it goes into a connection string, use another", unsafe)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read the value from this file instead of standard input")
	cmd.Flags().StringVar(&application, "app", "", "store the secret for this application only; its ${NAME} resolves to it before a server-wide one")
	return cmd
}

// refusedScopedSecret reports whether the agent refused a secret set with
// --app because it does not know the field: an agent before 0.9 rejects a body
// with a key it has not heard of, and stores nothing. That it refuses, rather
// than ignores, is why the application travels in the body.
func refusedScopedSecret(err error) bool {
	return client.IsCode(err, api.CodeInvalidRequest) && strings.Contains(err.Error(), "invalid JSON body")
}

// urlUnsafe names the characters of value that a URL cannot carry unescaped
// — what breaks a connection string when a password is written into it — in a
// fixed order, and never the value itself.
func urlUnsafe(value string) string {
	var found []string
	for _, r := range "@:/?#%" {
		if strings.ContainsRune(value, r) {
			found = append(found, string(r))
		}
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		found = append(found, "whitespace")
	}
	return strings.Join(found, ", ")
}

// readSecretValue reads the value from the file given, from a terminal
// without echo, or from whatever standard input is. It never returns the
// value in an error.
func (c *cli) readSecretValue(name, fromFile string) (string, error) {
	return c.readHidden("Value for "+name, fromFile, false, api.MaxSecretValueBytes,
		"no value on standard input\n\nPipe it in: printf '%s' \"$VALUE\" | shipwick secret set "+name+"\nor give a file with --from-file")
}

// readHidden is how every value that must not be seen is read: from the file
// given, from a terminal without echo under the prompt, or from whatever
// standard input is — and from standard input in any case when the caller was
// asked to. noInput is the error for an empty pipe.
func (c *cli) readHidden(prompt, fromFile string, fromStdin bool, limit int64, noInput string) (string, error) {
	var raw []byte
	var err error
	switch {
	case fromFile != "":
		raw, err = os.ReadFile(fromFile)
	case isTerminal(c.in) && !fromStdin:
		c.ui.Printf("%s: ", prompt)
		raw, err = term.ReadPassword(int(c.in.(*os.File).Fd()))
		c.ui.Println()
	default:
		raw, err = io.ReadAll(io.LimitReader(c.in, limit+2))
		if err == nil && len(raw) == 0 {
			return "", errors.New(noInput)
		}
	}
	if err != nil {
		return "", fmt.Errorf("read the value: %w", err)
	}
	raw = bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte("\n")), []byte("\r"))
	return string(raw), nil
}

// secretScope is how a listing names where a secret belongs.
func secretScope(application string) string {
	if application == "" {
		return "server"
	}
	return application
}

func (c *cli) secretListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the secrets on the server: names, scopes and dates, never values",
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
				c.ui.Println("No secrets on the server. Store one with: shipwick secret set DATABASE_PASSWORD --app <application>")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(secrets))
			for _, s := range secrets {
				rows = append(rows, []ui.Cell{
					ui.C(s.Name),
					ui.C(secretScope(s.Application)),
					ui.C(ui.RelativeTime(s.CreatedAt, now)),
					ui.C(ui.RelativeTime(s.UpdatedAt, now)),
				})
			}
			c.ui.Table([]string{"NAME", "SCOPE", "CREATED", "UPDATED"}, rows)
			return nil
		},
	}
}

func (c *cli) secretRemoveCommand() *cobra.Command {
	var (
		yes         bool
		application string
	)
	cmd := &cobra.Command{
		Use:     "rm <NAME>",
		Aliases: []string{"remove"},
		Short:   "Remove a secret; the next deployment that refers to it is refused",
		Long: `Remove a secret. Without --app the server-wide secret goes, and an
application with a secret of its own by that name keeps it; with --app the
application's own goes, and the server-wide one, if there is one, answers
its ${NAME} from the next deployment on. Deployments already made keep the
value they were started with.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := api.ValidateSecretName(name); err != nil {
				return err
			}
			if application != "" {
				if err := spec.ValidateName(application); err != nil {
					return err
				}
			}
			described := name
			if application != "" {
				described = name + " of " + application
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to remove without confirmation; pass --yes")
				}
				c.ui.Printf("Remove secret %s? Deployments already made keep their value. [y/N] ", described)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			// What is there, before anything goes: whether the secret meant
			// exists, and who keeps one of their own. The listing is also
			// what tells an agent before 0.9 apart, which would take a
			// DELETE with --app for the server-wide secret.
			secrets, err := cl.Secrets(cmd.Context())
			if err != nil {
				return err
			}
			var (
				serverWide, anyScoped bool
				own                   []string // applications with a secret of their own by this name
			)
			for _, s := range secrets {
				if s.Application != "" {
					anyScoped = true
				}
				if s.Name != name {
					continue
				}
				if s.Application == "" {
					serverWide = true
				} else {
					own = append(own, s.Application)
				}
			}
			switch {
			case application != "" && !slices.Contains(own, application):
				msg := fmt.Sprintf("there is no secret %s for application %s", name, application)
				if serverWide {
					msg += fmt.Sprintf("\n\nThe server-wide %s is removed without --app: shipwick secret rm %s", name, name)
				} else {
					msg += "\n\nList them with: shipwick secret ls"
				}
				if !anyScoped {
					msg += "\nSecrets of one application need an agent of 0.9 or newer; compare versions with: shipwick server status"
				}
				return errors.New(msg)
			case application == "" && !serverWide && len(own) > 0:
				return fmt.Errorf("there is no server-wide secret %s, only the own of %s\n\nRemove one with: shipwick secret rm %s --app %s",
					name, joinFiles(own), name, own[0])
			case application == "" && !serverWide:
				return fmt.Errorf("there is no secret named %s\n\nList them with: shipwick secret ls", name)
			}

			if err := cl.DeleteSecret(cmd.Context(), name, application); err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("there is no secret named %s\n\nList them with: shipwick secret ls", described)
				}
				return err
			}
			c.ui.Success("Removed secret %s", described)
			if application == "" && len(own) > 0 {
				c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  The %s of %s stays: ${%s} is unchanged there.", name, joinFiles(own), name)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().StringVar(&application, "app", "", "remove the secret this application has of its own; the server-wide one stays")
	return cmd
}
