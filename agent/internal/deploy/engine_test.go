package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

type harness struct {
	t      *testing.T
	store  *store.Store
	rt     *dockertest.Fake
	engine *Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	engine := New(st, rt, Options{
		StabilizeWindow: 50 * time.Millisecond,
		NameSettle:      time.Millisecond,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &harness{t: t, store: st, rt: rt, engine: engine}
}

func app(name, image string, replicas int) spec.App {
	return spec.App{
		Name:      name,
		Image:     image,
		Port:      8080,
		Replicas:  replicas,
		Env:       map[string]string{"SECRET": "hunter2"},
		Resources: spec.Resources{CPU: 0.5, MemoryBytes: 256 << 20},
		Restart:   spec.Restart{Policy: spec.RestartAlways},
		Deploy:    spec.Deploy{Strategy: spec.StrategyRolling},
	}
}

// deploy runs a deployment to completion and returns its final record.
func (h *harness) deploy(a spec.App) store.Deployment {
	h.t.Helper()
	d, err := h.engine.Deploy(context.Background(), a)
	if err != nil {
		h.t.Fatalf("Deploy: %v", err)
	}
	h.engine.Wait()
	final, err := h.store.GetDeployment(context.Background(), d.ID)
	if err != nil {
		h.t.Fatalf("GetDeployment: %v", err)
	}
	return final
}

func (h *harness) events(id int64) []api.Event {
	h.t.Helper()
	events, err := h.store.ListDeploymentEvents(context.Background(), id)
	if err != nil {
		h.t.Fatalf("ListDeploymentEvents: %v", err)
	}
	return events
}

func TestDeploySuccess(t *testing.T) {
	h := newHarness(t)
	d := h.deploy(app("my-api", "my-api:1.0", 2))

	if d.Status != api.StatusActive || d.Error != "" || d.CompletedAt == nil {
		t.Fatalf("unexpected final deployment: %+v", d)
	}

	containers := h.rt.Containers()
	if len(containers) != 2 {
		t.Fatalf("got %d containers, want 2", len(containers))
	}
	for i, c := range containers {
		if !c.Running || c.Name != "shipwick_my-api_1_"+string(rune('1'+i)) || c.DeploymentID != d.ID {
			t.Errorf("unexpected container: %+v", c)
		}
		s := h.rt.Spec(c.ID)
		if s.NanoCPUs != 500_000_000 || s.MemoryBytes != 256<<20 || s.Env["SECRET"] != "hunter2" {
			t.Errorf("limits or env not applied: %+v", s)
		}
	}

	var states []string
	for _, e := range h.events(d.ID) {
		if e.Type == api.EventState {
			states = append(states, e.Message)
		}
		if strings.Contains(e.Message, "hunter2") {
			t.Errorf("event leaks env value: %q", e.Message)
		}
	}
	want := "BUILDING STARTING HEALTH_CHECKING HEALTHY ACTIVE"
	if got := strings.Join(states, " "); got != want {
		t.Errorf("state events = %q, want %q", got, want)
	}

	a, _ := h.store.GetApplication(context.Background(), "my-api")
	if a.ActiveDeploymentID == nil || *a.ActiveDeploymentID != d.ID {
		t.Errorf("application not pointed at the new deployment: %+v", a)
	}
}

func TestDeployReplacesPreviousVersion(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 2))
	v2 := h.deploy(app("my-api", "my-api:1.1", 3))

	if v2.Status != api.StatusActive || v2.Sequence != 2 {
		t.Fatalf("unexpected v2: %+v", v2)
	}
	old, _ := h.store.GetDeployment(context.Background(), v1.ID)
	if old.Status != api.StatusSuperseded {
		t.Errorf("v1 status = %s, want SUPERSEDED", old.Status)
	}

	containers := h.rt.Containers()
	if len(containers) != 3 {
		t.Fatalf("got %d containers, want 3 (old ones removed)", len(containers))
	}
	for _, c := range containers {
		if c.DeploymentID != v2.ID || c.Image != "my-api:1.1" {
			t.Errorf("container of old version survived: %+v", c)
		}
	}
	if replicas, _ := h.store.ListReplicas(context.Background(), v1.ID); len(replicas) != 0 {
		t.Errorf("v1 replicas should be marked removed, got %+v", replicas)
	}
}

func TestFailedDeployKeepsPreviousVersionRunning(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 2))

	h.rt.CrashImages["my-api:broken"] = true
	v2 := h.deploy(app("my-api", "my-api:broken", 2))

	if v2.Status != api.StatusFailed || v2.CompletedAt == nil {
		t.Fatalf("unexpected v2: %+v", v2)
	}
	if !strings.Contains(v2.Error, "exited with code 1") {
		t.Errorf("error = %q, want the exit code", v2.Error)
	}

	containers := h.rt.Containers()
	if len(containers) != 2 {
		t.Fatalf("got %d containers, want the 2 of v1", len(containers))
	}
	for _, c := range containers {
		if c.DeploymentID != v1.ID || !c.Running {
			t.Errorf("v1 container disturbed: %+v", c)
		}
	}

	still, _ := h.store.GetDeployment(context.Background(), v1.ID)
	a, _ := h.store.GetApplication(context.Background(), "my-api")
	if still.Status != api.StatusActive || *a.ActiveDeploymentID != v1.ID {
		t.Errorf("v1 must stay active: status=%s active=%d", still.Status, *a.ActiveDeploymentID)
	}

	var capturedLogs bool
	for _, e := range h.events(v2.ID) {
		if e.Type == api.EventLog && strings.Contains(e.Message, "Last output of replica") {
			capturedLogs = true
		}
	}
	if !capturedLogs {
		t.Error("crash output should be preserved as an event before the container is removed")
	}
}

