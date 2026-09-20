package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

func (c *cli) logsCommand() *cobra.Command {
	var file string
	var tail int
	var follow, timestamps bool

	cmd := &cobra.Command{
		Use:   "logs [app]",
		Short: "Show the logs of an application",
		Long: `Show the logs of an application, merged across its replicas.

Without an argument, the application described by deploy.yaml is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			if tail < 0 || tail > 5000 || (tail == 0 && !follow) {
				return fmt.Errorf("--tail must be between 1 and 5000 (0 is allowed with --follow)")
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}

			// Replica prefixes are noise for a single replica.
			app, err := cl.Application(cmd.Context(), name)
			if err != nil {
				return err
			}
			print := c.logPrinter(app.Replicas.Desired > 1, timestamps)

			if !follow {
				lines, err := cl.Logs(cmd.Context(), name, tail)
				if err != nil {
					return err
				}
				for _, line := range lines {
					print(line)
				}
				return nil
			}

			err = cl.FollowLogs(cmd.Context(), name, tail, print)
			if err != nil || cmd.Context().Err() != nil {
				return err // Ctrl+C is the normal way out: no message
			}
			c.ui.Warn("log stream ended: the containers were stopped or replaced. Run the command again to follow the new ones.")
			return nil
		},
	}
	// Here -f means --follow, as in docker and kubectl; the config file flag
	// is long-form only.
	cmd.Flags().StringVar(&file, "file", DefaultFile, "path to the deployment config")
	cmd.Flags().IntVarP(&tail, "tail", "n", 100, "number of lines to show from the end of the logs")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new log lines")
	cmd.Flags().BoolVarP(&timestamps, "timestamps", "t", false, "prefix each line with its timestamp")
	return cmd
}

// replicaStyles tells replicas apart at a glance.
var replicaStyles = []ui.Style{ui.Cyan, ui.Yellow, ui.Green, ui.Red}

func (c *cli) logPrinter(showReplica, timestamps bool) func(api.LogLine) {
	return func(line api.LogLine) {
		prefix := ""
		if timestamps && !line.Time.IsZero() {
			prefix += c.ui.Styled(ui.Dim, line.Time.Local().Format("2006-01-02 15:04:05.000")) + " "
		}
		if showReplica {
			style := replicaStyles[(max(line.Replica, 1)-1)%len(replicaStyles)]
			prefix += c.ui.Styled(style, fmt.Sprintf("[%d]", line.Replica)) + " "
		}
		c.ui.Println(prefix + line.Message)
	}
}
