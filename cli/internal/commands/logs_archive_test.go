package commands

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// fakeArchive is the log archive side of fakeAgent: canned entries, canned
// pages of a search by the cursor that asks for them, and what was asked.
type fakeArchive struct {
	mu sync.Mutex
	// enabled: the routes answer. An agent older than the archive answers
	// them like any endpoint it does not know.
	enabled bool
	entries []api.LogArchiveDetail // newest first
	pages   map[string]api.LogSearchResult
	asked   []string // the query of every search
}

func (a *fakeArchive) register(mux *http.ServeMux) {
	unknown := func(w http.ResponseWriter, r *http.Request) bool {
		if a.enabled && r.PathValue("name") != "my-api" {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return true
		}
		if a.enabled {
			return false
		}
		respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
		return true
	}
	mux.HandleFunc("GET /api/v1/applications/{name}/logs/archive", func(w http.ResponseWriter, r *http.Request) {
		if unknown(w, r) {
			return
		}
		q := r.URL.Query()
		out := []api.LogArchiveEntry{}
		for _, e := range a.entries {
			switch {
			case q.Get("kind") != "" && q.Get("kind") != e.Kind:
			case q.Get("replica") != "" && q.Get("replica") != strconv.Itoa(e.Replica):
			case q.Get("deployment") != "" && (e.DeploymentID == nil || q.Get("deployment") != strconv.FormatInt(*e.DeploymentID, 10)):
			case q.Get("run") != "" && (e.RunID == nil || q.Get("run") != strconv.FormatInt(*e.RunID, 10)):
			default:
				out = append(out, e.LogArchiveEntry)
			}
		}
		if limit, _ := strconv.Atoi(q.Get("limit")); limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/logs/archive/{id}", func(w http.ResponseWriter, r *http.Request) {
		if unknown(w, r) {
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		for _, e := range a.entries {
			if e.ID == id {
				if tail, _ := strconv.Atoi(r.URL.Query().Get("tail")); tail > 0 && len(e.Output) > tail {
					e.Output = e.Output[len(e.Output)-tail:]
				}
				respond(w, 200, e)
				return
			}
		}
		respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/logs/search", func(w http.ResponseWriter, r *http.Request) {
		if unknown(w, r) {
			return
		}
		a.mu.Lock()
		a.asked = append(a.asked, r.URL.RawQuery)
		a.mu.Unlock()
		page, ok := a.pages[r.URL.Query().Get("cursor")]
		if !ok {
			page = api.LogSearchResult{Lines: []api.LogMatch{}}
		}
		respond(w, 200, page)
	})
}

func ptr[T any](v T) *T { return &v }

// crashedEntry is replica 2 of deployment #3, which crashed five minutes ago.
func crashedEntry() api.LogArchiveDetail {
	ended := fixedNow.Add(-5 * time.Minute)
	return api.LogArchiveDetail{
		LogArchiveEntry: api.LogArchiveEntry{
			ID: 41, Application: "my-api", Kind: api.LogKindReplica, DeploymentID: ptr(int64(30)), Deployment: 3, Version: "1.4.2",
			Replica: 2, Container: "shipwick_my-api_3_2", Reason: api.LogReasonCrashed, ExitCode: ptr(1), EndedAt: ended,
			Lines: 2, Bytes: 52, StoredBytes: 70,
		},
		Output: []api.LogLine{
			{Replica: 2, Container: "shipwick_my-api_3_2", Stream: "stdout", Time: ended.Add(-2 * time.Second), Message: "connecting to the database"},
			{Replica: 2, Container: "shipwick_my-api_3_2", Stream: "stderr", Time: ended.Add(-time.Second), Message: "panic: connection refused"},
		},
	}
}

func archiveAgent(t *testing.T) *fakeAgent {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2}}}
	f.archive.enabled = true
	return f
}

