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

// archiveFlags are the flags of `shipwick logs` that reach past the running
// containers, into what the agent kept of the ones that ended.
type archiveFlags struct {
	previous   bool
	list       bool
	id         int64
	deployment int
	replica    int
	run        int64
	search     string
	since      string
	until      string
}

func (a *archiveFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&a.previous, "previous", "p", false, "show the output of the last container that ended: the one that crashed, or was replaced")
	cmd.Flags().BoolVar(&a.list, "list", false, "list what is kept of ended containers and runs")
	cmd.Flags().Int64Var(&a.id, "id", 0, "show one entry of --list")
	cmd.Flags().IntVar(&a.deployment, "deployment", 0, "only the output of this deployment (its #number in `shipwick status`), kept or current")
	cmd.Flags().IntVar(&a.replica, "replica", 0, "only the output of this replica")
	cmd.Flags().Int64Var(&a.run, "run", 0, "show the output of this run of a job or a command (its id in `shipwick jobs`)")
	cmd.Flags().StringVar(&a.search, "search", "", "show the lines that contain this text, whatever its case, kept or current")
	cmd.Flags().StringVar(&a.since, "since", "", "only lines from then on, kept or current: 30m, 2h, 7d, a date or a time in RFC 3339")
	cmd.Flags().StringVar(&a.until, "until", "", "only lines up to then; takes what --since takes")
}

// set reports whether any of the flags was given: the command then reads the
// archive instead of the tail of the running containers.
func (a archiveFlags) set() bool {
	return a != archiveFlags{}
}

// queries reports whether the flags ask for lines across containers rather
// than for one entry.
func (a archiveFlags) queries() bool {
	return a.search != "" || a.since != "" || a.until != "" || a.deployment != 0 || a.replica != 0
}

func (a archiveFlags) validate(follow bool) error {
	switch {
	case follow:
		return errors.New("--follow streams the running containers; it does not go with --previous, --list, --id, --deployment, --replica, --run, --search, --since or --until")
	case a.id < 0 || a.run < 0 || a.deployment < 0 || a.replica < 0:
		return errors.New("--id, --deployment, --replica and --run take a positive number")
	case a.id != 0 && a != (archiveFlags{id: a.id}):
		return errors.New("--id shows one entry; it takes no other filter")
	case a.list && (a.previous || a.search != "" || a.since != "" || a.until != ""):
		return errors.New("--list lists entries; narrow it with --deployment, --replica or --run")
	case a.previous && (a.search != "" || a.since != "" || a.until != "" || a.run != 0):
		return errors.New("--previous shows the last ended container; narrow it with --deployment or --replica, or search with --search alone")
	case strings.ContainsAny(a.search, "\r\n"):
		return errors.New("--search takes one line: it looks inside lines")
	case len(a.search) > 256:
		return errors.New("--search takes at most 256 bytes")
	}
	return nil
}

// noArchive is what a command that needs the archive says to an agent that
// has none.
func noArchive(err error) error {
	if client.IsCode(err, api.CodeEndpointNotFound) {
		return errors.New("the agent is older than this shipwick and keeps no log archive: it shows the output of running containers only\n\nCompare versions with: shipwick server status")
	}
	return err
}

// archivedLogs is `shipwick logs` with any of the archive's flags.
func (c *cli) archivedLogs(ctx context.Context, name string, a archiveFlags, tail int, tailSet, timestamps bool) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	source := client.LogSource{Replica: a.replica, Run: a.run}
	if a.deployment != 0 {
		if source.Deployment, err = c.deploymentNumbered(ctx, cl, name, a.deployment); err != nil {
			return err
		}
	}
	// -n keeps its default for a search and a listing; of one entry
	// everything is shown unless it was given.
	entryTail := 0
	if tailSet {
		entryTail = tail
	}

	switch {
	case a.list:
		return c.listArchive(ctx, cl, name, source, tail)
	case a.id != 0:
		return c.showEntry(ctx, cl, name, a.id, entryTail, timestamps)
	case a.previous:
		entries, err := cl.LogArchive(ctx, name, api.LogKindReplica, source, 1)
		if err != nil {
			return noArchive(err)
		}
		if len(entries) == 0 {
			c.ui.Println(fmt.Sprintf("Nothing is kept of an earlier container of %s: none has ended since the agent began to keep their output, or what was kept has aged out.", name))
			return nil
		}
		return c.showEntry(ctx, cl, name, entries[0].ID, entryTail, timestamps)
	case a.run != 0 && !a.queries():
		return c.showRun(ctx, cl, name, a.run, entryTail, timestamps)
	}

	q := client.LogSearch{Text: a.search, Source: source}
	if a.since != "" {
		if q.Since, err = parseLogTime(a.since, c.now()); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}
	if a.until != "" {
		if q.Until, err = parseLogTime(a.until, c.now()); err != nil {
			return fmt.Errorf("--until: %w", err)
		}
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && q.Until.Before(q.Since) {
		return errors.New("--until is before --since")
	}
	return c.queryLogs(ctx, cl, name, q, tail, timestamps)
}

