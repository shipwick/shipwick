package deploy

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// The log archive. What a container printed is Docker's to keep, and Docker
// keeps it exactly as long as the container: a replica that a rollout
// replaced, a deployment that failed and was cleaned up, a job that finished
// — each takes its output with it. And a replica that crashes and is started
// again keeps its log only until the logging driver rotates it away.
//
// So the end of every run of a container is where its last lines are copied
// out: when a replica exits by itself or is restarted for failing its health
// check (the container stays, and the copy is made in the background), when
// the application is stopped, and before any container is removed (the copy
// has to be made first, by whoever removes it). Each copy begins where the
// last copy of the same container ended, so a line is kept once however often
// its container is looked at, and is bounded: the last replicaLogLines lines
// and replicaLogBytes bytes of a replica's run, runLogLines and runLogBytes
// of a job's.
//
// The lines are gzip files under the data directory, one per run, and the
// database holds one row per file saying what it is (store/logarchive.go).
// Files rather than rows, because the database is one file with one
// connection: output in it would be copied by every backup of the agent's
// state, would not give its space back when it is removed, and a search
// reading through it would make every deployment and every request wait.
// See docs/architecture.md, "The log archive".

// LogArchiveOptions say where ended containers' output is kept and how much.
type LogArchiveOptions struct {
	// Dir is the directory the files are written under. Empty: nothing is
	// kept.
	Dir string
	// MaxAge is how long an entry is kept after its run ended; MaxBytes how
	// much the files may take together. A MaxBytes of zero keeps nothing.
	MaxAge   time.Duration
	MaxBytes int64
}

const (
	// What is kept of one run of a replica, and of one run of a job: its
	// last lines, bounded both ways. A job's output is the job's result, and
	// gets more room.
	replicaLogLines = 2000
	replicaLogBytes = 1 << 20
	runLogLines     = 10000
	runLogBytes     = 4 << 20
	// logLineBytes is where a single line is cut.
	logLineBytes = 16 << 10

	// logCaptureTimeout bounds one copy, the reading from Docker included.
	logCaptureTimeout = 30 * time.Second
	// logArchiveInterval is how often retention is enforced on a quiet
	// archive; every copy enforces the size as well.
	logArchiveInterval = time.Hour

	logFileSuffix = ".log.gz"
	logTempPrefix = "capture-"
	// logTimeLayout stamps a line in its file: fixed width, so that a line
	// is taken apart by position.
	logTimeLayout = "2006-01-02T15:04:05.000000000Z"
)

// logReader is what the archive needs of the runtime beyond the Runtime
// interface: a container's log, a line at a time and by time.
type logReader interface {
	ReadLogs(ctx context.Context, id string, w docker.LogWindow, emit func(docker.LogEntry)) error
}

// logArchive is the engine's part of the archive's state.
type logArchive struct {
	mu sync.Mutex
	// busy are the containers being copied from right now, by ID: two copies
	// of one container at once would both begin where the last one ended.
	busy map[string]chan struct{}
	// files is held by whoever adds an entry with its file, and by whoever
	// compares the directory with the database: see tidyLogFiles.
	files sync.Mutex
}

// logEnd is how a run of a container ended, as far as the caller knows.
type logEnd struct {
	// reason is one of api.LogReason*; empty lets the copy work it out from
	// the container's deployment (a container that is being removed).
	reason string
	// exitCode and oomKilled, when the caller saw the exit itself; otherwise
	// the container is asked, if it is still down.
	exitCode  *int
	oomKilled bool
	// removing: the container is about to be removed, so everything it has
	// is taken, also of a run that has not ended.
	removing bool
}

func (e *Engine) archiving() bool {
	_, ok := e.rt.(logReader)
	return ok && e.opts.LogArchive.Dir != "" && e.opts.LogArchive.MaxBytes > 0
}

