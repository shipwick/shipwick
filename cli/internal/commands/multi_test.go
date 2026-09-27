package commands

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// fakeMany is the several-applications side of fakeAgent: one deployment per
// application, each finishing on its second poll with the status the test
// chose, and a record of which applications had finished when each one was
// started — the order the CLI chose, seen from the server.
type fakeMany struct {
	mu       sync.Mutex
	outcomes map[string]api.DeploymentStatus // per application; empty: not in use
	ids      map[int64]string                // deployment id → application
	polls    map[int64]int
	finished []string
	started  map[string][]string // application → what had finished when it was started
}

func (m *fakeMany) enabled() bool { return len(m.outcomes) > 0 }

func (m *fakeMany) deploy(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ids == nil {
		m.ids, m.polls, m.started = map[int64]string{}, map[int64]int{}, map[string][]string{}
	}
	name := r.PathValue("name")
	// Ids start clear of the literal /deployments/1 route the single-application tests use.
	id := int64(10 + len(m.ids))
	m.ids[id] = name
	m.started[name] = append([]string{}, m.finished...)
	respond(w, 202, api.Deployment{ID: id, Application: name, Sequence: 1, Status: api.StatusPending})
}

func (m *fakeMany) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/deployments/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		name, ok := m.ids[id]
		if !ok {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		m.polls[id]++
		d := api.DeploymentDetail{
			Deployment: api.Deployment{ID: id, Application: name, Status: api.StatusBuilding},
			Events:     []api.Event{event(1, api.EventStep, api.LevelInfo, "Pulled image for "+name)},
		}
		if m.polls[id] >= 2 {
			done := fixedNow
			d.Status, d.CompletedAt = m.outcomes[name], &done
			if d.Status == api.StatusActive {
				d.Version = "1.0.0"
				d.Events = append(d.Events, event(2, api.EventStep, api.LevelInfo, "Deployment successful"))
			} else {
				d.Error = "replica 1 exited with code 1 shortly after start"
			}
			if m.polls[id] == 2 {
				m.finished = append(m.finished, name)
			}
		}
		respond(w, 200, d)
	})
}

const manyConfig = `apps:
  - name: postgres
    image: postgres:17
    port: 5432
    volumes: [{ name: data, path: /var/lib/postgresql/data }]
    health: { tcp: 5432 }
    deploy: { strategy: recreate }
    env: { POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}" }
  - name: api
    image: ghcr.io/company/api:2.3.0
    port: 8080
    domain: api.example.com
    after: [postgres]
  - name: web
    image: ghcr.io/company/web:2.3.0
    port: 3000
    domain: example.com
`

func writeMany(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, spec.MultiFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func allActive(names ...string) map[string]api.DeploymentStatus {
	m := map[string]api.DeploymentStatus{}
	for _, n := range names {
		m[n] = api.StatusActive
	}
	return m
}

func TestDeployManyInDependencyOrder(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}

	// No -f: deploy.yaml is absent, shipwick.yaml is there.
	out, _, err := f.run(writeMany(t, manyConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Deploying 3 applications...",
		"✓ Validated shipwick.yaml (1 variable substituted)",
		"3 of 3 applications deployed.",
	})
	for _, want := range []string{
		"postgres  ✓ Pulled image for postgres",
		"api       ✓ Pulled image for api",
		"web       ✓ Pulled image for web",
		"postgres  ✓ Deployment successful",
		"api       api 1.0.0",
		"web       web 1.0.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Error("a substituted value must never be printed")
	}

	f.many.mu.Lock()
	defer f.many.mu.Unlock()
	if got := f.many.started["api"]; !slices.Contains(got, "postgres") {
		t.Errorf("api must start only after postgres finished, but at its start %v had finished", got)
	}
	if len(f.many.started) != 3 {
		t.Errorf("every application must be deployed, got %v", f.many.started)
	}

	// Each application receives an ordinary deploy.yaml, placeholders filled in and `after` gone.
	if len(f.deployBodies) != 3 {
		t.Fatalf("got %d deployments, want 3", len(f.deployBodies))
	}
	for _, body := range f.deployBodies {
		app, err := spec.Parse([]byte(body))
		if err != nil || strings.Contains(body, "after") || strings.Contains(body, "${") {
			t.Errorf("the agent must receive a complete deploy.yaml (%v):\n%s", err, body)
		}
		if app.Name == "postgres" && app.Env["POSTGRES_PASSWORD"] != "s3cret" {
			t.Errorf("placeholders must be filled in for every entry, got %q", app.Env["POSTGRES_PASSWORD"])
		}
	}
}

