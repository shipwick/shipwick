package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func withStopTimeout(a spec.App, d time.Duration) spec.App {
	a.Deploy.StopTimeout = spec.Duration(d)
	return a
}

func TestDeploymentCompletesWhileTheReplacedReplicaIsStillStopping(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 1))
	old := s.container(t, 1)

	// The old replica takes its whole grace period over SIGTERM.
	stopping := s.rt.HoldStops()
	v2 := s.begin(web("web:1.1", 1))
	if id := <-stopping; id != old.ID {
		t.Fatalf("the container being stopped is %s, want the old replica %s", id, old.ID)
	}
	d := s.awaitCompleted(v2.ID)

	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if !strings.Contains(s.steps(d.ID), "Deployment successful") {
		t.Errorf("the deployment did not say it succeeded:\n%s", s.steps(d.ID))
	}
	if c, err := s.rt.InspectContainer(ctx, old.ID); err != nil || !c.Running {
		t.Fatalf("the old replica should still be on its way out: %+v, %v", c, err)
	}
	if got := p.upstreams("web.example.com"); !slices.Contains(got, "shipwick_web_2_1:8080") {
		t.Errorf("serving %v, want the new replica in rotation", got)
	}

	// It is nobody's leftover: the supervisor leaves it to its drain.
	s.advance(time.Second)
	if events := strings.Join(s.appEvents(t, "web"), "\n"); strings.Contains(events, "leftover") {
		t.Errorf("the supervisor took the draining replica for a leftover:\n%s", events)
	}
	if view, _ := s.engine.Application(ctx, "web"); view.Status != api.AppHealthy || view.Replicas.Running != 1 {
		t.Errorf("application while the old replica drains: %s, %d running; want HEALTHY with the one replica that counts", view.Status, view.Replicas.Running)
	}

	// The application is free: completed_at means the next operation is accepted.
	v3 := s.begin(web("web:1.2", 1))
	s.rt.ReleaseStops()
	s.engine.Wait()

	if got := s.deployment(v3.ID); got.Status != api.StatusActive {
		t.Errorf("the next deployment: %s (%s)", got.Status, got.Error)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_3_1" {
		t.Errorf("containers = %s, want only the newest replica once everything has drained", got)
	}
	if replicas, _ := s.store.ListReplicas(ctx, 1); len(replicas) != 0 {
		t.Errorf("the drained replica is still recorded as alive: %+v", replicas)
	}
}

// awaitStep polls a deployment's events until one says text.
func (h *harness) awaitStep(id int64, text string) {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for !strings.Contains(h.steps(id), text) {
		select {
		case <-poll.C:
		case <-deadline:
			h.t.Fatalf("deployment %d never said %q:\n%s", id, text, h.steps(id))
		}
	}
}

// awaitStatus polls a deployment until it has the given status.
func (h *harness) awaitStatus(id int64, want api.DeploymentStatus) {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for h.deployment(id).Status != want {
		select {
		case <-poll.C:
		case <-deadline:
			h.t.Fatalf("deployment %d is %s, and never became %s", id, h.deployment(id).Status, want)
		}
	}
}

func TestNextReplicaWaitsForTheReplacedOneToBeGone(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 2))

	// While the first old replica drains, nothing new may start: that would
	// be two containers more than the application asks for.
	stopping := s.rt.HoldStops()
	s.rt.ResetPeak()
	v2 := s.begin(web("web:1.1", 2))
	<-stopping
	s.awaitStep(v2.ID, "Replica 1/2 is serving 1.1")
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2 shipwick_web_2_1" {
		t.Errorf("containers while the first replaced replica drains = %s", got)
	}
	s.rt.ReleaseStops()
	s.engine.Wait()

	if got := s.deployment(v2.ID); got.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", got.Status, got.Error)
	}
	if peak := s.rt.PeakContainers(); peak != 3 {
		t.Errorf("peak = %d containers, want 3: one more than the two replicas, never two", peak)
	}
}

func TestRollbackWaitsForTheReplicaItMustRecreate(t *testing.T) {
	s, _ := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))

	// The deployment runs out of time while replica 1 of the old version —
	// which the rollback has to bring back under the same name — is still
	// stopping.
	s.engine.opts.DeployTimeout = 500 * time.Millisecond
	stopping := s.rt.HoldStops()
	v2 := s.begin(web("web:1.1", 2))
	<-stopping
	s.awaitStep(v2.ID, "Replica 1/2 is serving 1.1")
	s.awaitStatus(v2.ID, api.StatusRollback)
	s.rt.ReleaseStops()
	s.engine.Wait()

	d := s.deployment(v2.ID)
	if d.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s), want ROLLED_BACK", d.Status, d.Error)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" {
		t.Errorf("containers = %s, want the old version whole again", got)
	}
	for _, c := range s.rt.Containers() {
		if !c.Running || c.DeploymentID != v1.ID {
			t.Errorf("unexpected container after the rollback: %+v", c)
		}
	}
}

