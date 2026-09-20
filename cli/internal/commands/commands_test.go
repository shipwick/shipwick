package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const testToken = "test-token-0123456789abcdef"

// fakeAgent serves canned API responses. The CLI cannot import the real agent
// (Go's internal-package rule), and should not need to: the contract between
// the two is pkg/api.
type fakeAgent struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// deploymentPolls are returned one per GET /deployments/1; the last one repeats.
	deploymentPolls []api.DeploymentDetail
	deployStatus    int // non-zero: POST deploy fails with this status and deployError
	deployError     api.Error
	app             api.ApplicationDetail
	history         []api.Deployment
	logs            []api.LogLine
	events          []api.Event
	server          api.Server
	metrics         *api.Metrics // nil: the agent answers 404

	deployBodies []string
	actionBodies map[string]string // "redeploy" | "rollback" → the JSON body received
	requests     []string
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{t: t, actionBodies: map[string]string{}}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, api.Health{Status: "ok", Version: "1.2.3"})
	})
	mux.HandleFunc("GET /api/v1/server", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, f.server)
	})
	mux.HandleFunc("GET /api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, []api.Application{f.app.Application})
	})
	mux.HandleFunc("GET /api/v1/applications/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != f.app.Name {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		respond(w, 200, f.app)
	})
	mux.HandleFunc("POST /api/v1/applications/{name}/deploy", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.deployBodies = append(f.deployBodies, string(body))
		f.mu.Unlock()
		if f.deployStatus != 0 {
			respondError(w, f.deployStatus, f.deployError)
			return
		}
		respond(w, 202, api.Deployment{ID: 1, Application: r.PathValue("name"), Sequence: 3, Status: api.StatusPending})
	})
	for _, action := range []string{"redeploy", "rollback"} {
		mux.HandleFunc("POST /api/v1/applications/{name}/"+action, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.actionBodies[action] = string(body)
			f.mu.Unlock()
			if f.deployStatus != 0 {
				respondError(w, f.deployStatus, f.deployError)
				return
			}
			respond(w, 202, api.Deployment{ID: 1, Application: r.PathValue("name"), Sequence: 4, Status: api.StatusPending, Kind: action})
		})
	}
	mux.HandleFunc("GET /api/v1/deployments/1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		next := f.deploymentPolls[0]
		if len(f.deploymentPolls) > 1 {
			f.deploymentPolls = f.deploymentPolls[1:]
		}
		respond(w, 200, next)
	})
	mux.HandleFunc("GET /api/v1/deployments", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, f.history)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, f.logs)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/metrics", func(w http.ResponseWriter, r *http.Request) {
		if f.metrics == nil {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "no such endpoint"})
			return
		}
		respond(w, 200, *f.metrics)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/events", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, f.events)
	})
	mux.HandleFunc("DELETE /api/v1/applications/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func respond[T any](w http.ResponseWriter, status int, data T) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(api.Response[T]{Data: data})
}

func respondError(w http.ResponseWriter, status int, e api.Error) {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(api.ErrorResponse{Error: e})
}

var fixedNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// run executes deployctl with args inside dir and returns stdout, stderr and the error.
func (f *fakeAgent) run(dir string, args ...string) (string, string, error) {
	f.t.Helper()
	f.t.Chdir(dir)

	var out, errOut bytes.Buffer
	env := map[string]string{
		cliconfig.EnvURL:    f.srv.URL,
		cliconfig.EnvToken:  testToken,
		cliconfig.EnvConfig: filepath.Join(f.t.TempDir(), "config.yaml"),
	}
	c, root := newRoot(Options{
		In:     strings.NewReader(""),
		Out:    &out,
		Err:    &errOut,
		Getenv: func(k string) string { return env[k] },
	})
	c.pollInterval = time.Millisecond
	c.now = func() time.Time { return fixedNow }
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const validConfig = "name: my-api\nimage: ghcr.io/company/my-api:1.4.2\nport: 8080\ndomain: api.example.com\nreplicas: 2\n"

func event(id int64, typ, level, msg string) api.Event {
	return api.Event{ID: id, Type: typ, Level: level, Message: msg}
}

func TestDeploySuccess(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	events := []api.Event{
		event(1, api.EventState, api.LevelInfo, "BUILDING"),
		event(2, api.EventStep, api.LevelInfo, "Pulled image ghcr.io/company/my-api:1.4.2"),
		event(3, api.EventStep, api.LevelInfo, "Created 2 containers"),
		event(4, api.EventState, api.LevelInfo, "ACTIVE"),
		event(5, api.EventStep, api.LevelInfo, "Deployment successful"),
	}
	f.deploymentPolls = []api.DeploymentDetail{
		{Deployment: api.Deployment{ID: 1, Status: api.StatusBuilding}, Events: events[:2]},
		// ACTIVE but not completed: the CLI must keep waiting.
		{Deployment: api.Deployment{ID: 1, Status: api.StatusActive}, Events: events[:4]},
		{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done}, Events: events,
			Spec: spec.App{Domain: "api.example.com"}},
	}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}}}

	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	want := []string{
		"Deploying my-api...",
		"✓ Validated deploy.yaml",
		"✓ Pulled image ghcr.io/company/my-api:1.4.2",
		"✓ Created 2 containers",
		"✓ Deployment successful",
		"my-api 1.4.2",
		"2/2 replicas healthy",
		"https://api.example.com",
	}
	assertInOrder(t, out, want)
	if strings.Count(out, "Pulled image") != 1 {
		t.Errorf("events must be echoed once, not on every poll:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "…") {
		t.Errorf("piped output must carry no colors and no progress lines:\n%q", out)
	}
	if f.deployBodies[0] != validConfig {
		t.Errorf("the agent should receive deploy.yaml verbatim, got:\n%s", f.deployBodies[0])
	}
}

