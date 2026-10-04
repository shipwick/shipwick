package commands

import (
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

func (c *cli) logsCommand() *cobra.Command {
	var file string
	var tail int
	var follow, timestamps bool
	var archive archiveFlags

	cmd := &cobra.Command{
		Use:   "logs [app]",
		Short: "Show the logs of an application",
		Long: `Show the logs of an application, merged across its replicas.

Without an argument, the application described by deploy.yaml is shown. With
--follow in a terminal, an application that has printed nothing yet is said
to be followed, so that an empty screen is not taken for a hang.

The agent keeps the last output of every container that ended: a replica that
crashed, was restarted or was replaced by a deployment, and the runs of jobs
and commands. --previous shows the last one that ended, which is where to look
after a crash; --list shows what is kept and --id one entry of it; --run the
output of a run. --search, --since, --until, --deployment and --replica show
lines of the kept output and of the running containers together, oldest
first, the newest -n of them.`,
		Example: `  shipwick logs my-api -f
  shipwick logs my-api --previous
  shipwick logs my-api --search "connection refused" --since 2h
  shipwick logs my-api --deployment 12
  shipwick logs my-api --list`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args, file)
			if err != nil {
				return err
			}
			if tail < 0 || tail > 5000 || (tail == 0 && !follow) {
				return fmt.Errorf("--tail must be between 1 and 5000 (0 is allowed with --follow)")
			}
			if archive.set() {
				if err := archive.validate(follow); err != nil {
					return err
				}
				// A search shows when each line was printed unless told not to:
				// what it finds is hours and containers apart.
				if archive.search != "" && !cmd.Flags().Changed("timestamps") {
					timestamps = true
				}
				return c.archivedLogs(cmd.Context(), name, archive, tail, cmd.Flags().Changed("tail"), timestamps)
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

			if c.ui.IsTerminal() {
				var stop func()
				print, stop = c.noteSilence(name, print)
				defer stop()
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
	archive.register(cmd)
	return cmd
}

// quietAfter is how long `logs -f` shows nothing before it says that it is
// waiting: longer than the agent takes to send the lines there are.
const quietAfter = 2 * time.Second

// noteSilence wraps print for a followed stream. An application that has
// printed nothing leaves the screen empty, which reads as a hang; when no
// line has arrived after quietAfter, the terminal is told once that the
// command is waiting. The returned stop calls that off.
func (c *cli) noteSilence(name string, print func(api.LogLine)) (wrapped func(api.LogLine), stop func()) {
	after := c.after
	if after == nil {
		after = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	var mu sync.Mutex
	seen := false
	stop = after(quietAfter, func() {
		mu.Lock()
		defer mu.Unlock()
		if !seen {
			c.ui.Note("Following %s; nothing printed yet. Ctrl-C stops.", name)
		}
	})
	return func(line api.LogLine) {
		mu.Lock()
		seen = true
		mu.Unlock()
		print(line)
	}, stop
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
