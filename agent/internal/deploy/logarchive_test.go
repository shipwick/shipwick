package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// keepLogs gives the harness a log archive in a directory of the test's.
func keepLogs(h *harness) string {
	h.t.Helper()
	dir := h.t.TempDir()
	h.engine.opts.LogArchive = LogArchiveOptions{Dir: dir, MaxAge: 14 * 24 * time.Hour, MaxBytes: 1 << 30}
	return dir
}

// kept lists what is kept of the application, oldest first: the order in
// which it happened.
func (h *harness) kept(name string) []api.LogArchiveEntry {
	h.t.Helper()
	entries, err := h.engine.LogArchive(context.Background(), name, LogArchiveQuery{})
	if err != nil {
		h.t.Fatalf("LogArchive: %v", err)
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}

// output is the lines of one entry.
func (h *harness) output(name string, id int64) []string {
	h.t.Helper()
	detail, err := h.engine.ArchivedLogs(context.Background(), name, id, 0)
	if err != nil {
		h.t.Fatalf("ArchivedLogs(%d): %v", id, err)
	}
	out := []string{}
	for _, line := range detail.Output {
		out = append(out, line.Message)
	}
	return out
}

func (h *harness) replicaAt(index int) docker.Container {
	h.t.Helper()
	for _, c := range h.rt.Containers() {
		if c.Replica == index && c.Job == "" {
			return c
		}
	}
	h.t.Fatalf("no container for replica %d", index)
	return docker.Container{}
}

func wantLines(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got)+len(want) > 0 && !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func TestReplacedReplicaLeavesItsLastLinesInTheArchive(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))
	old := h.replicaAt(1)
	h.rt.WriteLogs(old.ID, "listening on :8080")
	h.rt.WriteErrorLogs(old.ID, "GET /orders 500")

	h.deploy(app("my-api", "my-api:1.1", 1))

	if _, err := h.rt.InspectContainer(context.Background(), old.ID); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("the replaced container should be gone: %v", err)
	}
	entries := h.kept("my-api")
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the one replaced replica", entries)
	}
	e := entries[0]
	if e.Kind != api.LogKindReplica || e.Reason != api.LogReasonReplaced || e.Replica != 1 || e.Container != old.Name ||
		e.DeploymentID == nil || *e.DeploymentID != v1.ID || e.Deployment != v1.Sequence || e.Version != v1.Version {
		t.Errorf("entry does not say what it is the output of: %+v", e)
	}
	if e.Lines != 2 || e.Bytes != int64(len("listening on :8080")+len("GET /orders 500")) || e.StoredBytes == 0 || e.Truncated {
		t.Errorf("entry's size: %+v", e)
	}
	if e.FirstLineAt == nil || e.LastLineAt == nil || !e.FirstLineAt.Before(*e.LastLineAt) || e.EndedAt.Before(*e.LastLineAt) {
		t.Errorf("entry's times: first %v last %v ended %v", e.FirstLineAt, e.LastLineAt, e.EndedAt)
	}

	detail, err := h.engine.ArchivedLogs(context.Background(), "my-api", e.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Output) != 2 || detail.Output[0].Stream != "stdout" || detail.Output[1].Stream != "stderr" ||
		detail.Output[1].Message != "GET /orders 500" || !detail.Output[1].Time.Equal(*e.LastLineAt) ||
		detail.Output[0].Replica != 1 || detail.Output[0].Container != old.Name {
		t.Errorf("lines read back: %+v", detail.Output)
	}
}

func TestDeploymentDoesNotWaitForTheArchiveOfTheReplicaItReplaced(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.WriteLogs(h.replicaAt(1).ID, "still serving")

	stopping := h.rt.HoldStops()
	d, err := h.engine.Deploy(context.Background(), app("my-api", "my-api:1.1", 1))
	if err != nil {
		t.Fatal(err)
	}
	<-stopping
	if got := h.awaitCompleted(d.ID); got.Status != api.StatusActive {
		t.Fatalf("deployment = %s (%s), want ACTIVE while the old replica is still stopping", got.Status, got.Error)
	}
	if entries := h.kept("my-api"); len(entries) != 0 {
		t.Fatalf("nothing has ended yet: %+v", entries)
	}

	h.rt.ReleaseStops()
	h.engine.Wait()
	entries := h.kept("my-api")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonReplaced {
		t.Fatalf("entries = %+v, want the replaced replica once it had stopped", entries)
	}
	wantLines(t, "replaced replica", h.output("my-api", entries[0].ID), "still serving")
}

