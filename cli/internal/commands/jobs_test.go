package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// fakeJobs is the jobs side of fakeAgent: canned jobs and runs, and what the
// CLI asked it to start.
type fakeJobs struct {
	mu   sync.Mutex
	jobs []api.Job
	runs map[int64]api.RunDetail // by id
	// polls are returned one per GET /runs/{id}, the last one repeating;
	// empty, GET answers from runs.
	polls   []api.RunDetail
	refuse  *api.Error // POST answers this error instead of starting a run
	started []string   // "run <json body>" or "job <name>"
}

func (j *fakeJobs) register(mux *http.ServeMux) {
	accept := func(w http.ResponseWriter, run api.RunDetail) {
		run.ID, run.Application, run.Kind, run.Status, run.StartedAt = 7, "my-api", api.RunKindManual, api.RunRunning, fixedNow
		respond(w, 202, run)
	}
	mux.HandleFunc("GET /api/v1/applications/{name}/jobs", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, j.jobs)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/runs", func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		out := []api.Run{}
		for _, run := range j.runs {
			if job := r.URL.Query().Get("job"); job == "" || job == run.Job {
				out = append(out, run.Run)
			}
		}
		sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		if len(j.polls) > 0 {
			next := j.polls[0]
			if len(j.polls) > 1 {
				j.polls = j.polls[1:]
			}
			respond(w, 200, next)
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		run, ok := j.runs[id]
		if !ok {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		respond(w, 200, run)
	})
	mux.HandleFunc("POST /api/v1/applications/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		j.mu.Lock()
		j.started = append(j.started, "run "+strings.TrimSpace(string(body)))
		j.mu.Unlock()
		if j.refuse != nil {
			respondError(w, 409, *j.refuse)
			return
		}
		var req api.RunRequest
		json.Unmarshal(body, &req)
		accept(w, api.RunDetail{Run: api.Run{Job: "run", Command: req.Command}})
	})
	mux.HandleFunc("POST /api/v1/applications/{name}/jobs/{job}/run", func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		j.started = append(j.started, "job "+r.PathValue("job"))
		j.mu.Unlock()
		if j.refuse != nil {
			respondError(w, 409, *j.refuse)
			return
		}
		accept(w, api.RunDetail{Run: api.Run{Job: r.PathValue("job"), Command: []string{"node", "report.js"}}})
	})
}

func finishedRun(id int64, job string, status api.RunStatus, exitCode *int, output string) api.RunDetail {
	started := fixedNow.Add(-2 * time.Hour)
	finished := started.Add(time.Minute)
	return api.RunDetail{Run: api.Run{
		ID: id, Application: "my-api", Job: job, Kind: api.RunKindManual, Command: []string{"x"},
		Status: status, ExitCode: exitCode, StartedAt: started, FinishedAt: &finished,
	}, Output: output}
}

func intPtr(n int) *int { return &n }

func TestRunPrintsTheOutputAndExitsWithTheCommandsCode(t *testing.T) {
	f := newFakeAgent(t)
	running := api.RunDetail{Run: api.Run{ID: 7, Application: "my-api", Job: "run", Status: api.RunRunning, StartedAt: fixedNow}}
	f.jobs.polls = []api.RunDetail{running, running, finishedRun(7, "run", api.RunFailed, intPtr(3), "migrating...\nERROR: relation exists")}

	out, _, err := f.run(writeConfig(t, validConfig), "run", "--", "rails", "db:migrate", "--trace")
	if ExitCode(err) != 3 || Render(err) != "" {
		t.Fatalf("err = %v (exit %d, rendered %q); want a silent exit 3", err, ExitCode(err), Render(err))
	}
	assertInOrder(t, out, []string{"migrating...", "ERROR: relation exists", "✗ failed (exit 3)"})
	if !strings.HasPrefix(out, "migrating...\nERROR: relation exists\n") {
		t.Errorf("output must be printed as it is:\n%s", out)
	}
	if len(f.jobs.started) != 1 || f.jobs.started[0] != `run {"command":["rails","db:migrate","--trace"]}` {
		t.Errorf("request: %v", f.jobs.started)
	}
	if f.requests[0] != "POST /api/v1/applications/my-api/run" {
		t.Errorf("the application comes from deploy.yaml: %v", f.requests)
	}
}

func TestRunSucceedsQuietlyAndNamesTheApplication(t *testing.T) {
	f := newFakeAgent(t)
	f.jobs.polls = []api.RunDetail{finishedRun(7, "run", api.RunSucceeded, intPtr(0), "done")}

	out, errOut, err := f.run(t.TempDir(), "run", "other-app", "--", "true")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "done\n" || errOut != "" {
		t.Errorf("out = %q, err = %q", out, errOut)
	}
	if f.requests[0] != "POST /api/v1/applications/other-app/run" {
		t.Errorf("requests: %v", f.requests)
	}
}