func TestAReplicaThatHadToBeKilledIsReported(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(withStopTimeout(web("web:1.0", 1), 45*time.Second))
	s.rt.IgnoreSIGTERM("web:1.0")

	d := s.deploy(web("web:1.1", 1))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	events, _ := s.engine.Events(context.Background(), "web", 10)
	if len(events) != 1 || events[0].Level != api.LevelWarn ||
		!strings.Contains(events[0].Message, "The replaced container shipwick_web_1_1 did not exit within 45s of SIGTERM and was killed") ||
		!strings.Contains(events[0].Message, "deploy.stop_timeout") {
		t.Errorf("the application's events do not say that the old replica was killed, and what to do about it: %+v", events)
	}

	// One that exits when asked is nothing to report.
	s.deploy(web("web:1.2", 1))
	if events, _ := s.engine.Events(context.Background(), "web", 10); len(events) != 1 {
		t.Errorf("a replica that stopped by itself was reported: %+v", events)
	}
}

func TestStopTimeoutOfTheApplicationIsTheGracePeriodEverywhere(t *testing.T) {
	ctx := context.Background()
	const own = 90 * time.Second
	grace := func(t *testing.T, s *supervised, container string) time.Duration {
		t.Helper()
		got, stopped := s.rt.StopTimeout(container)
		if !stopped {
			t.Fatalf("%s was never stopped", container)
		}
		return got
	}

	t.Run("a replica that a rollout replaces", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withStopTimeout(web("web:1.0", 1), own))
		// The grace period is the old version's: it is the one that stops.
		s.deploy(withStopTimeout(web("web:1.1", 1), 5*time.Second))
		if got := grace(t, s, "shipwick_web_1_1"); got != own {
			t.Errorf("grace period = %s, want %s", got, own)
		}
	})
	t.Run("without one, the agent's default", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(web("web:1.0", 1))
		s.deploy(web("web:1.1", 1))
		if got := grace(t, s, "shipwick_web_1_1"); got != s.engine.opts.StopTimeout {
			t.Errorf("grace period = %s, want the default %s", got, s.engine.opts.StopTimeout)
		}
	})
	t.Run("stop", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withStopTimeout(web("web:1.0", 2), own))
		if err := s.engine.Stop(ctx, "web"); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"shipwick_web_1_1", "shipwick_web_1_2"} {
			if got := grace(t, s, name); got != own {
				t.Errorf("%s: grace period = %s, want %s", name, got, own)
			}
		}
	})
	t.Run("delete", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withStopTimeout(web("web:1.0", 1), own))
		if err := s.engine.Delete(ctx, "web"); err != nil {
			t.Fatal(err)
		}
		if got := grace(t, s, "shipwick_web_1_1"); got != own {
			t.Errorf("grace period = %s, want %s", got, own)
		}
	})
	t.Run("the old version under recreate", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withStopTimeout(stateful("db:1.0"), own))
		s.deploy(stateful("db:1.1"))
		if got := grace(t, s, "shipwick_web_1_1"); got != own {
			t.Errorf("grace period = %s, want %s", got, own)
		}
	})
	t.Run("an unhealthy replica the supervisor restarts", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withStopTimeout(withHealth(web("web:1.0", 1)), own))
		c := s.container(t, 1)
		s.probes.setFailing(c.IP, errors.New("connection refused"))
		for range 5 {
			s.advance(10 * time.Second)
		}
		if got := grace(t, s, c.Name); got != own {
			t.Errorf("grace period = %s, want %s", got, own)
		}
	})
	t.Run("a leftover of another deployment", func(t *testing.T) {
		s, _ := newRouted(t)
		v1 := s.deploy(withStopTimeout(web("web:1.0", 1), own))
		s.deploy(web("web:1.1", 1))
		// A replica of the first version that somehow is still there.
		id, name, _ := s.rt.CreateContainer(ctx, containerSpec("web", v1.ID, 1, 2))
		s.rt.StartContainer(ctx, id)
		s.advance(time.Second)
		s.engine.Wait()
		if got := grace(t, s, name); got != own {
			t.Errorf("grace period = %s, want %s: the one of the deployment the container belongs to", got, own)
		}
	})
}