func TestLogsPreviousShowsTheLastContainerThatEnded(t *testing.T) {
	f := archiveAgent(t)
	older := crashedEntry()
	older.ID, older.Output = 40, nil
	f.archive.entries = []api.LogArchiveDetail{crashedEntry(), older}

	for _, flag := range []string{"--previous", "-p"} {
		out, _, err := f.run(t.TempDir(), "logs", "my-api", flag)
		if err != nil {
			t.Fatalf("logs %s: %v", flag, Render(err))
		}
		want := "#3 replica 2 (1.4.2), shipwick_my-api_3_2: crashed (exit 1) 5m ago; 2 lines\n" +
			"connecting to the database\n" +
			"panic: connection refused\n"
		if out != want {
			t.Errorf("logs %s:\n%s\nwant:\n%s", flag, out, want)
		}
	}
	if last := f.requests[len(f.requests)-2]; !strings.Contains(last, "/logs/archive?") || !strings.Contains(last, "kind=replica") || !strings.Contains(last, "limit=1") {
		t.Errorf("the last ended replica should be asked for, got %s", last)
	}

	f.archive.entries = nil
	out, _, err := f.run(t.TempDir(), "logs", "my-api", "--previous")
	if err != nil || !strings.HasPrefix(out, "Nothing is kept of an earlier container of my-api") {
		t.Errorf("an empty archive: %q, %v", out, err)
	}
}

func TestLogsListShowsWhatIsKept(t *testing.T) {
	f := archiveAgent(t)
	run := api.LogArchiveDetail{LogArchiveEntry: api.LogArchiveEntry{
		ID: 44, Kind: api.LogKindRun, Job: "nightly-report", RunID: ptr(int64(12)), Container: "shipwick_my-api_job_nightly-report_12",
		Reason: string(api.RunFailed), ExitCode: ptr(2), EndedAt: fixedNow.Add(-time.Hour), Lines: 10000, Bytes: 3 << 20, Truncated: true,
	}}
	oom := crashedEntry()
	oom.ID, oom.Reason, oom.OOMKilled, oom.ExitCode, oom.EndedAt = 39, api.LogReasonOOMKilled, true, ptr(137), fixedNow.Add(-26*time.Hour)
	f.archive.entries = []api.LogArchiveDetail{run, crashedEntry(), oom}

	out, _, err := f.run(t.TempDir(), "logs", "my-api", "--list")
	if err != nil {
		t.Fatalf("logs --list: %v", Render(err))
	}
	assertInOrder(t, out, []string{
		"ID", "ENDED", "OUTPUT OF", "ENDED BECAUSE", "LINES", "SIZE",
		"44", "1h ago", "run 12 of nightly-report", "failed (exit 2)", "last 10000", "3 MB",
		"41", "5m ago", "#3 replica 2", "crashed (exit 1)", "2", "52 B",
		"39", "1d ago", "#3 replica 2", "out of memory",
		"Read one with: shipwick logs my-api --id <ID>",
	})

	out, _, _ = f.run(t.TempDir(), "logs", "my-api", "--list", "--run", "12")
	if !strings.Contains(out, "run 12 of nightly-report") || strings.Contains(out, "replica 2") {
		t.Errorf("--list --run 12:\n%s", out)
	}

	f.archive.entries = nil
	out, _, _ = f.run(t.TempDir(), "logs", "my-api", "--list")
	if !strings.HasPrefix(out, "Nothing is kept for my-api.") {
		t.Errorf("an empty archive:\n%s", out)
	}
}

func TestLogsIDShowsOneEntryAndSaysWhenItIsGone(t *testing.T) {
	f := archiveAgent(t)
	f.archive.entries = []api.LogArchiveDetail{crashedEntry()}

	out, _, err := f.run(t.TempDir(), "logs", "my-api", "--id", "41", "-n", "1", "-t")
	if err != nil {
		t.Fatalf("logs --id: %v", Render(err))
	}
	if !strings.Contains(out, "2 lines, the last 1 shown") || strings.Contains(out, "connecting to the database") ||
		!strings.Contains(out, fixedNow.Add(-5*time.Minute-time.Second).Local().Format("2006-01-02 15:04:05.000")+" panic: connection refused") {
		t.Errorf("logs --id 41 -n 1 -t:\n%s", out)
	}

	_, _, err = f.run(t.TempDir(), "logs", "my-api", "--id", "7")
	want := "Error: my-api has no archived output with id 7: it may have aged out\n\nSee what is kept with: shipwick logs my-api --list"
	if got := Render(err); got != want {
		t.Errorf("an entry that is gone:\n%s\nwant:\n%s", got, want)
	}
	_, _, err = f.run(t.TempDir(), "logs", "ghost", "--id", "41")
	if got := Render(err); !strings.Contains(got, "does not know that application") {
		t.Errorf("an unknown application: %s", got)
	}
}

