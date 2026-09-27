package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// ExitError carries the exit code of a command that ran on the server, so
// that `shipwick run` can exit with it. It counts as reported: the output and
// the code were already shown.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
func (e *ExitError) Unwrap() error { return ErrReported }

// ExitCode is the process exit code for an error from the command tree: the
// remote command's own for an ExitError, 1 for anything else.
func ExitCode(err error) int {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return 1
}

// jobCommands returns the run, jobs commands.
func (c *cli) jobCommands() []*cobra.Command {
	return []*cobra.Command{c.runCommand(), c.jobsCommand()}
}

func (c *cli) runCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "run [app] -- <command> [args...]",
		Short: "Run a one-off command in a container of the application",
		Long: `Run a one-off command in a fresh container from the application's image, with
its environment, limits and network: a migration, a console script, a shell
one-liner. The command runs on the server; you get its output and exit code.

  shipwick run my-api -- rails db:migrate
  shipwick run -- python manage.py createsuperuser

The command comes after "--". Without an application, the one described by
deploy.yaml is used. The container gets none of the application's volumes:
those belong to the running replica. A command is stopped after an hour.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash == len(args) {
				return errors.New("no command given\n\nPut it after --, e.g.: shipwick run my-api -- rails db:migrate")
			}
			if dash > 1 {
				return fmt.Errorf("expected one application before --, got %d: %s", dash, strings.Join(args[:dash], " "))
			}
			name, err := c.resolveApp(args[:dash], file)
			if err != nil {
				return err
			}
			command := args[dash:]
			if err := spec.ValidateCommand(command); err != nil {
				return fmt.Errorf("command %v", err)
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			run, err := cl.RunCommand(cmd.Context(), name, command)
			if err != nil {
				return err
			}
			return c.followRun(cmd.Context(), cl, run)
		},
	}
	fileFlag(cmd, &file)
	return cmd
}

func (c *cli) jobsCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "jobs [app]",
		Short: "List the scheduled jobs of an application",
		Long: `List the scheduled jobs of an application: schedule, last run, next run.
Schedules and times are in UTC.

Without an argument, the application described by deploy.yaml is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args, file)
			if err != nil {
				return err
			}
			return c.listJobs(cmd.Context(), name)
		},
	}
	fileFlag(cmd, &file)
	cmd.AddCommand(c.jobsRunCommand(), c.jobsLogsCommand())
	return cmd
}

func (c *cli) jobsRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "run <app> <job>",
		Short: "Start a scheduled job now and wait for it",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			if err := spec.ValidateJobName(args[1]); err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			run, err := cl.RunJob(cmd.Context(), name, args[1])
			if err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("%s has no job named %q, or the server does not know the application\n\nSee its jobs with: shipwick jobs %s", name, args[1], name)
				}
				return err
			}
			return c.followRun(cmd.Context(), cl, run)
		},
	}
}