func TestRecreateWaitsForAReplicaThatIsStillDraining(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))
	old := s.container(t, 1)

	stopping := s.rt.HoldStops()
	v2 := s.begin(web("web:1.1", 1))
	<-stopping
	s.awaitCompleted(v2.ID)

	// The application turns stateful while the first version's replica is
	// still on its way out. Nothing of the new version may exist before
	// every other container of the application has stopped.
	var runningAtCreate []string
	s.rt.CreateHook = func(docker.ContainerSpec) error {
		for _, existing := range s.rt.Containers() {
			if existing.Running {
				runningAtCreate = append(runningAtCreate, existing.Name)
			}
		}
		return nil
	}
	v3 := s.begin(stateful("web:2.0"))
	// The second version stops when asked; the first one's replica is still
	// taking its time.
	s.rt.ReleaseStop(<-stopping)
	s.awaitStep(v3.ID, "Stopped 1.1")
	if c, err := s.rt.InspectContainer(ctx, old.ID); err != nil || !c.Running {
		t.Fatalf("the first version's replica should still be draining: %+v, %v", c, err)
	}
	if got := names(s.rt.Containers()); strings.Contains(got, "shipwick_web_3_1") {
		t.Errorf("containers = %s: the new version was created while a replica of an earlier one was still running", got)
	}
	s.rt.ReleaseStops()
	s.engine.Wait()

	if got := s.deployment(v3.ID); got.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", got.Status, got.Error)
	}
	if len(runningAtCreate) != 0 {
		t.Errorf("%v were running when the new version was created; under recreate nothing else runs", runningAtCreate)
	}
}

func TestFailedDeploymentRemovesTheImageItNamed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	stamp := time.Now().UTC().Format("20060102-150405")
	good, bad := "shipwick.local/my-api:"+stamp+"-good", "shipwick.local/my-api:"+stamp+"-bad"
	h.rt.AddLocalImage(good)
	h.rt.AddLocalImage(bad)
	h.deploy(built(good))

	h.rt.CrashImages[bad] = true
	d := h.deploy(built(bad))
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	if got := h.rt.RemovedImages(); !slices.Equal(got, []string{bad}) {
		t.Errorf("removed %v, want the image sent for the failed deployment and nothing else", got)
	}
	if ok, _ := h.rt.ImageExists(ctx, good); !ok {
		t.Error("the image of the version that is running must stay")
	}
	if !strings.Contains(h.steps(d.ID), "Removed image "+bad) {
		t.Errorf("the deployment does not say that its image went:\n%s", h.steps(d.ID))
	}
}

func TestRolledBackDeploymentRemovesItsImage(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 2))
	s.rt.CrashNames["shipwick_web_2_2"] = true

	d := s.deploy(web("web:1.1", 2))
	if d.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s), want ROLLED_BACK", d.Status, d.Error)
	}
	if got := s.rt.RemovedImages(); !slices.Equal(got, []string{"web:1.1"}) {
		t.Errorf("removed %v, want web:1.1", got)
	}
}

func TestFailedDeploymentKeepsAnImageSomethingElseNeeds(t *testing.T) {
	t.Run("the active deployment's, when a redeploy fails", func(t *testing.T) {
		ctx := context.Background()
		h := newHarness(t)
		h.deploy(app("my-api", "my-api:1.0", 1))

		h.rt.CreateErr = errors.New("no space left on device")
		d := h.finish(h.engine.Redeploy(ctx, "my-api", ""))
		h.rt.CreateErr = nil
		if d.Status != api.StatusFailed {
			t.Fatalf("status = %s, want FAILED", d.Status)
		}
		if got := h.rt.RemovedImages(); len(got) != 0 {
			t.Errorf("removed %v: the image is the one that is running", got)
		}
	})
	t.Run("the rollback target's", func(t *testing.T) {
		ctx := context.Background()
		h := newHarness(t)
		h.deploy(app("my-api", "my-api:1.0", 1))
		h.deploy(app("my-api", "my-api:1.1", 1))

		h.rt.CreateErr = errors.New("no space left on device")
		d := h.finish(h.engine.Rollback(ctx, "my-api", 0))
		h.rt.CreateErr = nil
		if d.Status != api.StatusFailed {
			t.Fatalf("status = %s, want FAILED", d.Status)
		}
		if got := h.rt.RemovedImages(); len(got) != 0 {
			t.Errorf("removed %v: 1.0 is still what a rollback goes to", got)
		}
	})
	t.Run("another application's", func(t *testing.T) {
		h := newHarness(t)
		h.deploy(app("worker", "shared:1.0", 1))

		h.rt.CrashNames["shipwick_web_1_1"] = true
		d := h.deploy(app("web", "shared:1.0", 1))
		if d.Status != api.StatusFailed {
			t.Fatalf("status = %s, want FAILED", d.Status)
		}
		if got := h.rt.RemovedImages(); len(got) != 0 {
			t.Errorf("removed %v: worker runs it", got)
		}
	})
}