// parseLogTime reads --since and --until: how long ago ("30m", "2h", "7d"),
// a date, or a time in RFC 3339.
func parseLogTime(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d).UTC(), nil
	}
	return api.ParseSince(s, now)
}

// deploymentNumbered turns the #number of `shipwick status` into the
// deployment's id, which is what the API takes.
func (c *cli) deploymentNumbered(ctx context.Context, cl *client.Client, name string, sequence int) (int64, error) {
	history, err := cl.Deployments(ctx, name, 500)
	if err != nil {
		return 0, err
	}
	for _, d := range history {
		if d.Sequence == sequence {
			return d.ID, nil
		}
	}
	return 0, fmt.Errorf("%s has no deployment #%d\n\nSee its history with: shipwick status %s", name, sequence, name)
}

func (c *cli) listArchive(ctx context.Context, cl *client.Client, name string, source client.LogSource, limit int) error {
	entries, err := cl.LogArchive(ctx, name, "", source, min(limit, 500))
	if err != nil {
		return noArchive(err)
	}
	if len(entries) == 0 {
		c.ui.Println(fmt.Sprintf("Nothing is kept for %s. Output is archived when a container ends: a crash, a restart, a new deployment, a finished job.", name))
		return nil
	}
	now := c.now()
	rows := make([][]ui.Cell, 0, len(entries))
	for _, e := range entries {
		lines := fmt.Sprint(e.Lines)
		if e.Truncated {
			lines = "last " + lines
		}
		rows = append(rows, []ui.Cell{
			ui.C(fmt.Sprint(e.ID)),
			ui.C(ui.RelativeTime(e.EndedAt, now)),
			ui.C(entrySource(e)),
			{Text: describeEnd(e), Style: endStyle(e)},
			ui.C(lines),
			ui.C(spec.FormatMemory(e.Bytes)),
		})
	}
	c.ui.Table([]string{"ID", "ENDED", "OUTPUT OF", "ENDED BECAUSE", "LINES", "SIZE"}, rows)
	c.ui.Println()
	c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Read one with: shipwick logs %s --id <ID>", name)))
	return nil
}

// entrySource names the container an entry is the output of.
func entrySource(e api.LogArchiveEntry) string {
	if e.Kind == api.LogKindRun {
		id := int64(0)
		if e.RunID != nil {
			id = *e.RunID
		}
		return fmt.Sprintf("run %d of %s", id, e.Job)
	}
	if e.Deployment == 0 {
		return fmt.Sprintf("replica %d", e.Replica)
	}
	return fmt.Sprintf("#%d replica %d", e.Deployment, e.Replica)
}

// describeEnd says why the container of an entry ended.
func describeEnd(e api.LogArchiveEntry) string {
	exit := ""
	if e.ExitCode != nil {
		exit = fmt.Sprintf(" (exit %d)", *e.ExitCode)
	}
	if e.Kind == api.LogKindRun {
		return describeRun(api.Run{Status: api.RunStatus(e.Reason), ExitCode: e.ExitCode})
	}
	switch e.Reason {
	case api.LogReasonCrashed:
		return "crashed" + exit
	case api.LogReasonExited:
		return "exited" + exit
	case api.LogReasonOOMKilled:
		return "out of memory"
	case api.LogReasonUnhealthy:
		return "restarted: unhealthy"
	case api.LogReasonStopped:
		return "stopped"
	case api.LogReasonReplaced:
		return "replaced"
	case api.LogReasonDeploymentFailed:
		switch {
		case e.OOMKilled:
			return "deployment failed: out of memory"
		case e.ExitCode != nil && *e.ExitCode != 0:
			return "deployment failed" + exit
		}
		return "deployment failed"
	case api.LogReasonRemoved:
		return "removed"
	case api.LogReasonRestarted:
		return "ended unseen"
	}
	return e.Reason
}