// claim waits for the container to be free of other copies and takes it.
func (a *logArchive) claim(ctx context.Context, id string) (release func(), err error) {
	for {
		a.mu.Lock()
		held, taken := a.busy[id]
		if !taken {
			if a.busy == nil {
				a.busy = map[string]chan struct{}{}
			}
			done := make(chan struct{})
			a.busy[id] = done
			a.mu.Unlock()
			return func() {
				a.mu.Lock()
				delete(a.busy, id)
				a.mu.Unlock()
				close(done)
			}, nil
		}
		a.mu.Unlock()
		select {
		case <-held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// archiveLogs copies what the container printed since its last copy into the
// archive. It is the one way output gets there. A copy that cannot be made
// is logged and nothing else: the output is lost, and whatever was being done
// to the container goes on.
func (e *Engine) archiveLogs(ctx context.Context, id string, end logEnd) {
	if !e.archiving() {
		return
	}
	// On its own clock: the caller's context is often one that has ended.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logCaptureTimeout)
	defer cancel()
	release, err := e.archive.claim(ctx, id)
	if err != nil {
		return
	}
	defer release()
	if err := e.archiveOutput(ctx, id, end); err != nil {
		// The error is Docker's or the disk's; it never quotes the output.
		e.log.Warn("could not archive a container's output", "container", id, "error", err)
	}
}

// archiveInBackground is archiveLogs for a container that stays: nobody has
// to wait for the copy. It counts as an operation, so a shutdown lets it
// finish; one that can no longer begin is made up for at the next start (see
// archiveMissedRuns).
func (e *Engine) archiveInBackground(id string, end logEnd) {
	if !e.archiving() || !e.beginOp() {
		return
	}
	go func() {
		defer e.opDone()
		e.archiveLogs(context.Background(), id, end)
	}()
}

func (e *Engine) archiveOutput(ctx context.Context, id string, end logEnd) error {
	began := time.Now()
	c, err := e.rt.InspectContainer(ctx, id)
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	// Only for applications the database knows: like every other thing the
	// agent does to a container.
	app, err := e.store.GetApplication(ctx, c.App)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}

	entry := store.LogArchive{
		ApplicationID: app.ID, Kind: api.LogKindReplica, Replica: c.Replica,
		ContainerID: c.ID, ContainerName: c.Name,
		Reason: end.reason, ExitCode: end.exitCode, OOMKilled: end.oomKilled,
	}
	maxLines, maxBytes := replicaLogLines, replicaLogBytes
	if c.Job != "" {
		run, err := e.store.GetJobRun(ctx, c.RunID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && run.ApplicationID != app.ID) {
			return nil
		} else if err != nil {
			return err
		}
		entry.Kind, entry.Replica, entry.Job, entry.RunID = api.LogKindRun, 0, run.Job, &run.ID
		entry.DeploymentID, entry.Reason, entry.ExitCode, entry.OOMKilled = run.DeploymentID, string(run.Status), run.ExitCode, false
		maxLines, maxBytes = runLogLines, runLogBytes
	} else {
		d, err := e.store.GetDeployment(ctx, c.DeploymentID)
		known := err == nil && d.ApplicationID == app.ID
		if known {
			entry.DeploymentID = &d.ID
		}
		if entry.Reason == "" {
			entry.Reason = e.removalReason(ctx, app, d, known)
		}
		if entry.ExitCode == nil && !c.Running && c.FinishedAt != nil {
			code := c.ExitCode
			entry.ExitCode, entry.OOMKilled = &code, c.OOMKilled
		}
	}

	through, err := e.store.LogArchivedThrough(ctx, c.ID)
	if err != nil {
		return err
	}
	var window docker.LogWindow
	if !through.IsZero() {
		window.Since = through.Add(time.Nanosecond)
	}
	entry.EndedAt = time.Now().UTC()
	ended := c.FinishedAt != nil && c.FinishedAt.After(through)
	switch {
	case end.removing:
		window.Tail = maxLines
		if !c.Running && c.FinishedAt != nil {
			entry.EndedAt = *c.FinishedAt
		}
	case !ended:
		return nil // no run of it has ended that is not in the archive
	default:
		// The container stays, and may be running again by now: the run
		// that ended is what it printed up to its stop. The daemon takes
		// the tail before it looks at the times, so a container that runs
		// again is read from where the last copy ended instead.
		entry.EndedAt, window.Until = *c.FinishedAt, *c.FinishedAt
		if !c.Running {
			window.Tail = maxLines
		}
	}

	tail := logTail{maxLines: maxLines, maxBytes: int64(maxBytes)}
	if err := e.rt.(logReader).ReadLogs(ctx, c.ID, window, tail.add); err != nil {
		return err
	}
	lines := tail.kept()
	if len(lines) == 0 {
		// A run that printed nothing is worth an entry when it ended badly
		// and by itself: that it died is the news, and where one looks for
		// why. A job's run says as much in its own record.
		died := entry.Kind == api.LogKindReplica && ended && entry.ExitCode != nil && (*entry.ExitCode != 0 || entry.OOMKilled)
		switch entry.Reason {
		case api.LogReasonCrashed, api.LogReasonOOMKilled, api.LogReasonDeploymentFailed:
		default:
			// Stopped by the agent: the exit code is the signal's.
			died = false
		}
		if !died {
			return nil
		}
	} else {
		entry.FirstAt, entry.LastAt = &lines[0].Time, &lines[len(lines)-1].Time
	}
	entry.Lines, entry.Bytes, entry.Truncated = len(lines), tail.bytes, tail.truncated
	if err := e.addLogEntry(ctx, entry, lines); err != nil {
		return err
	}
	e.log.Debug("archived a container's output", "container", c.Name, "reason", entry.Reason, "lines", entry.Lines, "took", time.Since(began).Round(time.Millisecond))
	return nil
}

// removalReason says why a replica's container is being removed, from where
// its deployment stands.
func (e *Engine) removalReason(ctx context.Context, app store.Application, d store.Deployment, known bool) string {
	switch {
	case !known:
		return api.LogReasonRemoved
	case d.Status == api.StatusSuperseded:
		return api.LogReasonReplaced
	case d.Status != api.StatusActive:
		return api.LogReasonDeploymentFailed
	}
	// The active deployment's replicas go one by one while the deployment
	// that replaces it is still on its way.
	newest, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: app.Name, Limit: 1})
	if err == nil && len(newest) == 1 && newest[0].ID != d.ID && newest[0].CompletedAt == nil && newest[0].Status != api.StatusFailed {
		return api.LogReasonReplaced
	}
	return api.LogReasonRemoved
}