func (c *cli) jobsLogsCommand() *cobra.Command {
	var runID int64
	cmd := &cobra.Command{
		Use:   "logs <app> <job>",
		Short: "Show the output of a job's last run",
		Long: `Show the output of a job's last run, or of the run given with --run.

"pre-deploy" shows the last pre-deploy command, "run" the last one-off command.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			job := args[1]
			if err := spec.ValidateJobName(job); err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if runID == 0 {
				runs, err := cl.Runs(ctx, name, job, 1)
				if err != nil {
					return err
				}
				if len(runs) == 0 {
					c.ui.Println(fmt.Sprintf("%s has no runs of %s yet.", name, job))
					return nil
				}
				runID = runs[0].ID
			}
			run, err := cl.Run(ctx, name, runID)
			if err != nil {
				return err
			}
			c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Run #%d of %s: %s, started %s", run.ID, run.Job, describeRun(run.Run), ui.RelativeTime(run.StartedAt, c.now()))))
			if run.Output != "" {
				c.ui.Println(run.Output)
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&runID, "run", 0, "show this run instead of the last one (its id, from the API)")
	return cmd
}

func (c *cli) listJobs(ctx context.Context, name string) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	jobs, err := cl.Jobs(ctx, name)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		c.ui.Println(fmt.Sprintf("%s has no scheduled jobs. Add some under `jobs` in deploy.yaml.", name))
		return nil
	}
	now := c.now()
	rows := make([][]ui.Cell, 0, len(jobs))
	for _, j := range jobs {
		last, status := ui.C("never"), ui.C("")
		if r := j.LastRun; r != nil {
			last = ui.C(ui.RelativeTime(r.StartedAt, now))
			status = ui.Cell{Text: describeRun(*r), Style: runStyle(r.Status)}
		}
		next := "-"
		if j.NextRunAt != nil {
			next = j.NextRunAt.UTC().Format("2006-01-02 15:04")
		}
		rows = append(rows, []ui.Cell{ui.C(j.Name), ui.C(j.Schedule), last, status, ui.C(next)})
	}
	c.ui.Table([]string{"NAME", "SCHEDULE", "LAST RUN", "STATUS", "NEXT (UTC)"}, rows)
	return nil
}

// followRun waits for a run that was just started, prints what it wrote, and
// ends the process the way the command ended: with its exit code.
func (c *cli) followRun(ctx context.Context, cl *client.Client, run api.RunDetail) error {
	c.ui.Progress("Running %s", strings.Join(run.Command, " "))
	final, err := c.awaitRun(ctx, cl, run.Application, run.ID)
	c.ui.Done()
	if err != nil {
		if ctx.Err() != nil {
			c.ui.Println()
			c.ui.Println("Stopped waiting. The command continues on the server; see its output later with:")
			c.ui.Println(fmt.Sprintf("  shipwick jobs logs %s %s --run %d", run.Application, run.Job, run.ID))
			return ErrReported
		}
		return err
	}

	if final.Output != "" {
		c.ui.Println(final.Output)
	}
	switch final.Status {
	case api.RunSucceeded:
		return nil
	case api.RunFailed:
		if final.ExitCode == nil {
			c.ui.Failure("The command could not be started")
			return ErrReported
		}
		c.ui.Failure("%s", describeRun(final.Run))
		return &ExitError{Code: *final.ExitCode}
	default:
		c.ui.Failure("%s", describeRun(final.Run))
		return ErrReported
	}
}

// awaitRun polls a run until it has finished, riding out connectivity blips
// the way awaitDeployment does.
func (c *cli) awaitRun(ctx context.Context, cl *client.Client, name string, id int64) (api.RunDetail, error) {
	failures := 0
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		run, err := cl.Run(ctx, name, id)
		switch {
		case err == nil && run.FinishedAt != nil:
			return run, nil
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			return api.RunDetail{}, ctx.Err()
		default:
			var unreachable *client.UnreachableError
			if failures++; !errors.As(err, &unreachable) || failures >= maxPollFailures {
				return api.RunDetail{}, err
			}
			c.ui.Progress("Waiting for the agent to respond")
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return api.RunDetail{}, ctx.Err()
		}
	}
}

// describeRun says how a run stands, in a few words: "failed (exit 1)".
func describeRun(r api.Run) string {
	switch r.Status {
	case api.RunFailed:
		if r.ExitCode != nil {
			return fmt.Sprintf("failed (exit %d)", *r.ExitCode)
		}
		return "failed to start"
	case api.RunTimedOut:
		return "timed out"
	case api.RunInterrupted:
		return "interrupted by an agent restart"
	}
	return string(r.Status)
}

func runStyle(s api.RunStatus) ui.Style {
	switch s {
	case api.RunSucceeded:
		return ui.Green
	case api.RunFailed, api.RunTimedOut:
		return ui.Red
	case api.RunRunning:
		return ui.Yellow
	}
	return ui.Dim
}
