package commands

import (
	"bufio"
	"errors"
	"strings"

	"github.com/spf13/cobra"
)

func (c *cli) stopCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "stop [app]",
		Short: "Stop an application; it stays stopped until started or deployed again",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			c.ui.Progress("Stopping %s", name)
			if _, err := cl.Stop(cmd.Context(), name); err != nil {
				c.ui.Done()
				return err
			}
			c.ui.Success("Stopped %s", name)
			return nil
		},
	}
	fileFlag(cmd, &file)
	return cmd
}

func (c *cli) startCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "start [app]",
		Short: "Start a stopped application",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			app, err := cl.Start(cmd.Context(), name)
			if err != nil {
				return err
			}
			c.ui.Success("Started %s (%d/%d replicas running)", name, app.Replicas.Running, app.Replicas.Desired) // health is not known yet
			return nil
		},
	}
	fileFlag(cmd, &file)
	return cmd
}

func (c *cli) deleteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <app>",
		Short: "Remove an application, its containers and its deployment history",
		// The name is always explicit: deleting "whatever deploy.yaml says"
		// from the wrong directory is too easy a mistake.
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, "")
			if err != nil {
				return err
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to delete without confirmation; pass --yes")
				}
				c.ui.Printf("This removes %s, its containers and its deployment history from the server.\nType the application name to confirm: ", name)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if strings.TrimSpace(answer) != name {
					return errors.New("cancelled: the name did not match")
				}
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			c.ui.Progress("Deleting %s", name)
			if err := cl.Delete(cmd.Context(), name); err != nil {
				c.ui.Done()
				return err
			}
			c.ui.Success("Deleted %s", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
