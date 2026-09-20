package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// historyDepth is how far back `rollback` looks for its target.
const historyDepth = 500

func (c *cli) rollbackCommand() *cobra.Command {
	var file string
	var to int
	var noWait bool

	cmd := &cobra.Command{
		Use:   "rollback [app]",
		Short: "Go back to an earlier successful deployment",
		Long: `Go back to an earlier successful deployment: by default the one that was
active before the current one, or the one given with --to (the #number shown by
"shipwick status").

A rollback is an ordinary deployment of the configuration that was stored with
that earlier deployment — image, env, replicas, everything. It is rolled out
replica by replica, health-checked, and recorded as a new entry in the
history; nothing is rewritten.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			// Resolve the target here rather than leaving it to the agent, so
			// that what is announced is exactly what is requested.
			history, err := cl.Deployments(ctx, name, historyDepth)
			if err != nil {
				return err
			}
			target, err := rollbackTarget(history, to)
			if err != nil {
				return err
			}

			c.ui.Println("Rolling back " + c.ui.Styled(ui.Bold, name) + " to " + target.Version +
				c.ui.Styled(ui.Dim, fmt.Sprintf("  (deployment #%d)", target.Sequence)) + "...")
			c.ui.Println()

			started := c.now()
			d, err := cl.Rollback(ctx, name, target.ID)
			if err != nil {
				return err
			}
			return c.followDeployment(ctx, cl, d, started, noWait)
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().IntVar(&to, "to", 0, "deployment number to go back to (default: the previous successful one)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the rollback and return immediately")
	return cmd
}

// rollbackTarget picks the deployment to return to from the history (newest
// first). Only deployments that once served successfully qualify.
func rollbackTarget(history []api.Deployment, sequence int) (api.Deployment, error) {
	for _, d := range history {
		switch {
		case sequence == 0 && d.Status == api.StatusSuperseded:
			return d, nil
		case sequence != 0 && d.Sequence == sequence:
			switch d.Status {
			case api.StatusSuperseded:
				return d, nil
			case api.StatusActive:
				return api.Deployment{}, fmt.Errorf("deployment #%d is the one running right now", sequence)
			}
			return api.Deployment{}, fmt.Errorf("deployment #%d never ran successfully (%s), so there is nothing to go back to", sequence, d.Status)
		}
	}
	if sequence != 0 {
		return api.Deployment{}, fmt.Errorf("there is no deployment #%d\n\nSee the history with: shipwick status", sequence)
	}
	return api.Deployment{}, fmt.Errorf("there is no earlier successful deployment to go back to")
}

func (c *cli) redeployCommand() *cobra.Command {
	var file, image string
	var noWait bool

	cmd := &cobra.Command{
		Use:   "redeploy [app]",
		Short: "Deploy the running configuration again, optionally with another image",
		Long: `Deploy the running configuration again, optionally with another image.

Unlike "deploy", this needs no deploy.yaml: the agent re-uses the configuration
it stored with the active deployment, env values included. Useful to move an
application to a new image from anywhere, or to replace all its containers.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}

			what := "Redeploying " + c.ui.Styled(ui.Bold, name)
			if image != "" {
				what += " with " + image
			}
			c.ui.Println(what + "...")
			c.ui.Println()

			started := c.now()
			d, err := cl.Redeploy(cmd.Context(), name, image)
			if err != nil {
				return err
			}
			return c.followDeployment(cmd.Context(), cl, d, started, noWait)
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().StringVar(&image, "image", "", "image to deploy (default: the one already running)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the deployment and return immediately")
	return cmd
}