func TestLogsSearchFollowsThePagesAndPrintsOldestFirstUnderItsSources(t *testing.T) {
	f := archiveAgent(t)
	f.history = []api.Deployment{{ID: 40, Sequence: 4}, {ID: 30, Sequence: 3}}
	at := fixedNow.Add(-time.Hour)
	line := func(replica int, container, message string, offset time.Duration) api.LogLine {
		return api.LogLine{Replica: replica, Container: container, Stream: "stdout", Time: at.Add(offset), Message: message}
	}
	f.archive.pages = map[string]api.LogSearchResult{
		"": {Next: "a41", Lines: []api.LogMatch{
			{LogLine: line(1, "shipwick_my-api_4_1", "timeout talking to redis", 50*time.Minute), DeploymentID: ptr(int64(40)), Deployment: 4},
		}},
		// A page that read its fill without finding anything.
		"a41": {Next: "a41.7", Lines: []api.LogMatch{}},
		"a41.7": {Lines: []api.LogMatch{
			{LogLine: line(2, "shipwick_my-api_3_2", "Timeout talking to postgres", 2*time.Minute), ArchiveID: ptr(int64(41)), DeploymentID: ptr(int64(30)), Deployment: 3},
			{LogLine: line(2, "shipwick_my-api_3_2", "timeout talking to redis", time.Minute), ArchiveID: ptr(int64(41)), DeploymentID: ptr(int64(30)), Deployment: 3},
			{LogLine: api.LogLine{Container: "shipwick_my-api_job_sync_9", Time: at, Message: "sync: timeout"}, ArchiveID: ptr(int64(38)), RunID: ptr(int64(9)), Job: "sync"},
		}},
	}

	out, _, err := f.run(t.TempDir(), "logs", "my-api", "--search", "Timeout", "--since", "2h", "--deployment", "3")
	if err != nil {
		t.Fatalf("logs --search: %v", Render(err))
	}
	stamp := func(offset time.Duration) string {
		return at.Add(offset).Local().Format("2006-01-02 15:04:05.000") + " "
	}
	want := "== run 9 of sync, shipwick_my-api_job_sync_9, kept as 38 ==\n" +
		stamp(0) + "sync: timeout\n" +
		"== #3 replica 2, shipwick_my-api_3_2, kept as 41 ==\n" +
		stamp(time.Minute) + "timeout talking to redis\n" +
		stamp(2*time.Minute) + "Timeout talking to postgres\n" +
		"== #4 replica 1, shipwick_my-api_4_1 ==\n" +
		stamp(50*time.Minute) + "timeout talking to redis\n"
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if len(f.archive.asked) != 3 {
		t.Fatalf("searches = %q, want one per page", f.archive.asked)
	}
	first := f.archive.asked[0]
	for _, param := range []string{"q=Timeout", "deployment=30", "limit=100", "since=" + url.QueryEscape(fixedNow.Add(-2*time.Hour).Format(time.RFC3339))} {
		if !strings.Contains(first, param) {
			t.Errorf("the first search %q lacks %s", first, param)
		}
	}
	if !strings.Contains(f.archive.asked[2], "cursor=a41.7") || !strings.Contains(f.archive.asked[2], "limit=99") {
		t.Errorf("the last search: %q", f.archive.asked[2])
	}

	// Without --search no time is printed unless asked for, and -n is where
	// the newest lines end.
	f.archive.asked = nil
	out, errOut, err := f.run(t.TempDir(), "logs", "my-api", "--since", "2026-03-01", "-n", "1")
	if err != nil {
		t.Fatal(Render(err))
	}
	if out != "== #4 replica 1, shipwick_my-api_4_1 ==\ntimeout talking to redis\n" {
		t.Errorf("--since alone, -n 1:\n%s", out)
	}
	if !strings.Contains(errOut, "These are the newest 1 line that match; there are older ones") || !strings.Contains(errOut, "--until "+at.Add(50*time.Minute).Format(time.RFC3339)) {
		t.Errorf("the note about older lines: %q", errOut)
	}
	if len(f.archive.asked) != 1 || strings.Contains(f.archive.asked[0], "q=") || !strings.Contains(f.archive.asked[0], "since=2026-03-01T00%3A00%3A00Z") {
		t.Errorf("searches: %q", f.archive.asked)
	}

	f.archive.pages = nil
	out, _, _ = f.run(t.TempDir(), "logs", "my-api", "--search", "nothing like it")
	if out != "No lines match, in what is kept or in the running containers.\n" {
		t.Errorf("no match: %q", out)
	}
	_, _, err = f.run(t.TempDir(), "logs", "my-api", "--deployment", "9")
	if got := Render(err); got != "Error: my-api has no deployment #9\n\nSee its history with: shipwick status my-api" {
		t.Errorf("an unknown deployment: %s", got)
	}
}