func TestCrashLoopKeepsTheOutputOfEveryAttemptOnce(t *testing.T) {
	s := newSupervised(t)
	keepLogs(s.harness)
	s.rt.PrintOnStart("my-api:1.0", "booting", "panic: no database")
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID

	for attempt := 1; attempt <= 3; attempt++ {
		s.rt.Crash(id, 2)
		s.advance(time.Minute) // notices the exit
		s.engine.Wait()
		s.advance(10 * time.Minute) // restarts it
	}

	entries := s.kept("my-api")
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want one per attempt that crashed: %+v", len(entries), entries)
	}
	for i, e := range entries {
		if e.Reason != api.LogReasonCrashed || e.ExitCode == nil || *e.ExitCode != 2 || e.OOMKilled {
			t.Errorf("attempt %d: %+v", i+1, e)
		}
		wantLines(t, fmt.Sprintf("attempt %d", i+1), s.output("my-api", e.ID), "booting", "panic: no database")
	}
	if !entries[0].EndedAt.Before(entries[1].EndedAt) {
		t.Errorf("attempts are not in order: %v, %v", entries[0].EndedAt, entries[1].EndedAt)
	}

	// The container is removed at last: what it printed since the third
	// crash is all that is left to keep.
	s.deploy(app("my-api", "my-api:1.1", 1))
	entries = s.kept("my-api")
	if len(entries) != 4 || entries[3].Reason != api.LogReasonReplaced {
		t.Fatalf("after the replacement: %+v", entries)
	}
	wantLines(t, "the run that was replaced", s.output("my-api", entries[3].ID), "booting", "panic: no database")
}

func TestCrashWithoutOutputIsStillAnEntry(t *testing.T) {
	s := newSupervised(t)
	keepLogs(s.harness)
	s.deploy(app("my-api", "my-api:1.0", 1))
	s.rt.Crash(s.container(t, 1).ID, 139)
	s.advance(time.Second)
	s.engine.Wait()

	entries := s.kept("my-api")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonCrashed || *entries[0].ExitCode != 139 || entries[0].Lines != 0 || entries[0].StoredBytes != 0 {
		t.Fatalf("entries = %+v, want the crash, with no lines", entries)
	}
	wantLines(t, "no output", s.output("my-api", entries[0].ID))

	// Looked at again, the same exit is not a second entry.
	s.engine.archiveLogs(context.Background(), s.container(t, 1).ID, logEnd{reason: api.LogReasonCrashed})
	if entries := s.kept("my-api"); len(entries) != 1 {
		t.Errorf("the same exit was archived twice: %+v", entries)
	}
}

func TestMemoryKillIsArchivedAsOne(t *testing.T) {
	s := newSupervised(t)
	keepLogs(s.harness)
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID
	s.rt.WriteLogs(id, "loading the whole table")
	s.rt.KillForMemory(id)
	s.advance(time.Second)
	s.engine.Wait()

	entries := s.kept("my-api")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonOOMKilled || !entries[0].OOMKilled || *entries[0].ExitCode != 137 {
		t.Fatalf("entries = %+v, want one killed for memory", entries)
	}
	wantLines(t, "killed replica", s.output("my-api", entries[0].ID), "loading the whole table")
}

func TestUnhealthyRestartKeepsWhatTheRunPrinted(t *testing.T) {
	s := newSupervised(t)
	keepLogs(s.harness)
	s.deploy(withHealth(app("my-api", "my-api:1.0", 1)))
	c := s.container(t, 1)
	s.rt.WriteLogs(c.ID, "deadlock in worker 3")

	s.probes.setFailing(c.IP, errors.New("timeout"))
	for i := 0; i < 5; i++ {
		s.advance(10 * time.Second)
	}
	s.engine.Wait()
	if s.rt.Starts(c.ID) < 2 {
		t.Fatal("the unhealthy replica was not restarted")
	}
	entries := s.kept("my-api")
	if len(entries) == 0 || entries[0].Reason != api.LogReasonUnhealthy {
		t.Fatalf("entries = %+v, want the run that was restarted for its health", entries)
	}
	wantLines(t, "unhealthy run", s.output("my-api", entries[0].ID), "deadlock in worker 3")
}