func TestDeployFailsWhenImageCannotBePulled(t *testing.T) {
	h := newHarness(t)
	h.rt.PullErr = errors.New("manifest unknown")

	d := h.deploy(app("my-api", "my-api:nope", 1))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "manifest unknown") {
		t.Fatalf("unexpected deployment: %+v", d)
	}
	if n := len(h.rt.Containers()); n != 0 {
		t.Errorf("got %d containers, want 0", n)
	}
}

func TestDeployFallsBackToLocalImage(t *testing.T) {
	h := newHarness(t)
	h.rt.PullErr = errors.New("registry unreachable")
	h.rt.AddLocalImage("my-api:local")

	d := h.deploy(app("my-api", "my-api:local", 1))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s), want ACTIVE", d.Status, d.Error)
	}
	var warned bool
	for _, e := range h.events(d.ID) {
		if e.Level == api.LevelWarn && strings.Contains(e.Message, "local copy") {
			warned = true
		}
	}
	if !warned {
		t.Error("the fallback to the local image should be recorded as a warning")
	}
}

func TestDeployCleansUpWhenStartFails(t *testing.T) {
	h := newHarness(t)
	h.rt.StartErr = errors.New("port is already allocated")

	d := h.deploy(app("my-api", "my-api:1.0", 3))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "replica 1") {
		t.Fatalf("unexpected deployment: %+v", d)
	}
	if n := len(h.rt.Containers()); n != 0 {
		t.Errorf("created-but-unstarted containers must be removed, got %d", n)
	}
}

func TestConcurrentDeployIsRejected(t *testing.T) {
	h := newHarness(t)
	h.rt.PullDelay = 200 * time.Millisecond

	if _, err := h.engine.Deploy(context.Background(), app("my-api", "my-api:1.0", 1)); err != nil {
		t.Fatalf("first Deploy: %v", err)
	}
	if _, err := h.engine.Deploy(context.Background(), app("my-api", "my-api:1.1", 1)); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Deploy: err = %v, want ErrBusy", err)
	}
	// A different application is independent.
	if _, err := h.engine.Deploy(context.Background(), app("other", "other:1.0", 1)); err != nil {
		t.Fatalf("Deploy of another app: %v", err)
	}
	h.engine.Wait()

	if all, _ := h.store.ListDeployments(context.Background(), store.DeploymentFilter{}); len(all) != 2 {
		t.Errorf("got %d deployment records, want 2 (the rejected one must not be recorded)", len(all))
	}
}

// completed_at is the signal that the next operation will be accepted. It must
// not appear a moment too early: the engine still holds the application while
// it sweeps up after the commit.
func TestCompletedAtMeansNextDeployIsAccepted(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 3))
	h.rt.StopDelay = 150 * time.Millisecond

	v2, err := h.engine.Deploy(ctx, app("my-api", "my-api:1.1", 1))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		d, _ := h.store.GetDeployment(ctx, v2.ID)
		if d.CompletedAt != nil {
			break
		}
	}

	// The instant completed_at is visible, a new deployment must be accepted.
	if _, err := h.engine.Deploy(ctx, app("my-api", "my-api:1.2", 1)); err != nil {
		t.Fatalf("Deploy right after completion: %v", err)
	}
	h.engine.Wait()
}

func TestShutdownAbortsInFlightDeployment(t *testing.T) {
	h := newHarness(t)
	h.rt.PullDelay = time.Minute

	d, err := h.engine.Deploy(context.Background(), app("my-api", "my-api:1.0", 1))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.engine.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	final, _ := h.store.GetDeployment(context.Background(), d.ID)
	if final.Status != api.StatusFailed || !strings.Contains(final.Error, "shut down") {
		t.Errorf("unexpected deployment after shutdown: %+v", final)
	}
	if _, err := h.engine.Deploy(context.Background(), app("my-api", "my-api:1.0", 1)); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Deploy after Shutdown: err = %v, want ErrShuttingDown", err)
	}
}

func TestDeployTimeout(t *testing.T) {
	h := newHarness(t)
	h.engine.opts.DeployTimeout = 50 * time.Millisecond
	h.rt.PullDelay = time.Minute

	d := h.deploy(app("my-api", "my-api:1.0", 1))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "timed out") {
		t.Errorf("unexpected deployment: %+v", d)
	}
}