func TestValidateAnswersWhatDeployWouldWithoutDeploying(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.engine.opts.ReservedHostPorts = []int{9000}
	taken := web("web:1.0", 1)
	taken.Name = "other"
	taken.Publish = []spec.Publish{{Port: 5432, Host: 5432, Protocol: "tcp"}}
	taken.Deploy.Strategy = spec.StrategyRecreate
	h.deploy(taken)
	before, _ := h.store.ListDeployments(ctx, store.DeploymentFilter{})

	check := func(a spec.App) error {
		t.Helper()
		got := h.engine.Validate(ctx, a)
		// The same document, deployed: the same answer.
		_, deployErr := h.engine.Deploy(ctx, a)
		h.engine.Wait()
		if got == nil != (deployErr == nil) || (got != nil && got.Error() != deployErr.Error()) {
			t.Errorf("Validate answered %v, Deploy %v", got, deployErr)
		}
		return got
	}

	a := web("web:1.0", 1)
	a.Name = "mine"
	var conflict *DomainConflictError
	if err := check(a); !errors.As(err, &conflict) || conflict.Field != "domain" {
		t.Errorf("a hostname another application serves: err = %v", err)
	}

	a = app("mine", "mine:1.0", 1)
	a.Publish = []spec.Publish{{Port: 5432, Host: 5432, Protocol: "tcp"}}
	var port *PortConflictError
	if err := check(a); !errors.As(err, &port) || port.Owner != `application "other"` {
		t.Errorf("a port another application publishes: err = %v", err)
	}
	a.Publish = []spec.Publish{{Port: 9000, Host: 9000, Protocol: "tcp"}}
	if err := check(a); !errors.As(err, &port) {
		t.Errorf("a port Shipwick listens on: err = %v", err)
	}

	a = app("mine", "mine:1.0", 1)
	a.Env = map[string]string{"DATABASE_URL": "postgres://app:${DB_PASSWORD}@db/app"}
	var missing *MissingSecretsError
	if err := check(a); !errors.As(err, &missing) || len(missing.Missing) != 1 || missing.Missing[0].Name != "DB_PASSWORD" {
		t.Errorf("a secret that is not stored: err = %v", err)
	}

	after, _ := h.store.ListDeployments(ctx, store.DeploymentFilter{})
	if len(after) != len(before) {
		t.Errorf("%d deployments were recorded for documents that were refused", len(after)-len(before))
	}

	// A document that would be accepted: valid, and nothing happens.
	a = app("mine", "mine:1.0", 1)
	if err := h.engine.Validate(ctx, a); err != nil {
		t.Errorf("Validate of an acceptable document: %v", err)
	}
	if _, err := h.store.GetApplication(ctx, "mine"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Validate registered the application: %v", err)
	}
	if len(h.rt.Pulled()) != 1 {
		t.Errorf("Validate pulled an image: %v", h.rt.Pulled())
	}
}

func TestValidateAcceptsADocumentWhoseImageIsNotBuiltYet(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	a := built("")

	if err := h.engine.Validate(ctx, a); err != nil {
		t.Errorf("Validate before the build: %v", err)
	}
	// Deploy keeps refusing it: by then the image must be there.
	if _, err := h.engine.Deploy(ctx, a); !errors.Is(err, ErrImageNotBuilt) {
		t.Errorf("Deploy without the image: err = %v, want ErrImageNotBuilt", err)
	}
}

func TestValidateDoesNotWaitForARunningDeployment(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "pull")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached

	// The application is locked by its deployment; the question is answered
	// all the same, and the deployment is none the wiser.
	if err := h.engine.Validate(ctx, app("my-api", "my-api:1.2", 1)); err != nil {
		t.Errorf("Validate while a deployment runs: %v", err)
	}
	if _, err := h.engine.Deploy(ctx, app("my-api", "my-api:1.2", 1)); !errors.Is(err, ErrBusy) {
		t.Errorf("Deploy while a deployment runs: err = %v, want ErrBusy", err)
	}
	if got := h.deployment(d.ID); IsSettled(got.Status) {
		t.Errorf("the running deployment was disturbed: %s (%s)", got.Status, got.Error)
	}
}