// logTail keeps the last lines of what it is given, within both bounds.
type logTail struct {
	maxLines  int
	maxBytes  int64
	lines     []docker.LogEntry
	first     int // lines[:first] have been dropped
	bytes     int64
	truncated bool
}

func (t *logTail) add(line docker.LogEntry) {
	if len(line.Message) > logLineBytes {
		cut := logLineBytes
		for cut > 0 && !utf8.RuneStart(line.Message[cut]) {
			cut--
		}
		line.Message, t.truncated = line.Message[:cut], true
	}
	t.lines = append(t.lines, line)
	t.bytes += int64(len(line.Message))
	for len(t.lines)-t.first > t.maxLines || t.bytes > t.maxBytes {
		t.bytes -= int64(len(t.lines[t.first].Message))
		t.lines[t.first] = docker.LogEntry{}
		t.first++
		t.truncated = true
	}
	// Dropped lines are let go of once they are half of what is held.
	if t.first > 1024 && t.first > len(t.lines)/2 {
		t.lines = append(t.lines[:0], t.lines[t.first:]...)
		t.first = 0
	}
}

func (t *logTail) kept() []docker.LogEntry {
	return t.lines[t.first:]
}

// logFilePath is where an entry's lines are. Both parts are numbers the
// database made: nothing an application or a request named is in the path.
func logFilePath(dir string, f store.LogArchiveFile) string {
	return filepath.Join(dir, strconv.FormatInt(f.ApplicationID, 10), strconv.FormatInt(f.ID, 10)+logFileSuffix)
}

// addLogEntry writes the lines to a file and records the entry, in that
// order: an entry is never listed before its lines can be read.
func (e *Engine) addLogEntry(ctx context.Context, entry store.LogArchive, lines []docker.LogEntry) error {
	dir := e.opts.LogArchive.Dir
	temp := ""
	if len(lines) > 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create the log archive: %w", err)
		}
		f, err := os.CreateTemp(dir, logTempPrefix+"*")
		if err != nil {
			return fmt.Errorf("write to the log archive: %w", err)
		}
		temp = f.Name()
		err = writeLogFile(f, lines)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		var info os.FileInfo
		if err == nil {
			info, err = os.Stat(temp)
		}
		if err != nil {
			os.Remove(temp)
			return fmt.Errorf("write to the log archive: %w", err)
		}
		entry.StoredBytes = info.Size()
	}

	e.archive.files.Lock()
	id, err := e.store.AddLogArchive(ctx, entry, time.Now())
	if err == nil && temp != "" {
		path := logFilePath(dir, store.LogArchiveFile{ID: id, ApplicationID: entry.ApplicationID})
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			err = os.Rename(temp, path)
		}
		if err != nil {
			if derr := e.store.DeleteLogArchive(ctx, id); derr != nil {
				e.log.Warn("could not remove a log archive entry without a file", "entry", id, "error", derr)
			}
		}
	}
	e.archive.files.Unlock()
	if err != nil {
		if temp != "" {
			os.Remove(temp)
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil // the application, or the run, was deleted meanwhile
		}
		return err
	}
	e.enforceLogSize(ctx, entry.StoredBytes)
	return nil
}