func TestDeployManySkipsWhatDependsOnAFailure(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.many.outcomes["postgres"] = api.StatusFailed
	f.env = map[string]string{"POSTGRES_PASSWORD": "x"}

	out, _, err := f.run(writeMany(t, manyConfig), "deploy", "-f", spec.MultiFile)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v, want ErrReported: CI must go red when any application did not deploy\n%s", err, out)
	}
	for _, want := range []string{
		"postgres  ✗ Deployment failed",
		"postgres    replica 1 exited with code 1 shortly after start",
		"Skipped api: postgres did not deploy",
		"web       ✓ Deployment successful",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "Stopped: 1 of 3 applications deployed, 1 failed, 1 skipped.\n") {
		t.Errorf("unexpected last line:\n%s", out)
	}
	for _, body := range f.deployBodies {
		if strings.Contains(body, "name: api") {
			t.Error("a skipped application must not be sent to the agent")
		}
	}
}

func TestDeployManyNoWaitStartsWhatWaitsForNothing(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "x"}

	out, _, err := f.run(writeMany(t, manyConfig), "deploy", "--no-wait")
	if err != nil {
		t.Fatalf("deploy --no-wait: %v\n%s", err, out)
	}
	for _, want := range []string{"postgres  ✓ Deployment #1 started", "web       ✓ Deployment #1 started",
		"Not started: api waits for postgres", "2 of 3 applications started."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/deployments/") || strings.Contains(r, "/api/deploy") {
			t.Errorf("--no-wait must not poll and cannot start api, saw %s", r)
		}
	}
}

func TestDeployManyRefusesWhatCannotApply(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeMany(t, manyConfig)
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{"POSTGRES_PASSWORD": "x"}

	// With both files present and no -f, deploy.yaml wins, as it always has.
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &fixedNow}}}
	if out, _, err := f.run(dir, "deploy"); err != nil || !strings.Contains(out, "Deploying my-api...") {
		t.Errorf("deploy.yaml must be preferred: %v\n%s", err, out)
	}

	_, _, err := f.run(dir, "deploy", "-f", spec.MultiFile, "-f", DefaultFile)
	if got := Render(err); !strings.Contains(got, "cannot be combined with other files") {
		t.Errorf("unexpected rendering: %s", got)
	}
	_, _, err = f.run(dir, "deploy", "-f", spec.MultiFile, "--image", "x:1")
	if got := Render(err); !strings.Contains(got, "--image applies to one application") {
		t.Errorf("unexpected rendering: %s", got)
	}
	_, _, err = f.run(dir, "deploy", "-f", spec.MultiFile, "--parallel", "0")
	if got := Render(err); !strings.Contains(got, "--parallel must be at least 1") {
		t.Errorf("unexpected rendering: %s", got)
	}

	// An invalid entry is reported by entry, before anything reaches the agent.
	before := len(f.requests)
	_, _, err = f.run(writeMany(t, "apps:\n  - name: api\n    image: nginx\n    after: [db]\n"), "deploy")
	if got := Render(err); !strings.HasPrefix(got, "invalid shipwick.yaml\n\napps[0].after:\n") {
		t.Errorf("unexpected rendering:\n%s", got)
	}
	if len(f.requests) != before {
		t.Errorf("an invalid file must not reach the agent, saw %v", f.requests[before:])
	}
}

func TestValidateMany(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"POSTGRES_PASSWORD": "x"}
	out, _, err := f.run(writeMany(t, manyConfig), "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ shipwick.yaml is valid (1 variable substituted)",
		"postgres\n",
		"api       after postgres\n",
		"web\n",
		"Name", "postgres", "Strategy", "recreate",
		"Name", "api", "api.example.com",
		"Name", "web", "example.com",
	})
	if len(f.requests) != 0 {
		t.Errorf("validate is offline, saw %v", f.requests)
	}
}