func endStyle(e api.LogArchiveEntry) ui.Style {
	switch e.Reason {
	case api.LogReasonCrashed, api.LogReasonOOMKilled, api.LogReasonUnhealthy, api.LogReasonDeploymentFailed,
		string(api.RunFailed), string(api.RunTimedOut):
		return ui.Red
	case api.LogReasonReplaced, api.LogReasonStopped, string(api.RunSucceeded):
		return ui.Dim
	}
	return ui.Plain
}

// showEntry prints one entry: a line saying what it is the output of and how
// its container ended, then the lines.
func (c *cli) showEntry(ctx context.Context, cl *client.Client, name string, id int64, tail int, timestamps bool) error {
	detail, err := cl.ArchivedLogs(ctx, name, id, tail)
	if client.IsCode(err, api.CodeNotFound) {
		if _, appErr := cl.Application(ctx, name); appErr != nil {
			return appErr
		}
		return fmt.Errorf("%s has no archived output with id %d: it may have aged out\n\nSee what is kept with: shipwick logs %s --list", name, id, name)
	} else if err != nil {
		return noArchive(err)
	}
	e := detail.LogArchiveEntry
	what := entrySource(e)
	if e.Kind == api.LogKindReplica && e.Version != "" {
		what += " (" + e.Version + ")"
	}
	size := plural(e.Lines, "line")
	switch {
	case e.Lines == 0:
		size = "it printed nothing"
	case e.Truncated:
		size = "its last " + size
	}
	if len(detail.Output) < e.Lines {
		size += fmt.Sprintf(", the last %d shown", len(detail.Output))
	}
	c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("%s, %s: %s %s; %s", upperFirst(what), e.Container, describeEnd(e), ui.RelativeTime(e.EndedAt, c.now()), size)))
	print := c.logPrinter(false, timestamps)
	for _, line := range detail.Output {
		print(line)
	}
	return nil
}

func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// showRun prints the output of a run: what the archive kept of it, or, from
// an agent that keeps no archive or for a run that is still going, the tail
// in the run's own record.
func (c *cli) showRun(ctx context.Context, cl *client.Client, name string, runID int64, tail int, timestamps bool) error {
	entries, err := cl.LogArchive(ctx, name, api.LogKindRun, client.LogSource{Run: runID}, 1)
	if err != nil && !client.IsCode(err, api.CodeEndpointNotFound) {
		return err
	}
	if len(entries) > 0 {
		return c.showEntry(ctx, cl, name, entries[0].ID, tail, timestamps)
	}
	run, err := cl.Run(ctx, name, runID)
	if client.IsCode(err, api.CodeNotFound) {
		if _, appErr := cl.Application(ctx, name); appErr != nil {
			return appErr
		}
		return fmt.Errorf("%s has no run %d: the history keeps the last 50 runs of each job\n\nSee its jobs with: shipwick jobs %s", name, runID, name)
	} else if err != nil {
		return err
	}
	c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Run #%d of %s: %s, started %s", run.ID, run.Job, describeRun(run.Run), ui.RelativeTime(run.StartedAt, c.now()))))
	if run.Output != "" {
		c.ui.Println(run.Output)
	}
	return nil
}