// writeLogFile writes lines as gzip: one line each, "<time> <o|e> <message>",
// which `zcat` shows as it is.
func writeLogFile(w io.Writer, lines []docker.LogEntry) error {
	zw := gzip.NewWriter(w)
	bw := bufio.NewWriter(zw)
	for _, line := range lines {
		stream := byte('o')
		if line.Stream == "stderr" {
			stream = 'e'
		}
		bw.WriteString(line.Time.UTC().Format(logTimeLayout))
		bw.Write([]byte{' ', stream, ' '})
		bw.WriteString(line.Message)
		bw.WriteByte('\n')
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

// readLogFile calls fn for every line of an entry's file, oldest first. The
// message is only valid during the call.
func readLogFile(path string, fn func(at time.Time, stream string, message []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	const header = len(logTimeLayout) + len(" o ")
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), logLineBytes+header+1)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < header {
			continue
		}
		at, err := time.Parse(logTimeLayout, string(line[:len(logTimeLayout)]))
		if err != nil {
			continue
		}
		stream := "stdout"
		if line[len(logTimeLayout)+1] == 'e' {
			stream = "stderr"
		}
		fn(at, stream, line[header:])
	}
	return sc.Err()
}

// enforceLogSize removes entries until the archive is within its size: the
// oldest of the application that holds the most, see store.PruneLogArchivesTo.
// added is what the caller just put there. While the disk is as full
// as the disk alert's threshold, the archive may not grow at all: what was
// added has to be made room for by removing at least as much, so that the
// agent is not what fills the last of a disk.
func (e *Engine) enforceLogSize(ctx context.Context, added int64) {
	limit := e.opts.LogArchive.MaxBytes
	if u := e.Disk(); u != nil && int(percentOf(u.UsedBytes, u.TotalBytes)) >= e.opts.AlertDiskPercent {
		if _, total, err := e.store.LogArchiveUsage(ctx); err == nil {
			limit = min(limit, total-added)
		}
	}
	e.archive.files.Lock()
	defer e.archive.files.Unlock()
	files, err := e.store.PruneLogArchivesTo(ctx, limit)
	if err != nil {
		e.log.Warn("could not prune the log archive", "error", err)
		return
	}
	e.removeLogFiles(files)
}

func (e *Engine) removeLogFiles(files []store.LogArchiveFile) {
	for _, f := range files {
		if err := os.Remove(logFilePath(e.opts.LogArchive.Dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.log.Warn("could not remove a log archive file", "entry", f.ID, "error", err)
		}
	}
}

// purgeLogs removes what the archive holds of a deleted application. Its
// entries went with its record; these are the files.
func (e *Engine) purgeLogs(appID int64) {
	if e.opts.LogArchive.Dir == "" {
		return
	}
	e.archive.files.Lock()
	defer e.archive.files.Unlock()
	dir := filepath.Join(e.opts.LogArchive.Dir, strconv.FormatInt(appID, 10))
	if err := os.RemoveAll(dir); err != nil {
		e.log.Warn("could not remove a deleted application's archived logs", "error", err)
	}
}

// startLogArchive enforces the archive's retention once now and then every
// logArchiveInterval, and first copies what ended while no agent was there
// to copy it.
func (e *Engine) startLogArchive() {
	if e.opts.LogArchive.Dir == "" {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		e.archiveMissedRuns(e.baseCtx)
		e.tendLogArchive(e.baseCtx, time.Now(), true)
		ticker := time.NewTicker(logArchiveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.tendLogArchive(e.baseCtx, now, false)
			}
		}
	}()
}

// archiveMissedRuns looks, once at startup, for runs that ended without
// being archived: the agent was stopped between a container's end and its
// copy, or was not running when it ended. A replica that is down under an
// application that should run is the supervisor's, whose first tick finds it
// and says why it died; left here are the replica that runs again, and the
// one of an application that was stopped.
func (e *Engine) archiveMissedRuns(ctx context.Context) {
	if !e.archiving() {
		return
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return
	}
	running := make(map[string]bool, len(apps))
	for _, app := range apps {
		running[app.Name] = app.DesiredState == api.DesiredRunning
	}
	containers, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return
	}
	for _, c := range containers {
		shouldRun, known := running[c.App]
		if !known || c.Job != "" || ctx.Err() != nil {
			continue
		}
		switch {
		case c.Running:
			e.archiveLogs(ctx, c.ID, logEnd{reason: api.LogReasonRestarted})
		case !shouldRun:
			e.archiveLogs(ctx, c.ID, logEnd{reason: api.LogReasonStopped})
		}
	}
}

