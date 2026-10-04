package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

func (c *cli) configCommand() *cobra.Command {
	var output string
	var force bool
	cmd := &cobra.Command{
		Use:   "config <app>",
		Short: "Print the deploy.yaml of what an application runs",
		Long: `Print the deploy.yaml that describes what an application runs: the way to
get the file back when it was lost, or was never on this machine.

  shipwick config my-api -o deploy.yaml

The server does not hand out secret values. A value that was written as a
reference to a secret stored on the server is that reference again, such as
postgres://app:${DB_PASSWORD}@db:5432/app, and is filled in again when the file
is deployed. A value that stood in the deployed file as it is, such as
LOG_LEVEL: info, is that value again: "shipwick deploy" tells the server which
ones did. A value that "shipwick deploy" filled in from the environment or
--env-file is shown as "********": write it again, or store it with
"shipwick secret set NAME" and refer to it as ${NAME}. Until then
"shipwick deploy" refuses the file and names what is missing. A value typed
in the dashboard's editor is shown so too, and so is every value of an
application last deployed by a shipwick or a server before 0.8.

It takes a token that may deploy the application.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && !force {
				if _, err := os.Stat(output); err == nil {
					return fmt.Errorf("%s already exists\n\nOverwrite it with: shipwick config %s -o %s --force", output, args[0], output)
				}
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			config, err := cl.Config(cmd.Context(), args[0])
			if client.IsCode(err, api.CodeEndpointNotFound) {
				return errors.New("the agent is older than this shipwick and does not write an application's deploy.yaml\n\nCompare versions with: shipwick server status")
			} else if err != nil {
				return err
			}
			if output == "" {
				c.ui.Printf("%s", config.Document)
			} else {
				if err := os.WriteFile(output, []byte(config.Document), 0o644); err != nil {
					return err
				}
				c.ui.Success("Wrote %s: %s as deployment #%d (%s) runs it", output, config.Application, config.Sequence, config.Version)
			}
			if n := len(config.Masked); n > 0 {
				c.ui.Warn("%s %s not handed out and %s \"********\" in the file: %s", plural(n, "value"), pluralIs(n), standOrStands(n), strings.Join(config.Masked, ", "))
				c.ui.Note("  Write %s again, or store %s with shipwick secret set NAME and refer to it as ${NAME}.", pluralIt(n), itOrEach(n))
				c.ui.Note("  Until then shipwick deploy refuses the file.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the file here instead of printing it")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

func itOrEach(n int) string {
	if n == 1 {
		return "it"
	}
	return "each"
}

func standOrStands(n int) string {
	if n == 1 {
		return "stands as"
	}
	return "stand as"
}