func TestDeployFailure(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusFailed, CompletedAt: &done,
			Error: "replica 1 exited with code 1 shortly after start"},
		Events: []api.Event{
			event(1, api.EventStep, api.LevelInfo, "Pulled image x"),
			event(2, api.EventLog, api.LevelError, "Last output of replica 1:\npanic: DATABASE_URL is not set"),
			event(3, api.EventState, api.LevelError, "FAILED: replica 1 exited with code 1 shortly after start"),
		},
	}}
	f.app = api.ApplicationDetail{
		Application:      api.Application{Name: "my-api", Status: api.AppHealthy},
		ActiveDeployment: &api.Deployment{Version: "1.4.1"},
	}

	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v, want ErrReported (the failure is already on screen)", err)
	}
	if Render(err) != "" {
		t.Error("a reported error must not be printed a second time")
	}
	assertInOrder(t, out, []string{
		"✓ Pulled image x",
		"✗ Deployment failed",
		"replica 1 exited with code 1 shortly after start",
		"panic: DATABASE_URL is not set",
		"my-api is still running 1.4.1",
	})
}

func TestDeployImageOverride(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &done}}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	dir := writeConfig(t, validConfig)

	if out, _, err := f.run(dir, "deploy", "--image", "ghcr.io/company/my-api:sha-abc123"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	sent, err := spec.Parse([]byte(f.deployBodies[0]))
	if err != nil {
		t.Fatalf("the agent must still receive a valid deploy.yaml: %v", err)
	}
	if sent.Image != "ghcr.io/company/my-api:sha-abc123" || sent.Domain != "api.example.com" || sent.Replicas != 2 {
		t.Errorf("override should change the image and nothing else: %+v", sent)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, DefaultFile))
	if string(onDisk) != validConfig {
		t.Error("--image must not modify the file on disk")
	}
}

func TestDeployNoWait(t *testing.T) {
	f := newFakeAgent(t)
	out, _, err := f.run(writeConfig(t, validConfig), "deploy", "--no-wait")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(out, "Deployment #3 started") {
		t.Errorf("unexpected output:\n%s", out)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/deployments/") {
			t.Errorf("--no-wait must not poll, saw %s", r)
		}
	}
}

func TestDeployInvalidConfigNeverReachesTheAgent(t *testing.T) {
	f := newFakeAgent(t)
	_, _, err := f.run(writeConfig(t, "name: my-api\nimage: nginx\nresources:\n  memory: abc\n"), "deploy")

	want := "invalid deploy.yaml\n\nresources.memory:\n  invalid value \"abc\"\n  expected: 128mb, 512mb, 1gb, ...\n"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if len(f.requests) != 0 {
		t.Errorf("no request should be made for a config that fails local validation, saw %v", f.requests)
	}
}

func TestAgentSideValidationRendersLikeLocalValidation(t *testing.T) {
	f := newFakeAgent(t)
	f.deployStatus = 400
	f.deployError = api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{
		"fields": []map[string]string{{"field": "image", "message": "is not allowed on this server", "expected": ""}},
	}}
	_, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if got := Render(err); !strings.HasPrefix(got, "invalid deploy.yaml\n\nimage:\n  is not allowed on this server") {
		t.Errorf("unexpected rendering:\n%s", got)
	}
}