func TestLogsRunShowsTheArchivedOutputOrTheTailInTheRunsRecord(t *testing.T) {
	f := archiveAgent(t)
	ended := fixedNow.Add(-time.Hour)
	f.archive.entries = []api.LogArchiveDetail{{
		LogArchiveEntry: api.LogArchiveEntry{ID: 44, Kind: api.LogKindRun, Job: "nightly-report", RunID: ptr(int64(12)),
			Container: "shipwick_my-api_job_nightly-report_12", Reason: string(api.RunSucceeded), ExitCode: ptr(0), EndedAt: ended, Lines: 1},
		Output: []api.LogLine{{Container: "shipwick_my-api_job_nightly-report_12", Time: ended, Message: "report written"}},
	}}
	out, _, err := f.run(t.TempDir(), "logs", "my-api", "--run", "12")
	if err != nil {
		t.Fatal(Render(err))
	}
	if out != "Run 12 of nightly-report, shipwick_my-api_job_nightly-report_12: succeeded 1h ago; 1 line\nreport written\n" {
		t.Errorf("an archived run:\n%s", out)
	}

	// An agent that keeps no archive still has the run's tail.
	f.archive.enabled = false
	f.jobs.runs = map[int64]api.RunDetail{12: {Run: api.Run{ID: 12, Job: "nightly-report", Status: api.RunSucceeded, StartedAt: ended}, Output: "the tail"}}
	out, _, err = f.run(t.TempDir(), "logs", "my-api", "--run", "12")
	if err != nil || out != "Run #12 of nightly-report: succeeded, started 1h ago\nthe tail\n" {
		t.Errorf("the run's own record: %q, %v", out, err)
	}
	_, _, err = f.run(t.TempDir(), "logs", "my-api", "--run", "99")
	if got := Render(err); !strings.HasPrefix(got, "Error: my-api has no run 99") {
		t.Errorf("an unknown run: %s", got)
	}
}

func TestLogsArchiveFlagsSayWhatAnOlderAgentLacksAndWhichFlagsDoNotGoTogether(t *testing.T) {
	f := archiveAgent(t)
	f.archive.enabled = false
	want := "Error: the agent is older than this shipwick and keeps no log archive: it shows the output of running containers only\n\nCompare versions with: shipwick server status"
	for _, args := range [][]string{{"--previous"}, {"--list"}, {"--id", "3"}, {"--search", "x"}, {"--since", "1h"}} {
		_, _, err := f.run(t.TempDir(), append([]string{"logs", "my-api"}, args...)...)
		if got := Render(err); got != want {
			t.Errorf("%v against an older agent:\n%s", args, got)
		}
	}

	f.archive.enabled = true
	before := len(f.requests)
	for args, want := range map[string]string{
		"--previous -f":            "--follow streams the running containers",
		"--id 3 --replica 1":       "--id shows one entry",
		"--list --search x":        "--list lists entries",
		"--previous --search x":    "--previous shows the last ended container",
		"--id -3":                  "take a positive number",
		"--since yesterday":        "--since: invalid time",
		"--since 2h --until 3h":    "--until is before --since",
		"--search x --tail 6000":   "--tail must be between 1 and 5000",
		"--search " + longNeedle(): "--search takes at most 256 bytes",
	} {
		_, _, err := f.run(t.TempDir(), append([]string{"logs", "my-api"}, strings.Fields(args)...)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("logs %s: %v, want an error with %q", args, err, want)
		}
	}
	if len(f.requests) != before {
		t.Errorf("flags that do not go together reached the agent: %q", f.requests[before:])
	}
}