func TestSchedule(t *testing.T) {
	entries := func(specs ...string) []spec.Entry {
		var out []spec.Entry
		for _, s := range specs {
			name, after, _ := strings.Cut(s, "<")
			e := spec.Entry{App: spec.App{Name: name}}
			if after != "" {
				e.After = strings.Split(after, ",")
			}
			out = append(out, e)
		}
		return out
	}
	type step struct {
		finish    string  // "name=outcome", or "" for the first step, or "halt"
		outcome   outcome // for finish
		wantStart string  // names started, comma-separated
		wantSkips string  // "name:blame", comma-separated
	}
	tests := []struct {
		name    string
		entries []spec.Entry
		limit   int
		steps   []step
		summary string
	}{
		{
			name:    "independent applications start together, dependants wait, the limit holds",
			entries: entries("postgres", "api<postgres", "web", "worker<api"),
			limit:   2,
			steps: []step{
				{wantStart: "postgres,web"},
				{finish: "postgres", outcome: deployed, wantStart: "api"},
				{finish: "web", outcome: deployed},
				{finish: "api", outcome: deployed, wantStart: "worker"},
				{finish: "worker", outcome: deployed},
			},
			summary: "4 of 4 applications deployed.",
		},
		{
			name:    "a failure skips everything that waits for it, however indirectly",
			entries: entries("postgres", "api<postgres", "worker<api", "web"),
			limit:   4,
			steps: []step{
				{wantStart: "postgres,web"},
				{finish: "postgres", outcome: failed, wantSkips: "api:postgres,worker:api"},
				{finish: "web", outcome: deployed},
			},
			summary: "Stopped: 1 of 4 applications deployed, 1 failed, 2 skipped.",
		},
		{
			name:    "a dependant with two dependencies waits for both",
			entries: entries("a", "b", "c<a,b"),
			limit:   4,
			steps: []step{
				{wantStart: "a,b"},
				{finish: "a", outcome: deployed},
				{finish: "b", outcome: deployed, wantStart: "c"},
				{finish: "c", outcome: deployed},
			},
			summary: "3 of 3 applications deployed.",
		},
		{
			name:    "one at a time is the file order",
			entries: entries("a", "b", "c"),
			limit:   1,
			steps: []step{
				{wantStart: "a"},
				{finish: "a", outcome: deployed, wantStart: "b"},
				{finish: "b", outcome: deployed, wantStart: "c"},
				{finish: "c", outcome: deployed},
			},
			summary: "3 of 3 applications deployed.",
		},
		{
			name:    "after Ctrl-C nothing more starts; what runs is still deploying, the rest never started",
			entries: entries("a", "b", "c<a"),
			limit:   1,
			steps: []step{
				{wantStart: "a"},
				{finish: "halt"},
				{finish: "a", outcome: interrupted},
			},
			summary: "Stopped: 0 of 3 applications deployed, 1 still deploying, 2 not started.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSchedule(tt.entries, tt.limit)
			for i, st := range tt.steps {
				switch st.finish {
				case "":
				case "halt":
					s.halt()
				default:
					s.finish(s.index[st.finish], st.outcome)
				}
				start, skips := s.next()
				var started, skipped []string
				for _, j := range start {
					started = append(started, tt.entries[j].App.Name)
				}
				for _, sk := range skips {
					skipped = append(skipped, tt.entries[sk.app].App.Name+":"+sk.blame)
				}
				if got := strings.Join(started, ","); got != st.wantStart {
					t.Errorf("step %d: started %q, want %q", i, got, st.wantStart)
				}
				if got := strings.Join(skipped, ","); got != st.wantSkips {
					t.Errorf("step %d: skipped %q, want %q", i, got, st.wantSkips)
				}
				if s.inFlight() > tt.limit {
					t.Errorf("step %d: %d in flight, limit %d", i, s.inFlight(), tt.limit)
				}
			}
			if got := s.summary(false); got != tt.summary {
				t.Errorf("summary = %q, want %q", got, tt.summary)
			}
		})
	}

	s := newSchedule(entries("a", "b<a"), 4)
	s.next()
	s.finish(0, begun)
	if got := s.summary(true); got != "1 of 2 applications started." {
		t.Errorf("--no-wait summary = %q", got)
	}
}

func TestDeployManyBuildsAnEntryHereFirst(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	f.many.outcomes = allActive("api", "web")
	docker := &fakeDocker{output: "#1 DONE\n"}
	f.build = docker.tools()

	config := "apps:\n  - name: api\n    build: ./api\n    port: 8080\n    domain: api.example.com\n  - name: web\n    image: ghcr.io/company/web:2.3.0\n    port: 3000\n    domain: example.com\n    after: [api]\n"
	dir := writeMany(t, config)
	out, _, err := f.run(dir, "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	// docker runs where shipwick.yaml is, with the entry's context.
	if docker.dir != "." || len(docker.argv) != 9 || docker.argv[5] != "api/Dockerfile" || docker.argv[8] != "api" {
		t.Errorf("argv = %q in %q", docker.argv, docker.dir)
	}
	image := docker.argv[7]
	if !strings.HasPrefix(image, "shipwick.local/api:") || f.images.body != image+"\n" {
		t.Errorf("built %q, uploaded %q", image, f.images.body)
	}
	for _, want := range []string{"api  ✓ Built " + image, "api  ✓ Sent image to the server", "2 of 2 applications deployed."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	var sentImage bool
	for _, body := range f.deployBodies {
		if app, err := spec.Parse([]byte(body)); err == nil && app.Name == "api" {
			sentImage = app.Image == image
		}
	}
	if !sentImage {
		t.Errorf("the api document must name the image the server answered:\n%v", f.deployBodies)
	}
}
