package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const jobsConfig = validConfig + `
pre_deploy:
  command: ["dotnet", "Migrate.dll"]
jobs:
  - name: nightly-report
    schedule: "0 3 * * *"
    command: ["node", "report.js"]
    timeout: 30m
`

func TestJobsEndpoints(t *testing.T) {
	f := newFixture(t)
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", jobsConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()

	status, body := f.do("GET", "/api/v1/applications/my-api/jobs", "")
	if status != http.StatusOK {
		t.Fatalf("jobs: %d %s", status, body)
	}
	jobs := decode[[]api.Job](t, body)
	if len(jobs) != 1 || jobs[0].Name != "nightly-report" || jobs[0].Schedule != "0 3 * * *" || jobs[0].LastRun != nil || jobs[0].NextRunAt == nil {
		t.Errorf("jobs: %+v", jobs)
	}
	if !strings.Contains(string(body), `"timeout":"30m0s"`) || !strings.Contains(string(body), `"last_run":null`) {
		t.Errorf("wire format: %s", body)
	}

	// The hook's run is in the history, with its output.
	status, body = f.do("GET", "/api/v1/applications/my-api/runs?job=pre-deploy", "")
	runs := decode[[]api.Run](t, body)
	if status != http.StatusOK || len(runs) != 1 || runs[0].Kind != api.RunKindHook || runs[0].Status != api.RunSucceeded {
		t.Fatalf("runs: %d %+v", status, runs)
	}
	if strings.Contains(string(body), `"output"`) {
		t.Error("the listing must not carry outputs")
	}
	status, body = f.do("GET", "/api/v1/applications/my-api/runs/1", "")
	detail := decode[api.RunDetail](t, body)
	if status != http.StatusOK || detail.ID != 1 || detail.Job != "pre-deploy" || !strings.Contains(detail.Output, "log line from") {
		t.Errorf("run detail: %d %+v", status, detail)
	}

	// Start the job by hand and follow it to its end.
	f.rt.JobExits["nightly-report"] = 2
	status, body = f.do("POST", "/api/v1/applications/my-api/jobs/nightly-report/run", "")
	started := decode[api.RunDetail](t, body)
	if status != http.StatusAccepted || started.Status != api.RunRunning || started.Kind != api.RunKindManual || started.Command[1] != "report.js" {
		t.Fatalf("run job: %d %+v", status, started)
	}
	f.engine.Wait()
	_, body = f.do("GET", "/api/v1/applications/my-api/runs/2", "")
	if finished := decode[api.RunDetail](t, body); finished.Status != api.RunFailed || finished.ExitCode == nil || *finished.ExitCode != 2 || finished.FinishedAt == nil {
		t.Errorf("finished run: %+v", finished)
	}
	_, body = f.do("GET", "/api/v1/applications/my-api/jobs", "")
	if jobs = decode[[]api.Job](t, body); jobs[0].LastRun == nil || jobs[0].LastRun.ID != 2 {
		t.Errorf("last run: %+v", jobs[0].LastRun)
	}
	_, body = f.do("GET", "/api/v1/applications/my-api/events", "")
	if events := decode[[]api.Event](t, body); len(events) != 1 || events[0].Type != api.EventJob || events[0].Message != "Job nightly-report failed (exit 2)" {
		t.Errorf("events: %+v", events)
	}

	// A one-off command.
	status, body = f.do("POST", "/api/v1/applications/my-api/run", `{"command": ["rails", "db:seed"]}`)
	run := decode[api.RunDetail](t, body)
	if status != http.StatusAccepted || run.Job != "run" || run.Command[0] != "rails" {
		t.Fatalf("run command: %d %s", status, body)
	}
	f.engine.Wait()
	if status, _ := f.do("GET", "/api/v1/applications/my-api/runs?job=run", ""); status != http.StatusOK {
		t.Errorf("runs?job=run: %d", status)
	}
	if n := len(f.rt.JobContainers()); n != 0 {
		t.Errorf("%d job containers left", n)
	}
}

func TestJobsEndpointErrors(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", jobsConfig)
	f.engine.Wait()

	tests := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", "/api/v1/applications/ghost/jobs", "", 404, api.CodeNotFound},
		{"GET", "/api/v1/applications/my-api/runs/999", "", 404, api.CodeNotFound},
		{"GET", "/api/v1/applications/my-api/runs/abc", "", 400, api.CodeInvalidRequest},
		{"GET", "/api/v1/applications/my-api/runs?job=Not_Valid", "", 400, api.CodeInvalidRequest},
		{"GET", "/api/v1/applications/my-api/runs?limit=0", "", 400, api.CodeInvalidRequest},
		{"POST", "/api/v1/applications/my-api/jobs/no-such-job/run", "", 404, api.CodeNotFound},
		{"POST", "/api/v1/applications/my-api/jobs/Bad_Name/run", "", 400, api.CodeInvalidRequest},
		{"POST", "/api/v1/applications/my-api/run", "", 400, api.CodeInvalidRequest},
		{"POST", "/api/v1/applications/my-api/run", `{"command": []}`, 400, api.CodeInvalidRequest},
		{"POST", "/api/v1/applications/my-api/run", `{"command": ["x"], "shell": true}`, 400, api.CodeInvalidRequest},
		{"POST", "/api/v1/applications/my-api/run", `{"command": "ls -la"}`, 400, api.CodeInvalidRequest},
	}
	for _, tt := range tests {
		status, body := f.do(tt.method, tt.path, tt.body)
		if status != tt.status {
			t.Errorf("%s %s: status = %d, want %d (%s)", tt.method, tt.path, status, tt.status, body)
			continue
		}
		if e := decodeError(t, body); e.Code != tt.code {
			t.Errorf("%s %s: code = %q, want %q", tt.method, tt.path, e.Code, tt.code)
		}
	}

	// A job that is still running refuses a second start.
	f.rt.HoldJobs = true
	if status, _ := f.do("POST", "/api/v1/applications/my-api/jobs/nightly-report/run", ""); status != http.StatusAccepted {
		t.Fatalf("first start: %d", status)
	}
	status, body := f.do("POST", "/api/v1/applications/my-api/jobs/nightly-report/run", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeJobAlreadyRunning {
		t.Errorf("second start: %d %s", status, body)
	}
	f.rt.ReleaseJob("nightly-report", 0)
	f.engine.Wait()

	// No active deployment: nothing to run from.
	f.rt.PullErr = errors.New("manifest unknown")
	f.do("POST", "/api/v1/applications/fresh/deploy", "name: fresh\nimage: fresh:1.0\n")
	f.engine.Wait()
	status, body = f.do("POST", "/api/v1/applications/fresh/run", `{"command": ["x"]}`)
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeNotDeployed {
		t.Errorf("run on undeployed app: %d %s", status, body)
	}
	if status, _ := f.do("GET", "/api/v1/applications/fresh/jobs", ""); status != http.StatusConflict {
		t.Errorf("jobs of undeployed app: %d", status)
	}
}

func TestJobEndpointsNeedTheDeployRole(t *testing.T) {
	f := newFixture(t)
	// The read endpoints and the deploy endpoints exist and are protected;
	// role enforcement itself belongs to the token feature.
	for _, path := range []string{"/api/v1/applications/my-api/jobs", "/api/v1/applications/my-api/runs", "/api/v1/applications/my-api/runs/1"} {
		if status, _ := f.doWithAuth("GET", path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", path, status)
		}
	}
	for _, path := range []string{"/api/v1/applications/my-api/run", "/api/v1/applications/my-api/jobs/x/run"} {
		if status, _ := f.doWithAuth("POST", path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", path, status)
		}
	}
}