func longNeedle() string { return strings.Repeat("x", 257) }

func TestStatusEndsWithTheCommandThatShowsWhyAReplicaDied(t *testing.T) {
	f := archiveAgent(t)
	started := fixedNow.Add(-2 * time.Hour)
	f.app = api.ApplicationDetail{
		Application:      api.Application{Name: "my-api", Status: api.AppCrashLoop, Replicas: api.ReplicaCount{Desired: 2, Running: 1, Healthy: 1}},
		Spec:             &spec.App{},
		ActiveDeployment: &api.Deployment{ID: 30, Sequence: 3, Version: "1.4.2", StartedAt: started},
		Containers: []api.Container{
			{Replica: 1, Name: "shipwick_my-api_3_1", State: "running", StartedAt: &started},
			{Replica: 2, Name: "shipwick_my-api_3_2", State: "exited", ExitCode: 1, Restarts: 7, CrashLoop: true},
		},
	}
	f.history = []api.Deployment{
		{ID: 40, Sequence: 4, Version: "1.5.0", Status: api.StatusFailed, StartedAt: started},
		{ID: 30, Sequence: 3, Version: "1.4.2", Status: api.StatusActive, StartedAt: started},
	}
	replaced := crashedEntry()
	replaced.ID, replaced.Reason = 43, api.LogReasonReplaced
	ofFailed := crashedEntry()
	ofFailed.ID, ofFailed.DeploymentID, ofFailed.Deployment, ofFailed.Reason, ofFailed.Container = 42, ptr(int64(40)), 4, api.LogReasonDeploymentFailed, "shipwick_my-api_4_1"
	f.archive.entries = []api.LogArchiveDetail{replaced, ofFailed, crashedEntry()}

	out, _, err := f.run(t.TempDir(), "status", "my-api")
	if err != nil {
		t.Fatal(Render(err))
	}
	assertInOrder(t, out, []string{
		"7 (crash loop)",
		"Replica 2's last output before it exited with code 1, 5m ago:", "shipwick logs my-api --id 41",
		"What deployment #4 printed before it failed:", "shipwick logs my-api --deployment 4",
	})

	// Nothing died: nothing is asked, and nothing is said.
	f.app.Containers = f.app.Containers[:1]
	f.history = f.history[1:]
	before := len(f.requests)
	out, _, _ = f.run(t.TempDir(), "status", "my-api")
	if strings.Contains(out, "shipwick logs") {
		t.Errorf("a healthy application gets no hint:\n%s", out)
	}
	for _, r := range f.requests[before:] {
		if strings.Contains(r, "/logs/archive") {
			t.Errorf("the archive was asked about a healthy application: %s", r)
		}
	}

	// An agent without an archive: the status is what it always was.
	f.archive.enabled = false
	f.app.Containers = append(f.app.Containers, api.Container{Replica: 2, Name: "shipwick_my-api_3_2", State: "exited", ExitCode: 1, Restarts: 7, CrashLoop: true})
	out, _, err = f.run(t.TempDir(), "status", "my-api")
	if err != nil || strings.Contains(out, "shipwick logs") {
		t.Errorf("against an older agent: %v\n%s", err, out)
	}
}

func TestServerStatusShowsWhatTheLogArchiveHolds(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", LogArchive: &api.LogArchiveStatus{Enabled: true, Entries: 278, Bytes: 64 << 20, MaxBytes: 1 << 30, RetentionDays: 14}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"Log archive", "64 MB of 1 GB, 278 entries, kept 14 days"})

	f.server.LogArchive = &api.LogArchiveStatus{}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	assertInOrder(t, out, []string{"Log archive", "off", "SHIPWICK_LOG_RETENTION_SIZE is 0"})

	// An agent older than the archive says nothing about one.
	f.server.LogArchive = nil
	out, _, _ = f.run(t.TempDir(), "server", "status")
	if strings.Contains(out, "Log archive") {
		t.Errorf("an older agent:\n%s", out)
	}
}