func TestDeployConflictAndMissingFile(t *testing.T) {
	f := newFakeAgent(t)
	f.deployStatus = 409
	f.deployError = api.Error{Code: api.CodeDeploymentInProgress, Message: "busy"}
	_, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if got := Render(err); !strings.Contains(got, "already in progress") {
		t.Errorf("unexpected rendering: %s", got)
	}

	_, _, err = f.run(t.TempDir(), "deploy")
	if got := Render(err); !strings.Contains(got, "deploy.yaml not found") || !strings.Contains(got, "deployctl init") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestStatus(t *testing.T) {
	f := newFakeAgent(t)
	started := fixedNow.Add(-2 * time.Hour)
	f.app = api.ApplicationDetail{
		Application: api.Application{Name: "my-api", Status: api.AppCrashLoop, Domain: "api.example.com",
			Replicas: api.ReplicaCount{Desired: 2, Running: 1, Healthy: 1}},
		Spec: &spec.App{Resources: spec.Resources{CPU: 2, MemoryBytes: 1 << 30},
			Health: &spec.Health{Path: "/health", Interval: spec.Duration(10 * time.Second)}},
		ActiveDeployment: &api.Deployment{Sequence: 3, Version: "1.4.2", Image: "ghcr.io/company/my-api:1.4.2", StartedAt: started, CompletedAt: &started},
		Containers: []api.Container{
			{Replica: 1, Name: "shipwick_my-api_3_1", State: "running", StartedAt: &started, Health: api.HealthHealthy},
			{Replica: 2, Name: "shipwick_my-api_3_2", State: "exited", ExitCode: 137, OOMKilled: true, Health: api.HealthUnhealthy, Restarts: 7, CrashLoop: true},
		},
	}
	f.history = []api.Deployment{
		{Sequence: 3, Version: "1.4.2", Status: api.StatusActive, StartedAt: started},
		{Sequence: 2, Version: "1.4.1", Status: api.StatusFailed, StartedAt: started.Add(-24 * time.Hour), Error: "replica 1 exited with code 1"},
	}

	f.events = []api.Event{{Level: api.LevelError, Type: api.EventSupervisor, Message: "Replica 2 is crash-looping", CreatedAt: fixedNow.Add(-5 * time.Minute)}}

	// By explicit name, and by the deploy.yaml in the current directory.
	for _, args := range [][]string{{"status", "my-api"}, {"status"}} {
		out, _, err := f.run(writeConfig(t, validConfig), args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		assertInOrder(t, out, []string{
			"my-api", "● CRASH_LOOP",
			"1.4.2", "(deployment #3, 2h ago)",
			"https://api.example.com",
			"1/2 healthy",
			"GET /health every 10s",
			"2 CPU, 1 GB",
			"shipwick_my-api_3_1", "running", "healthy", "0", "2h ago",
			"shipwick_my-api_3_2", "out of memory", "unhealthy", "7 (crash loop)",
			"#3", "ACTIVE",
			"#2", "FAILED", "1d ago", "replica 1 exited with code 1",
			"5m ago", "Replica 2 is crash-looping",
		})
	}
}

func TestStatusUnknownApplication(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	_, _, err := f.run(t.TempDir(), "status", "ghost")
	if got := Render(err); !strings.Contains(got, "does not know that application") {
		t.Errorf("unexpected rendering: %s", got)
	}

	_, _, err = f.run(t.TempDir(), "status", "Not_A_Valid_Name")
	if err == nil || len(f.requests) != 1 {
		t.Errorf("an invalid name must be rejected locally; err=%v requests=%v", err, f.requests)
	}
}

func TestPs(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Application = api.Application{Name: "my-api", Status: api.AppHealthy, Version: "1.4.2", Deploying: true,
		Replicas: api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}, UpdatedAt: fixedNow.Add(-5 * time.Minute)}
	out, _, err := f.run(t.TempDir(), "ps")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[1], []string{"my-api", "HEALTHY (deploying)", "1.4.2", "2/2", "-", "5m ago"})
	// Columns line up: the STATUS header starts where the status value starts.
	if strings.Index(lines[0], "STATUS") != strings.Index(lines[1], "HEALTHY") {
		t.Errorf("misaligned columns:\n%s", out)
	}
}

