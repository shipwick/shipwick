package commands

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/ui"
)

// loadSaved reads the config file `shipwick login` writes.
func (c *cli) loadSaved() (string, cliconfig.Config, error) {
	path, err := cliconfig.Path(c.getenv)
	if err != nil {
		return "", cliconfig.Config{}, err
	}
	file, err := cliconfig.Load(path)
	return path, file, err
}

func (c *cli) contextCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "context",
		Short: "Switch between saved servers",
		Long: `Switch between saved servers.

Each "shipwick login" saves a server under a name — a context. Commands talk to
the current one; --context or SHIPWICK_CONTEXT picks another for one command.

  shipwick login --context staging --url https://staging.example.com
  shipwick context use prod
  shipwick deploy --context staging`,
	}
	root.AddCommand(c.contextLsCommand(), c.contextUseCommand(), c.contextRmCommand(), c.contextCurrentCommand())
	return root
}

func (c *cli) contextLsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the saved servers; * marks the current one",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, file, err := c.loadSaved()
			if err != nil {
				return err
			}
			if len(file.Contexts) == 0 {
				c.ui.Println("No saved servers.\n\nSave one with: shipwick login")
				return nil
			}
			var rows [][]ui.Cell
			for _, name := range file.Names() {
				mark := "  "
				if name == file.Current {
					mark = "* "
				}
				rows = append(rows, []ui.Cell{{Text: mark + name, Style: ui.Bold}, ui.C(file.Contexts[name].URL)})
			}
			c.ui.Table([]string{"  NAME", "URL"}, rows)
			return nil
		},
	}
}

func (c *cli) contextUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a saved server the current one",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, file, err := c.loadSaved()
			if err != nil {
				return err
			}
			name := args[0]
			ctx, ok := file.Contexts[name]
			if !ok {
				return unknownContext(name)
			}
			file.Current = name
			if err := cliconfig.Save(path, file); err != nil {
				return err
			}
			c.ui.Success("Switched to %s (%s)", name, ctx.URL)
			return nil
		},
	}
}

func (c *cli) contextRmCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Forget a saved server and its token",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, file, err := c.loadSaved()
			if err != nil {
				return err
			}
			name := args[0]
			ctx, ok := file.Contexts[name]
			if !ok {
				return unknownContext(name)
			}
			if !yes {
				if !isTerminal(c.in) {
					return fmt.Errorf("refusing to remove context %s without confirmation; pass --yes", name)
				}
				c.ui.Printf("Forget %s (%s) and its token? [y/N] ", name, ctx.URL)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}
			wasCurrent := file.Current == name
			file.Remove(name)
			if err := cliconfig.Save(path, file); err != nil {
				return err
			}
			c.ui.Success("Removed context %s", name)
			if wasCurrent && len(file.Contexts) > 0 {
				c.ui.Println(c.ui.Styled(ui.Dim, "  no server is current now; pick one with: shipwick context use <name>"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (c *cli) contextCurrentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the name of the current server",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, file, err := c.loadSaved()
			if err != nil {
				return err
			}
			switch {
			case len(file.Contexts) == 0:
				return errors.New("no saved servers\n\nSave one with: shipwick login")
			case file.Current == "":
				return errors.New("no server is current\n\nPick one with: shipwick context use <name>")
			}
			c.ui.Println(file.Current)
			return nil
		},
	}
}

func unknownContext(name string) error {
	return fmt.Errorf("%w %q\n\nSee the saved ones with: shipwick context ls", cliconfig.ErrUnknownContext, name)
}