func TestStopAndStart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 2))

	if err := h.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, c := range h.rt.Containers() {
		if c.Running {
			t.Errorf("container still running after Stop: %+v", c)
		}
	}
	view, _ := h.engine.Application(ctx, "my-api")
	if view.Status != api.AppStopped || view.DesiredState != api.DesiredStopped {
		t.Errorf("after Stop: status=%s desired=%s", view.Status, view.DesiredState)
	}

	if err := h.engine.Start(ctx, "my-api"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	view, _ = h.engine.Application(ctx, "my-api")
	if view.Status != api.AppHealthy || view.Replicas.Running != 2 {
		t.Errorf("after Start: %+v", view.Application)
	}
}

func TestStopRequiresActiveDeployment(t *testing.T) {
	h := newHarness(t)
	h.rt.PullErr = errors.New("nope")
	h.deploy(app("my-api", "my-api:1.0", 1))

	if err := h.engine.Stop(context.Background(), "my-api"); !errors.Is(err, ErrNotDeployed) {
		t.Errorf("err = %v, want ErrNotDeployed", err)
	}
	if err := h.engine.Stop(context.Background(), "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

func TestDeployRevivesStoppedApplication(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.engine.Stop(ctx, "my-api")

	h.deploy(app("my-api", "my-api:1.1", 1))
	view, _ := h.engine.Application(ctx, "my-api")
	if view.Status != api.AppHealthy || view.DesiredState != api.DesiredRunning {
		t.Errorf("a deploy should bring a stopped app back: %+v", view.Application)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 2))
	h.deploy(app("other", "other:1.0", 1))

	if err := h.engine.Delete(ctx, "my-api"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].App != "other" {
		t.Errorf("only the other app's container should remain: %+v", containers)
	}
	if _, err := h.store.GetApplication(ctx, "my-api"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("application still in the database: %v", err)
	}
}

func TestRecover(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	// Simulate an agent crash in the middle of v2: a record stuck in
	// STARTING and a container that belongs to it.
	stuck, _ := h.store.CreateDeployment(ctx, app("my-api", "my-api:1.1", 1), time.Now())
	h.store.TransitionDeployment(ctx, stuck.ID, api.StatusPending, api.StatusBuilding, "")
	h.store.TransitionDeployment(ctx, stuck.ID, api.StatusBuilding, api.StatusStarting, "")
	h.rt.CreateContainer(ctx, containerSpec("my-api", stuck.ID, 2, 1))

	// A container of an application the database has never heard of.
	foreignID, _, _ := h.rt.CreateContainer(ctx, containerSpec("unknown-app", 99, 1, 1))

	if err := h.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, _ := h.store.GetDeployment(ctx, stuck.ID)
	if got.Status != api.StatusFailed || !strings.Contains(got.Error, "agent restarted") {
		t.Errorf("interrupted deployment: %+v", got)
	}

	var sawActive, sawForeign bool
	for _, c := range h.rt.Containers() {
		switch {
		case c.DeploymentID == v1.ID && c.App == "my-api":
			sawActive = true
		case c.ID == foreignID:
			sawForeign = true
		default:
			t.Errorf("leftover container was not removed: %+v", c)
		}
	}
	if !sawActive {
		t.Error("Recover removed the active deployment's container")
	}
	if !sawForeign {
		t.Error("Recover must never touch containers of applications it does not know")
	}
}

func TestApplicationsView(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.4.2", 2))
	h.rt.PullErr = errors.New("nope")
	h.deploy(app("never-worked", "x:1", 1))
	h.rt.PullErr = nil

	apps, err := h.engine.Applications(ctx)
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("got %d applications, want 2", len(apps))
	}
	ok, failed := apps[0], apps[1] // sorted by name
	if ok.Status != api.AppHealthy || ok.Version != "1.4.2" || ok.Replicas != (api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}) {
		t.Errorf("unexpected summary: %+v", ok)
	}
	if failed.Status != api.AppFailed {
		t.Errorf("status = %s, want FAILED", failed.Status)
	}

	// Kill one replica: DEGRADED. Kill both: DOWN.
	containers := h.rt.Containers()
	h.rt.Crash(containers[0].ID, 137)
	detail, _ := h.engine.Application(ctx, "my-api")
	if detail.Status != api.AppDegraded || detail.Replicas.Running != 1 {
		t.Errorf("after one crash: %+v", detail.Application)
	}
	if detail.Containers[0].ExitCode != 137 {
		t.Errorf("detail should carry the exit code: %+v", detail.Containers[0])
	}
	if detail.Spec.Env["SECRET"] == "hunter2" {
		t.Error("API view leaks env values")
	}
	h.rt.Crash(containers[1].ID, 137)
	detail, _ = h.engine.Application(ctx, "my-api")
	if detail.Status != api.AppDown {
		t.Errorf("status = %s, want DOWN", detail.Status)
	}
}

func TestLogsView(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 2))

	lines, err := h.engine.Logs(context.Background(), "my-api", 100)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(lines) != 2 || lines[0].Replica == lines[1].Replica {
		t.Errorf("want one line from each replica, got %+v", lines)
	}
	lines, _ = h.engine.Logs(context.Background(), "my-api", 1)
	if len(lines) != 1 {
		t.Errorf("tail=1 returned %d lines", len(lines))
	}
}