func TestLogs(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2}}}
	f.logs = []api.LogLine{
		{Replica: 1, Message: "listening on :8080"},
		{Replica: 2, Message: "listening on :8080"},
	}
	out, _, err := f.run(t.TempDir(), "logs", "my-api", "-n", "10")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if out != "[1] listening on :8080\n[2] listening on :8080\n" {
		t.Errorf("unexpected output:\n%q", out)
	}

	// With a single replica the prefix is noise.
	f.app.Replicas.Desired = 1
	out, _, _ = f.run(t.TempDir(), "logs", "my-api")
	if !strings.HasPrefix(out, "listening on :8080\n") {
		t.Errorf("unexpected output:\n%q", out)
	}

	if _, _, err := f.run(t.TempDir(), "logs", "my-api", "-n", "0"); err == nil {
		t.Error("--tail 0 without --follow should be rejected")
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	f := newFakeAgent(t)
	if _, _, err := f.run(t.TempDir(), "delete", "my-api"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want a refusal pointing at --yes", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("nothing may be sent without confirmation, saw %v", f.requests)
	}

	out, _, err := f.run(t.TempDir(), "delete", "my-api", "--yes")
	if err != nil || !strings.Contains(out, "Deleted my-api") {
		t.Errorf("delete --yes: %v\n%s", err, out)
	}
	// Unlike other commands, delete never infers its target from deploy.yaml.
	if _, _, err := f.run(writeConfig(t, validConfig), "delete", "--yes"); err == nil {
		t.Error("delete without an explicit name must be a usage error")
	}
}

func TestUnauthorizedAndUnreachable(t *testing.T) {
	f := newFakeAgent(t)
	f.t.Chdir(t.TempDir())

	runWith := func(env map[string]string, args ...string) error {
		env[cliconfig.EnvConfig] = filepath.Join(t.TempDir(), "config.yaml")
		_, root := newRoot(Options{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard,
			Getenv: func(k string) string { return env[k] }})
		root.SetArgs(args)
		return root.ExecuteContext(context.Background())
	}

	err := runWith(map[string]string{cliconfig.EnvURL: f.srv.URL, cliconfig.EnvToken: "wrong-token-0123456789"}, "ps")
	if got := Render(err); !strings.Contains(got, "rejected the API token") || !strings.Contains(got, "deployctl login") {
		t.Errorf("unexpected rendering: %s", got)
	}
	if strings.Contains(Render(err), "wrong-token") {
		t.Error("the token must never be echoed")
	}

	f.srv.Close()
	err = runWith(map[string]string{cliconfig.EnvURL: f.srv.URL, cliconfig.EnvToken: testToken}, "ps")
	if got := Render(err); !strings.Contains(got, "cannot reach the Shipwick agent") || !strings.Contains(got, "ssh -L") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestInit(t *testing.T) {
	f := newFakeAgent(t)
	dir := filepath.Join(t.TempDir(), "My Cool_API")
	os.MkdirAll(dir, 0o755)

	out, _, err := f.run(dir, "init", "--image", "ghcr.io/company/cool:1.0.0", "--port", "3000", "--domain", "cool.example.com")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "Created deploy.yaml") {
		t.Errorf("unexpected output:\n%s", out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, DefaultFile))
	app, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("init must produce a valid config: %v\n%s", err, data)
	}
	if app.Name != "my-cool-api" || app.Image != "ghcr.io/company/cool:1.0.0" || app.Port != 3000 || app.Domain != "cool.example.com" {
		t.Errorf("unexpected config: %+v", app)
	}
	if app.Health != nil {
		t.Error("a health check must be opt-in: a guessed path would fail every deployment of an app without it")
	}

	if _, _, err := f.run(dir, "init", "--image", "other:1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("init must not overwrite silently, err = %v", err)
	}
	if _, _, err := f.run(dir, "init", "--image", "other:1", "--force"); err != nil {
		t.Errorf("init --force: %v", err)
	}
	if _, _, err := f.run(t.TempDir(), "init"); err == nil || !strings.Contains(err.Error(), "--image is required") {
		t.Errorf("without a terminal init cannot prompt, err = %v", err)
	}
	if _, _, err := f.run(t.TempDir(), "init", "--image", "Not A Valid Image"); err == nil {
		t.Error("init must not write an invalid config")
	}
	if len(f.requests) != 0 {
		t.Errorf("init is offline, saw %v", f.requests)
	}
}