func TestRunNeedsTheCommandAfterADash(t *testing.T) {
	f := newFakeAgent(t)
	for _, args := range [][]string{{"run", "my-api"}, {"run", "my-api", "rails", "db:migrate"}, {"run", "my-api", "--"}} {
		_, _, err := f.run(t.TempDir(), args...)
		if err == nil || !strings.Contains(err.Error(), "--") {
			t.Errorf("%v: err = %v, want a hint about --", args, err)
		}
	}
	if _, _, err := f.run(t.TempDir(), "run", "a", "b", "--", "x"); err == nil || !strings.Contains(err.Error(), "one application") {
		t.Errorf("two names before --: %v", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("nothing valid was asked, yet the agent saw %v", f.requests)
	}
}

func TestRunOtherOutcomesExitNonZero(t *testing.T) {
	tests := map[string]struct {
		run  api.RunDetail
		want string
	}{
		"timed out":       {finishedRun(7, "run", api.RunTimedOut, nil, ""), "timed out"},
		"interrupted":     {finishedRun(7, "run", api.RunInterrupted, nil, ""), "interrupted by an agent restart"},
		"could not start": {finishedRun(7, "run", api.RunFailed, nil, "could not create the container: no such image"), "could not be started"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeAgent(t)
			f.jobs.polls = []api.RunDetail{tt.run}
			out, _, err := f.run(t.TempDir(), "run", "my-api", "--", "x")
			if !errors.Is(err, ErrReported) || ExitCode(err) != 1 {
				t.Errorf("err = %v", err)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output %q lacks %q", out, tt.want)
			}
		})
	}
}

func TestJobsTable(t *testing.T) {
	f := newFakeAgent(t)
	next := time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC)
	last := finishedRun(12, "nightly-report", api.RunFailed, intPtr(1), "")
	f.jobs.jobs = []api.Job{
		{Name: "nightly-report", Schedule: "0 3 * * *", Command: []string{"node", "report.js"}, LastRun: &last.Run, NextRunAt: &next},
		{Name: "cleanup", Schedule: "*/15 * * * *", Command: []string{"cleanup"}},
	}

	out, _, err := f.run(writeConfig(t, validConfig), "jobs")
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[0], "NEXT (UTC)") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[1], []string{"nightly-report", "0 3 * * *", "2h ago", "failed (exit 1)", "2026-03-02 03:00"})
	assertInOrder(t, lines[2], []string{"cleanup", "*/15 * * * *", "never", "-"})

	f.jobs.jobs = nil
	out, _, _ = f.run(t.TempDir(), "jobs", "my-api")
	if !strings.Contains(out, "no scheduled jobs") {
		t.Errorf("empty listing: %q", out)
	}
}

func TestJobsRunAndTheConflictAnswer(t *testing.T) {
	f := newFakeAgent(t)
	f.jobs.polls = []api.RunDetail{finishedRun(7, "nightly-report", api.RunSucceeded, intPtr(0), "report sent")}
	out, _, err := f.run(t.TempDir(), "jobs", "run", "my-api", "nightly-report")
	if err != nil || !strings.Contains(out, "report sent") {
		t.Fatalf("jobs run: %v\n%s", err, out)
	}
	if f.jobs.started[0] != "job nightly-report" {
		t.Errorf("started: %v", f.jobs.started)
	}

	f.jobs.refuse = &api.Error{Code: api.CodeJobAlreadyRunning, Message: "a run of this job is still in progress"}
	_, _, err = f.run(t.TempDir(), "jobs", "run", "my-api", "nightly-report")
	if got := Render(err); !strings.Contains(got, "still running from an earlier start") || !strings.Contains(got, "shipwick jobs") {
		t.Errorf("rendering: %q", got)
	}

	if _, _, err := f.run(t.TempDir(), "jobs", "run", "my-api", "Bad_Name"); err == nil {
		t.Error("an invalid job name must be rejected locally")
	}
}

func TestJobsLogsShowsTheLastRunOrAChosenOne(t *testing.T) {
	f := newFakeAgent(t)
	f.jobs.runs = map[int64]api.RunDetail{
		3: finishedRun(3, "nightly-report", api.RunSucceeded, intPtr(0), "older output"),
		9: finishedRun(9, "nightly-report", api.RunFailed, intPtr(2), "newest output"),
		5: finishedRun(5, "run", api.RunSucceeded, intPtr(0), "a command"),
	}

	out, _, err := f.run(t.TempDir(), "jobs", "logs", "my-api", "nightly-report")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"Run #9 of nightly-report: failed (exit 2), started 2h ago", "newest output"})
	if strings.Contains(out, "older output") {
		t.Errorf("only the last run: %s", out)
	}

	out, _, _ = f.run(t.TempDir(), "jobs", "logs", "my-api", "nightly-report", "--run", "3")
	if !strings.Contains(out, "older output") {
		t.Errorf("--run: %s", out)
	}

	out, _, err = f.run(t.TempDir(), "jobs", "logs", "my-api", "cleanup")
	if err != nil || !strings.Contains(out, "no runs of cleanup yet") {
		t.Errorf("no runs: %v %q", err, out)
	}
}

func TestInitTemplateDescribesJobs(t *testing.T) {
	content := renderConfig(initAnswers{Name: "my-api", Image: "img:1"})
	for _, want := range []string{"# pre_deploy:", "#   command: [\"dotnet\", \"Migrate.dll\"]", "# jobs:", "#     schedule: \"0 3 * * *\""} {
		if !strings.Contains(content, want) {
			t.Errorf("template lacks %q", want)
		}
	}
}