// tendLogArchive is the archive's housekeeping: entries past their age go,
// the size is enforced, and the directory is made to agree with the
// database. Time is a parameter so that tests can let entries age. starting
// says that no copy can be under way, and unfinished files are leftovers.
func (e *Engine) tendLogArchive(ctx context.Context, now time.Time, starting bool) {
	if e.opts.LogArchive.MaxAge > 0 {
		e.archive.files.Lock()
		files, err := e.store.PruneLogArchivesBefore(ctx, now.Add(-e.opts.LogArchive.MaxAge))
		if err != nil {
			e.log.Warn("could not prune the log archive", "error", err)
		}
		e.removeLogFiles(files)
		e.archive.files.Unlock()
		if len(files) > 0 {
			e.log.Info("removed archived logs past their retention", "entries", len(files))
		}
	}
	e.enforceLogSize(ctx, 0)
	if err := e.tidyLogFiles(ctx, 0, now, starting); err != nil && ctx.Err() == nil {
		e.log.Warn("could not tidy the log archive", "error", err)
	}
}

// tidyLogFiles removes the files no entry names — of an application that was
// deleted while a copy was being made, of a job's run that was pruned — and
// the entries whose file is gone, as after the database was restored from a
// backup, which does not hold the files. With an application's id it looks
// at that application's files only.
func (e *Engine) tidyLogFiles(ctx context.Context, appID int64, now time.Time, starting bool) error {
	e.archive.files.Lock()
	defer e.archive.files.Unlock()

	dir := e.opts.LogArchive.Dir
	entries, err := e.store.LogArchiveFiles(ctx, appID)
	if err != nil {
		return err
	}
	expected := make(map[string]int64, len(entries))
	for _, f := range entries {
		expected[logFilePath(dir, f)] = f.ID
	}

	top, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		top = nil
	} else if err != nil {
		return err
	}
	for _, d := range top {
		path := filepath.Join(dir, d.Name())
		if appID != 0 && d.Name() != strconv.FormatInt(appID, 10) {
			continue
		}
		if !d.IsDir() {
			// A copy that never got its entry. One that is being written
			// right now is younger than any copy takes.
			info, err := d.Info()
			if err == nil && strings.HasPrefix(d.Name(), logTempPrefix) && (starting || now.Sub(info.ModTime()) > 2*logCaptureTimeout) {
				os.Remove(path)
			}
			continue
		}
		files, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		left := len(files)
		for _, f := range files {
			file := filepath.Join(path, f.Name())
			if _, ok := expected[file]; ok {
				delete(expected, file)
				continue
			}
			if strings.HasSuffix(f.Name(), logFileSuffix) && os.Remove(file) == nil {
				left--
			}
		}
		if left == 0 {
			os.Remove(path)
		}
	}
	for _, id := range expected {
		if err := e.store.DeleteLogArchive(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// logArchiveStatus is the archive's size and bounds, for GET /server.
func (e *Engine) logArchiveStatus(ctx context.Context) *api.LogArchiveStatus {
	status := &api.LogArchiveStatus{
		Enabled:       e.archiving(),
		MaxBytes:      e.opts.LogArchive.MaxBytes,
		RetentionDays: int(e.opts.LogArchive.MaxAge / (24 * time.Hour)),
	}
	status.Entries, status.Bytes, _ = e.store.LogArchiveUsage(ctx)
	return status
}

// endOf is the end of a replica that the supervisor found down: it exited by
// itself, and its container says how.
func endOf(c docker.Container) logEnd {
	code := c.ExitCode
	end := logEnd{reason: api.LogReasonExited, exitCode: &code, oomKilled: c.OOMKilled}
	switch {
	case c.OOMKilled:
		end.reason = api.LogReasonOOMKilled
	case code != 0:
		end.reason = api.LogReasonCrashed
	}
	return end
}

// dropPrunedRunLogs removes the files of the application's runs that its
// job history no longer holds: their entries went with the runs, and the
// space is given back now rather than at the next housekeeping.
func (e *Engine) dropPrunedRunLogs(ctx context.Context, appID int64) {
	if !e.archiving() {
		return
	}
	if err := e.tidyLogFiles(ctx, appID, time.Now(), false); err != nil {
		e.log.Warn("could not remove the archived output of pruned runs", "error", err)
	}
}