func TestValidate(t *testing.T) {
	f := newFakeAgent(t)
	out, _, err := f.run(writeConfig(t, validConfig), "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	assertInOrder(t, out, []string{"deploy.yaml is valid", "my-api", "1.4.2", "unlimited CPU"})
	if len(f.requests) != 0 {
		t.Errorf("validate is offline, saw %v", f.requests)
	}
}

func TestOverrideImage(t *testing.T) {
	tests := []struct{ name, in, image, wantImage string }{
		{"replaces", "name: a\nimage: old:1\nport: 80\n", "new:2", "new:2"},
		{"adds when missing", "name: a\n", "new:2", "new:2"},
		{"keeps comments and other keys", "# my app\nname: a # the name\nimage: old:1\nenv:\n  KEY: value\n", "new:2", "new:2"},
		{"numeric-looking tag stays a string", "name: a\nimage: old:1\n", "registry.local/app:123", "registry.local/app:123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := overrideImage([]byte(tt.in), tt.image)
			app, err := spec.Parse(out)
			if err != nil {
				t.Fatalf("result does not parse: %v\n%s", err, out)
			}
			if app.Image != tt.wantImage || app.Name != "a" {
				t.Errorf("unexpected result: %+v\n%s", app, out)
			}
		})
	}

	out := string(overrideImage([]byte("# my app\nname: a\nimage: old:1\nenv:\n  KEY: value\n"), "new:2"))
	if !strings.Contains(out, "# my app") || !strings.Contains(out, "KEY: value") {
		t.Errorf("the rest of the document should survive:\n%s", out)
	}

	for _, garbage := range []string{"", "- a\n- list\n", "name: [unclosed"} {
		if got := overrideImage([]byte(garbage), "new:2"); string(got) != garbage {
			t.Errorf("unparseable input must pass through for Parse to reject, got %q", got)
		}
	}
}

func assertInOrder(t *testing.T, text string, fragments []string) {
	t.Helper()
	rest := text
	for _, frag := range fragments {
		i := strings.Index(rest, frag)
		if i < 0 {
			t.Fatalf("missing %q (or out of order) in:\n%s", frag, text)
		}
		rest = rest[i+len(frag):]
	}
}

func TestRenderFallback(t *testing.T) {
	if got := Render(fmt.Errorf("something odd")); got != "Error: something odd" {
		t.Errorf("Render = %q", got)
	}
	if Render(nil) != "" {
		t.Error("Render(nil) should be empty")
	}
}

func TestServerStatus(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1", OS: "Debian 13", Architecture: "x86_64", Kernel: "6.12",
		DockerVersion: "29.8.0", CPUs: 4, MemoryBytes: 8 << 30, Applications: 3, Containers: 5,
		Proxy: api.ProxyStatus{Enabled: true, Reachable: true, Routes: 2}}

	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v", err)
	}
	assertInOrder(t, out, []string{"reachable", "1.2.3", "vps-1", "Debian 13", "29.8.0", "8 GB", "5 running", "Proxy", "ok", "serving 2 domains"})

	f.server.Proxy = api.ProxyStatus{Enabled: true, Reachable: false, Error: "cannot reach Caddy's admin endpoint: connection refused"}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	assertInOrder(t, out, []string{"Proxy", "unreachable", "connection refused"})

	f.server.Proxy = api.ProxyStatus{}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	assertInOrder(t, out, []string{"Proxy", "not configured", "SHIPWICK_CADDY_ADMIN"})
}

func TestDeployRolledBack(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusRolledBack, CompletedAt: &done,
			Error: "replica 2 exited with code 1 shortly after start"},
		Events: []api.Event{
			event(1, api.EventStep, api.LevelInfo, "Replica 1/2 is serving 1.4.2; its 1.4.1 predecessor is retired"),
			event(2, api.EventState, api.LevelError, "FAILED: replica 2 exited with code 1 shortly after start"),
			event(3, api.EventStep, api.LevelInfo, "Rolling back: restoring 1 replica of 1.4.1"),
			event(4, api.EventStep, api.LevelInfo, "Rolled back: my-api is running 1.4.1 again"),
		},
	}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppHealthy}, ActiveDeployment: &api.Deployment{Version: "1.4.1"}}

	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v; a rolled-back deployment is a failed deployment: CI must go red", err)
	}
	assertInOrder(t, out, []string{
		"✓ Replica 1/2 is serving 1.4.2",
		"✓ Rolling back: restoring 1 replica of 1.4.1",
		"✓ Rolled back: my-api is running 1.4.1 again",
		"✗ Deployment failed and was rolled back",
		"replica 2 exited with code 1",
		"my-api is running 1.4.1 again: the replicas that had already been replaced were restored",
	})
}