// queryLogs prints the newest `want` lines that match, oldest first, under a
// line for each container they come from.
func (c *cli) queryLogs(ctx context.Context, cl *client.Client, name string, q client.LogSearch, want int, timestamps bool) error {
	var lines []api.LogMatch
	more := false
	for {
		q.Limit = min(want-len(lines), 1000)
		page, err := cl.SearchLogs(ctx, name, q)
		if err != nil {
			return noArchive(err)
		}
		lines = append(lines, page.Lines...)
		if page.Next == "" {
			break
		}
		if len(lines) >= want {
			more = true
			break
		}
		q.Cursor = page.Next
	}
	if len(lines) == 0 {
		c.ui.Println("No lines match, in what is kept or in the running containers.")
		return nil
	}

	print := c.logPrinter(false, timestamps)
	source := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if from := matchSource(line); from != source {
			source = from
			c.ui.Println(c.ui.Styled(ui.Dim, "== "+from+" =="))
		}
		print(line.LogLine)
	}
	if more {
		c.ui.Note("These are the newest %s that match; there are older ones. Show more with -n, or go back with --until %s",
			plural(len(lines), "line"), lines[len(lines)-1].Time.UTC().Format(time.RFC3339))
	}
	return nil
}

// matchSource names where a found line comes from: an entry of the archive,
// by its id, or a container that exists.
func matchSource(m api.LogMatch) string {
	what := fmt.Sprintf("#%d replica %d", m.Deployment, m.Replica)
	if m.RunID != nil {
		what = fmt.Sprintf("run %d of %s", *m.RunID, m.Job)
	}
	what += ", " + m.Container
	if m.ArchiveID != nil {
		return fmt.Sprintf("%s, kept as %d", what, *m.ArchiveID)
	}
	return what
}

// deathHints are the commands that show why the application's replicas died
// and what a failed deployment printed: the lines `shipwick status` ends with
// when it shows either. An agent without an archive, or with nothing kept,
// has none.
func (c *cli) deathHints(ctx context.Context, cl *client.Client, app api.ApplicationDetail, history []api.Deployment) [][2]string {
	troubled := false
	for _, ct := range app.Containers {
		if !ct.Stopping && (ct.CrashLoop || ct.Restarts > 0 || ct.OOMKilled || (ct.State == "exited" && ct.ExitCode != 0)) {
			troubled = true
		}
	}
	var failed *api.Deployment
	for i, d := range history {
		if d.Status == api.StatusFailed || d.Status == api.StatusRolledBack {
			failed = &history[i]
			break
		}
	}
	if !troubled && failed == nil {
		return nil
	}
	entries, err := cl.LogArchive(ctx, app.Name, api.LogKindReplica, client.LogSource{}, 100)
	if err != nil {
		return nil
	}

	var hints [][2]string
	now := c.now()
	for _, ct := range app.Containers {
		for _, e := range entries {
			if e.Container != ct.Name {
				continue
			}
			if e.Reason == api.LogReasonCrashed || e.Reason == api.LogReasonOOMKilled || e.Reason == api.LogReasonUnhealthy {
				hints = append(hints, [2]string{
					fmt.Sprintf("Replica %d's last output before it %s, %s:", ct.Replica, diedHow(e), ui.RelativeTime(e.EndedAt, now)),
					fmt.Sprintf("shipwick logs %s --id %d", app.Name, e.ID),
				})
				break
			}
		}
	}
	if failed != nil {
		for _, e := range entries {
			if e.DeploymentID != nil && *e.DeploymentID == failed.ID && failed.ID != 0 {
				hints = append(hints, [2]string{
					fmt.Sprintf("What deployment #%d printed before it failed:", failed.Sequence),
					fmt.Sprintf("shipwick logs %s --deployment %d", app.Name, failed.Sequence),
				})
				break
			}
		}
	}
	return hints
}

// diedHow completes "before it …" for an entry of a replica that died.
func diedHow(e api.LogArchiveEntry) string {
	switch {
	case e.Reason == api.LogReasonOOMKilled:
		return "ran out of memory"
	case e.Reason == api.LogReasonUnhealthy:
		return "was restarted for its health check"
	case e.ExitCode != nil:
		return fmt.Sprintf("exited with code %d", *e.ExitCode)
	}
	return "crashed"
}

// describeLogArchive is the archive's line in `shipwick server status`.
func describeLogArchive(a api.LogArchiveStatus) string {
	if !a.Enabled {
		return "off  (SHIPWICK_LOG_RETENTION_SIZE is 0 on the agent)"
	}
	return fmt.Sprintf("%s of %s, %s, kept %s", spec.FormatMemory(a.Bytes), spec.FormatMemory(a.MaxBytes), entries(a.Entries), plural(a.RetentionDays, "day"))
}

func entries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}