func TestFailedDeploymentLeavesTheOutputOfItsReplicas(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.CrashImages["my-api:broken"] = true
	h.rt.PrintOnStart("my-api:broken", "Error: DATABASE_URL is not set")

	d := h.deploy(app("my-api", "my-api:broken", 1))
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	entries, err := h.engine.LogArchive(context.Background(), "my-api", LogArchiveQuery{DeploymentID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Reason != api.LogReasonDeploymentFailed || entries[0].ExitCode == nil || *entries[0].ExitCode != 1 || entries[0].Deployment != d.Sequence {
		t.Fatalf("entries of the failed deployment = %+v", entries)
	}
	wantLines(t, "failed replica", h.output("my-api", entries[0].ID), "Error: DATABASE_URL is not set")
	if all := h.kept("my-api"); len(all) != 1 {
		t.Errorf("the version that kept running has nothing in the archive: %+v", all)
	}
}

func TestStoppingAnApplicationArchivesItsRunAndStartingItAgainDoesNotRepeatIt(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	id := h.replicaAt(1).ID
	h.rt.WriteLogs(id, "before the stop")

	if err := h.engine.Stop(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	entries := h.kept("my-api")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonStopped || entries[0].ExitCode == nil || *entries[0].ExitCode != 0 {
		t.Fatalf("entries = %+v, want the stopped run", entries)
	}

	if err := h.engine.Start(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	h.rt.WriteLogs(id, "after the start")
	h.deploy(app("my-api", "my-api:1.1", 1))
	entries = h.kept("my-api")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the stopped run and the replaced one", entries)
	}
	wantLines(t, "stopped run", h.output("my-api", entries[0].ID), "before the stop")
	wantLines(t, "replaced run", h.output("my-api", entries[1].ID), "after the start")
}

func TestQuietReplicaThatIsReplacedLeavesNoEntry(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 2))
	h.deploy(app("my-api", "my-api:1.1", 2))
	if entries := h.kept("my-api"); len(entries) != 0 {
		t.Errorf("nothing was printed, and nothing died: %+v", entries)
	}
}

func TestRunKeepsMoreOfItsOutputInTheArchiveThanInItsRecord(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("migrated table %d", i)
	}
	h.rt.PrintOnStart("my-api:1.0", lines...)
	h.deploy(app("my-api", "my-api:1.0", 1))

	run, err := h.engine.RunCommand(context.Background(), "my-api", []string{"migrate"})
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()

	detail, err := h.engine.JobRun(context.Background(), "my-api", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(detail.Output, "\n") + 1; got != jobOutputLines {
		t.Errorf("the run's own record keeps %d lines, want its tail of %d as before", got, jobOutputLines)
	}
	entries, err := h.engine.LogArchive(context.Background(), "my-api", LogArchiveQuery{RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries of the run = %+v", entries)
	}
	e := entries[0]
	if e.Kind != api.LogKindRun || e.Job != commandJobName || e.RunID == nil || *e.RunID != run.ID || e.Replica != 0 ||
		e.Reason != string(api.RunSucceeded) || e.ExitCode == nil || *e.ExitCode != 0 || e.Lines != 300 {
		t.Errorf("entry of the run: %+v", e)
	}
	if got := h.output("my-api", e.ID); got[0] != "migrated table 0" || got[299] != "migrated table 299" {
		t.Errorf("the archive should hold the whole output, got %d lines from %q to %q", len(got), got[0], got[len(got)-1])
	}

	// The archive follows the job's history: a run that is pruned takes its
	// output along.
	if err := h.store.PruneJobRuns(context.Background(), appID(h, "my-api"), commandJobName, 0); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.engine.LogArchive(context.Background(), "my-api", LogArchiveQuery{RunID: run.ID}); len(left) != 0 {
		t.Errorf("the pruned run's output is still listed: %+v", left)
	}
}

func TestArchiveKeepsTheLastLinesWithinBothBounds(t *testing.T) {
	tail := logTail{maxLines: 3, maxBytes: 1 << 20}
	for i := 1; i <= 5; i++ {
		tail.add(docker.LogEntry{Message: fmt.Sprint("line ", i)})
	}
	if got := tail.kept(); len(got) != 3 || got[0].Message != "line 3" || got[2].Message != "line 5" || !tail.truncated || tail.bytes != 18 {
		t.Errorf("bounded by lines: %+v, truncated=%v, bytes=%d", got, tail.truncated, tail.bytes)
	}

	tail = logTail{maxLines: 1000, maxBytes: 25}
	for i := 0; i < 2000; i++ {
		tail.add(docker.LogEntry{Message: "0123456789"})
	}
	if got := tail.kept(); len(got) != 2 || tail.bytes != 20 || !tail.truncated {
		t.Errorf("bounded by bytes: %d lines, %d bytes, truncated=%v", len(got), tail.bytes, tail.truncated)
	}

	tail = logTail{maxLines: 10, maxBytes: 1 << 20}
	tail.add(docker.LogEntry{Message: strings.Repeat("é", logLineBytes)})
	if got := tail.kept()[0].Message; len(got) > logLineBytes || !strings.HasSuffix(got, "é") || !tail.truncated {
		t.Errorf("a long line should be cut between characters at %d bytes, got %d", logLineBytes, len(got))
	}

	tail = logTail{maxLines: 10, maxBytes: 1 << 20}
	tail.add(docker.LogEntry{Message: "all of it"})
	if tail.truncated {
		t.Error("output that fits is not truncated")
	}
}

func TestReplicaKeepsItsLastTwoThousandLines(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	lines := make([]string, replicaLogLines+500)
	for i := range lines {
		lines[i] = fmt.Sprint("request ", i)
	}
	h.rt.WriteLogs(h.replicaAt(1).ID, lines...)
	h.deploy(app("my-api", "my-api:1.1", 1))

	entries := h.kept("my-api")
	if len(entries) != 1 || entries[0].Lines != replicaLogLines {
		t.Fatalf("entries = %+v, want %d lines", entries, replicaLogLines)
	}
	got := h.output("my-api", entries[0].ID)
	if got[0] != "request 500" || got[len(got)-1] != fmt.Sprint("request ", len(lines)-1) {
		t.Errorf("kept %q … %q, want the last lines", got[0], got[len(got)-1])
	}
	// The daemon was asked for the tail, not for the whole log.
	reads := h.rt.LogReads()
	if last := reads[len(reads)-1]; last.Window.Tail != replicaLogLines && reads[0].Window.Tail != replicaLogLines {
		t.Errorf("log was read without a tail: %+v", reads)
	}
}

func TestContainerWhoseLogCannotBeReadIsRemovedAllTheSame(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.WriteLogs(h.replicaAt(1).ID, "sent to syslog only")
	h.rt.FailLogReads(dockertest.ErrNoLogs)

	d := h.deploy(app("my-api", "my-api:1.1", 1))
	if d.Status != api.StatusActive || len(h.rt.Containers()) != 1 {
		t.Fatalf("deployment = %s, containers = %d", d.Status, len(h.rt.Containers()))
	}
	if entries := h.kept("my-api"); len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
}

func TestNothingIsKeptWhenTheArchiveIsTurnedOff(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	h.engine.opts.LogArchive.MaxBytes = 0
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.WriteLogs(h.replicaAt(1).ID, "a line")
	h.deploy(app("my-api", "my-api:1.1", 1))

	if entries := h.kept("my-api"); len(entries) != 0 {
		t.Errorf("entries = %+v", entries)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("files were written: %v", files)
	}
	if len(h.rt.LogReads()) != 0 {
		t.Error("the log was read for an archive that keeps nothing")
	}
}

func TestDeletingAnApplicationRemovesItsArchivedLogs(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.WriteLogs(h.replicaAt(1).ID, "password=hunter2")
	h.deploy(app("my-api", "my-api:1.1", 1))
	h.deploy(app("other", "other:1.0", 1))
	h.rt.WriteLogs(h.rt.Containers()[len(h.rt.Containers())-1].ID, "other's line")
	h.deploy(app("other", "other:1.1", 1))
	if len(h.kept("my-api")) != 1 || len(h.kept("other")) != 1 {
		t.Fatal("both applications should have an entry")
	}

	if err := h.engine.Delete(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.LogArchive(context.Background(), "my-api", LogArchiveQuery{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("archive of a deleted application: %v, want not found", err)
	}
	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 1 {
		t.Errorf("files left = %v, want only the other application's", files)
	}
	if entries := h.kept("other"); len(entries) != 1 {
		t.Errorf("the other application lost its archive: %+v", entries)
	}
}

func TestRetentionRemovesEntriesPastTheirAge(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.WriteLogs(h.replicaAt(1).ID, "old")
	h.deploy(app("my-api", "my-api:1.1", 1))
	entries := h.kept("my-api")
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	path := logFilePath(dir, store.LogArchiveFile{ID: entries[0].ID, ApplicationID: appID(h, "my-api")})

	h.engine.tendLogArchive(context.Background(), time.Now().Add(13*24*time.Hour), false)
	if len(h.kept("my-api")) != 1 {
		t.Fatal("an entry was removed before its fourteen days were over")
	}
	h.engine.tendLogArchive(context.Background(), time.Now().Add(15*24*time.Hour), false)
	if left := h.kept("my-api"); len(left) != 0 {
		t.Errorf("entries past their age: %+v", left)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the entry's file is still there: %v", err)
	}
}

func appID(h *harness, name string) int64 {
	h.t.Helper()
	app, err := h.store.GetApplication(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return app.ID
}

// churn replaces the application's replica n times, each printing one line
// of its own, and returns the entries.
func churn(h *harness, n int) []api.LogArchiveEntry {
	h.t.Helper()
	h.deploy(app("my-api", "my-api:0", 1))
	for i := 1; i <= n; i++ {
		h.rt.WriteLogs(h.replicaAt(1).ID, fmt.Sprintf("version %d says %s", i-1, strings.Repeat("x", 64)))
		h.deploy(app("my-api", fmt.Sprint("my-api:", i), 1))
	}
	entries := h.kept("my-api")
	if len(entries) != n {
		h.t.Fatalf("entries = %d, want %d", len(entries), n)
	}
	return entries
}

func TestRetentionRemovesTheOldestEntriesOverTheSize(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	entries := churn(h, 4)
	each := entries[0].StoredBytes

	// Room for three and a half: the fifth entry pushes the oldest two out.
	h.engine.opts.LogArchive.MaxBytes = each*3 + each/2
	h.rt.WriteLogs(h.replicaAt(1).ID, fmt.Sprintf("version 4 says %s", strings.Repeat("x", 64)))
	h.deploy(app("my-api", "my-api:5", 1))

	left := h.kept("my-api")
	if len(left) != 3 || left[0].ID != entries[2].ID {
		t.Fatalf("left = %+v, want the newest three", left)
	}
	for _, gone := range entries[:2] {
		if _, err := os.Stat(logFilePath(dir, store.LogArchiveFile{ID: gone.ID, ApplicationID: appID(h, "my-api")})); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("file of removed entry %d: %v", gone.ID, err)
		}
	}
	status := h.engine.logArchiveStatus(context.Background())
	if !status.Enabled || status.Entries != 3 || status.Bytes > status.MaxBytes || status.RetentionDays != 14 {
		t.Errorf("status = %+v", status)
	}
}

func TestArchiveDoesNotGrowWhileTheDiskIsAsFullAsItsAlert(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	entries := churn(h, 3)
	_, before, _ := h.store.LogArchiveUsage(context.Background())

	h.engine.opts.DiskUsage = func() (api.DiskUsage, bool) { return api.DiskUsage{TotalBytes: 100, UsedBytes: 85}, true }
	h.rt.WriteLogs(h.replicaAt(1).ID, fmt.Sprintf("version 3 says %s", strings.Repeat("x", 64)))
	h.deploy(app("my-api", "my-api:4", 1))

	left := h.kept("my-api")
	_, after, _ := h.store.LogArchiveUsage(context.Background())
	if after > before {
		t.Errorf("the archive grew from %d to %d bytes on a disk that is 85%% full", before, after)
	}
	// Entries differ by a byte or two once compressed, so the newest takes
	// the place of the oldest one or two.
	if len(left) == 0 || len(left) > 3 || left[0].ID == entries[0].ID || left[len(left)-1].ID <= entries[2].ID {
		t.Fatalf("left = %+v, want the newest entry in place of the oldest", left)
	}
	wantLines(t, "the newest entry", h.output("my-api", left[len(left)-1].ID), "version 3 says "+strings.Repeat("x", 64))

	h.engine.opts.DiskUsage = func() (api.DiskUsage, bool) { return api.DiskUsage{TotalBytes: 100, UsedBytes: 84}, true }
	h.rt.WriteLogs(h.replicaAt(1).ID, fmt.Sprintf("version 4 says %s", strings.Repeat("x", 64)))
	h.deploy(app("my-api", "my-api:5", 1))
	if grown := h.kept("my-api"); len(grown) != len(left)+1 {
		t.Errorf("below the threshold the archive grows again: %+v", grown)
	}
}

func TestTidyingRemovesFilesWithoutAnEntryAndEntriesWithoutAFile(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	entries := churn(h, 2)
	id := appID(h, "my-api")

	stray := filepath.Join(dir, fmt.Sprint(id), "999"+logFileSuffix)
	unfinished := filepath.Join(dir, logTempPrefix+"123")
	gone := filepath.Join(dir, "4242", "7"+logFileSuffix)
	os.MkdirAll(filepath.Dir(gone), 0o700)
	for _, path := range []string{stray, unfinished, gone} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(logFilePath(dir, store.LogArchiveFile{ID: entries[0].ID, ApplicationID: id})); err != nil {
		t.Fatal(err)
	}

	h.engine.tendLogArchive(context.Background(), time.Now(), false)
	if _, err := os.Stat(unfinished); err != nil {
		t.Errorf("a copy that may be under way was removed: %v", err)
	}
	h.engine.tendLogArchive(context.Background(), time.Now(), true)

	for _, path := range []string{stray, unfinished, gone, filepath.Dir(gone)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there: %v", path, err)
		}
	}
	left := h.kept("my-api")
	if len(left) != 1 || left[0].ID != entries[1].ID {
		t.Errorf("left = %+v, want only the entry whose file exists", left)
	}
	wantLines(t, "the entry that stayed", h.output("my-api", left[0].ID), "version 1 says "+strings.Repeat("x", 64))
}

func TestStartupArchivesTheRunsThatEndedUnseen(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.deploy(app("stopped", "stopped:1.0", 1))
	var restarted, stopped docker.Container
	for _, c := range h.rt.Containers() {
		if c.App == "my-api" {
			restarted = c
		} else {
			stopped = c
		}
	}

	// What an agent that was killed at the wrong moment leaves behind: a
	// replica that crashed and runs again, and an application whose stop was
	// recorded, neither with its copy made.
	ctx := context.Background()
	h.rt.WriteLogs(restarted.ID, "first run")
	h.rt.Crash(restarted.ID, 1)
	h.rt.StartContainer(ctx, restarted.ID)
	h.rt.WriteLogs(restarted.ID, "second run")
	h.rt.WriteLogs(stopped.ID, "bye")
	h.rt.StopContainer(ctx, stopped.ID, time.Second)
	a, _ := h.store.GetApplication(ctx, "stopped")
	h.store.SetDesiredState(ctx, a.ID, api.DesiredStopped, time.Now())

	h.engine.archiveMissedRuns(ctx)
	h.engine.archiveMissedRuns(ctx) // an agent that starts twice

	entries := h.kept("my-api")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonRestarted || entries[0].ExitCode != nil {
		t.Fatalf("my-api: %+v, want the run that ended, its exit code unknown", entries)
	}
	wantLines(t, "the run that ended", h.output("my-api", entries[0].ID), "first run")
	entries = h.kept("stopped")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonStopped {
		t.Fatalf("stopped: %+v", entries)
	}
	wantLines(t, "the stopped run", h.output("stopped", entries[0].ID), "bye")
}

func TestServerSaysWhatTheArchiveHolds(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	churn(h, 2)
	server, err := h.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := server.LogArchive
	if a == nil || !a.Enabled || a.Entries != 2 || a.Bytes == 0 || a.MaxBytes != 1<<30 || a.RetentionDays != 14 {
		t.Errorf("log archive status = %+v", a)
	}
}

func TestRecreateStrategyArchivesTheVersionItStopped(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	a := app("my-db", "my-db:1.0", 1)
	a.Deploy.Strategy = spec.StrategyRecreate
	h.deploy(a)
	h.rt.WriteLogs(h.replicaAt(1).ID, "checkpoint complete")
	a.Image = "my-db:1.1"
	h.deploy(a)

	entries := h.kept("my-db")
	if len(entries) != 1 || entries[0].Reason != api.LogReasonReplaced {
		t.Fatalf("entries = %+v", entries)
	}
	wantLines(t, "the stopped version", h.output("my-db", entries[0].ID), "checkpoint complete")
}

func TestOutputOfARunGoesWithItsRunWhenTheHistoryIsPruned(t *testing.T) {
	h := newHarness(t)
	dir := keepLogs(h)
	h.rt.PrintOnStart("my-api:1.0", "done")
	h.deploy(app("my-api", "my-api:1.0", 1))
	for i := 0; i < jobRunsKept+3; i++ {
		if _, err := h.engine.RunCommand(context.Background(), "my-api", []string{"rake"}); err != nil {
			t.Fatal(err)
		}
		h.engine.Wait()
	}

	entries, err := h.engine.LogArchive(context.Background(), "my-api", LogArchiveQuery{Kind: api.LogKindRun})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != jobRunsKept {
		t.Errorf("entries = %d, want one for each of the %d runs the history keeps", len(entries), jobRunsKept)
	}
	files, err := os.ReadDir(filepath.Join(dir, fmt.Sprint(appID(h, "my-api"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != jobRunsKept {
		t.Errorf("files = %d, want %d: a pruned run's file goes when its run does", len(files), jobRunsKept)
	}
}

func TestStartupKeepsTheOutputOfARunItInterruptsAndOfTheLeftoversItRemoves(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	keepLogs(h)
	d := h.deploy(app("my-api", "my-api:1.0", 1))

	// What an agent that died leaves: a job that was running, and a replica
	// of a deployment that is no longer the active one.
	run, err := h.store.CreateJobRun(ctx, store.JobRun{ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: "nightly", Kind: api.RunKindScheduled, Command: []string{"x"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cspec := containerSpec("my-api", d.ID, d.Sequence, 0)
	cspec.Job = &docker.JobSpec{Name: "nightly", RunID: run.ID}
	job, _, _ := h.rt.CreateContainer(ctx, cspec)
	h.rt.StartContainer(ctx, job)
	h.rt.WriteLogs(job, "half way through the report")
	leftover, _, _ := h.rt.CreateContainer(ctx, containerSpec("my-api", d.ID+50, 9, 1))
	h.rt.StartContainer(ctx, leftover)
	h.rt.WriteLogs(leftover, "still here")

	if err := h.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	h.engine.Wait()

	entries := h.kept("my-api")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the run and the leftover", entries)
	}
	for _, e := range entries {
		switch e.Kind {
		case api.LogKindRun:
			if e.Reason != string(api.RunInterrupted) || e.RunID == nil || *e.RunID != run.ID || e.Job != "nightly" {
				t.Errorf("the interrupted run: %+v", e)
			}
			wantLines(t, "the interrupted run", h.output("my-api", e.ID), "half way through the report")
		default:
			if e.Reason != api.LogReasonRemoved || e.DeploymentID != nil || e.Replica != 1 {
				t.Errorf("the leftover: %+v", e)
			}
			wantLines(t, "the leftover", h.output("my-api", e.ID), "still here")
		}
	}
}
