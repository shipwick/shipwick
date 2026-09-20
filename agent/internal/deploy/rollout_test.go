package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/pkg/api"
)

func stepMessages(events []api.Event) string {
	var out []string
	for _, e := range events {
		if e.Type == api.EventStep {
			out = append(out, e.Message)
		}
	}
	return strings.Join(out, "\n")
}

func names(containers []docker.Container) string {
	var out []string
	for _, c := range containers {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestRollingDeploymentPeaksAtOneExtraContainer(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 3))
	s.rt.ResetPeak()

	d := s.deploy(web("web:1.1", 3))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if peak := s.rt.PeakContainers(); peak != 4 {
		t.Errorf("peak = %d containers for 3 replicas, want 4: replacing replica by replica is the point of rolling — 2N does not fit on a small server", peak)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_2_1 shipwick_web_2_2 shipwick_web_2_3" {
		t.Errorf("containers after the rollout: %s", got)
	}

	steps := stepMessages(s.events(d.ID))
	for _, want := range []string{
		"Replica 1/3 is serving 1.1; its 1.0 predecessor is retired",
		"Replica 3/3 is serving 1.1; its 1.0 predecessor is retired",
		"Routed https://web.example.com to 3 replicas",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("missing step %q in:\n%s", want, steps)
		}
	}
}

func TestFirstDeploymentStartsAllReplicasTogether(t *testing.T) {
	s, _ := newRouted(t)
	start := time.Now()
	d := s.deploy(web("web:1.0", 5))

	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	// One stabilization window (50ms in tests), not five.
	if elapsed := time.Since(start); elapsed > 220*time.Millisecond {
		t.Errorf("took %s: with nothing to replace, replicas have no reason to wait for each other", elapsed)
	}
	if steps := stepMessages(s.events(d.ID)); !strings.Contains(steps, "Started 5 containers") || strings.Contains(steps, "is serving") {
		t.Errorf("a first deployment should not be narrated replica by replica:\n%s", steps)
	}
}

func TestScalingUpAndDown(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))

	up := s.deploy(web("web:1.1", 4))
	if up.Status != api.StatusActive || len(s.rt.Containers()) != 4 {
		t.Fatalf("scale up: %s, %d containers", up.Status, len(s.rt.Containers()))
	}
	if got := p.upstreams("web.example.com"); len(got) != 4 {
		t.Errorf("upstreams after scaling up = %v", got)
	}

	s.rt.ResetPeak()
	down := s.deploy(web("web:1.2", 1))
	if down.Status != api.StatusActive {
		t.Fatalf("scale down: %s (%s)", down.Status, down.Error)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_3_1" {
		t.Errorf("containers after scaling down: %s", got)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_3_1:8080" {
		t.Errorf("upstreams after scaling down = %v", got)
	}
	if steps := stepMessages(s.events(down.ID)); !strings.Contains(steps, "Retired 3 replicas of 1.1 that 1.2 no longer needs") {
		t.Errorf("the surplus should be retired explicitly:\n%s", steps)
	}
	// Surplus replicas leave the rotation last: capacity never dips below
	// what the new version is meant to have.
	for _, step := range p.history("web.example.com") {
		if step == "" {
			t.Error("the domain was left without upstreams during a scale-down")
		}
	}
}

func TestFailureOnTheFirstReplicaNeedsNoRollback(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))
	s.rt.CrashImages["web:bad"] = true

	d := s.deploy(web("web:bad", 2))
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED: nothing of the old version was retired, so there is nothing to roll back", d.Status)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" {
		t.Errorf("containers = %s; v1 must be exactly as it was", got)
	}
	for _, c := range s.rt.Containers() {
		if s.rt.Starts(c.ID) != 1 {
			t.Errorf("%s was restarted; a failed deployment must not touch the old replicas", c.Name)
		}
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("upstreams = %v", got)
	}
	still, _ := s.store.GetDeployment(context.Background(), v1.ID)
	if still.Status != api.StatusActive {
		t.Errorf("v1 status = %s", still.Status)
	}
}

func TestFailureHalfWayRollsBack(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 3))

	// Replica 1 of the new version comes up fine; replica 2 dies on start.
	// By then v1's replica 1 is already gone.
	s.rt.CrashNames["shipwick_web_2_2"] = true
	d := s.deploy(web("web:1.1", 3))

	if d.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s), want ROLLED_BACK", d.Status, d.Error)
	}
	if !strings.Contains(d.Error, "replica 2 exited with code 1") {
		t.Errorf("error = %q; the cause of the failure must survive the rollback", d.Error)
	}
	if d.CompletedAt == nil {
		t.Error("a rolled-back deployment is done and must say so")
	}

	// v1 is whole again: same deployment, same names, three running replicas.
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2 shipwick_web_1_3" {
		t.Errorf("containers = %s", got)
	}
	for _, c := range s.rt.Containers() {
		if !c.Running || c.DeploymentID != v1.ID {
			t.Errorf("unexpected container after rollback: %+v", c)
		}
	}
	app, _ := s.store.GetApplication(ctx, "web")
	prev, _ := s.store.GetDeployment(ctx, v1.ID)
	if *app.ActiveDeploymentID != v1.ID || prev.Status != api.StatusActive {
		t.Errorf("v1 must still be the active deployment: active=%d status=%s", *app.ActiveDeploymentID, prev.Status)
	}
	if replicas, _ := s.store.ListReplicas(ctx, v1.ID); len(replicas) != 3 {
		t.Errorf("v1 has %d live replica records, want 3", len(replicas))
	}
	if got := p.upstreams("web.example.com"); strings.Join(got, " ") != "shipwick_web_1_1:8080 shipwick_web_1_2:8080 shipwick_web_1_3:8080" {
		t.Errorf("upstreams = %v; traffic must be back on v1 only", got)
	}

	var states []string
	for _, e := range s.events(d.ID) {
		if e.Type == api.EventState {
			states = append(states, strings.SplitN(e.Message, ":", 2)[0])
		}
	}
	if got := strings.Join(states, " "); got != "BUILDING STARTING HEALTH_CHECKING FAILED ROLLBACK RESTORING ROLLED_BACK" {
		t.Errorf("states = %s", got)
	}
	steps := stepMessages(s.events(d.ID))
	for _, want := range []string{"Rolling back: restoring 1 replica of 1.0", "Rolled back: web is running 1.0 again"} {
		if !strings.Contains(steps, want) {
			t.Errorf("missing step %q in:\n%s", want, steps)
		}
	}
	// The restore is this deployment's story; v1's own record stays as it was.
	for _, e := range s.events(v1.ID) {
		if strings.Contains(e.Message, "Rolling back") || e.CreatedAt.After(*d.CompletedAt) {
			t.Errorf("the previous deployment's event log was written to: %q", e.Message)
		}
	}

	// And the application is simply healthy afterwards.
	s.advance(time.Second)
	view, _ := s.engine.Application(ctx, "web")
	if view.Status != api.AppHealthy || view.Version != "1.0" {
		t.Errorf("after rollback: %s %s", view.Status, view.Version)
	}
}

func TestRollbackNeverDropsBelowWhatIsStillServing(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))
	s.rt.CrashNames["shipwick_web_2_2"] = true
	s.deploy(web("web:1.1", 2))

	for _, step := range p.history("web.example.com") {
		if step == "" {
			t.Errorf("the domain had no upstream at some point during the rollback: %q", p.history("web.example.com"))
		}
	}
}

func TestRollbackThatFailsIsReportedAndLeftToReconciliation(t *testing.T) {
	s, _ := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))

	s.rt.CrashNames["shipwick_web_2_2"] = true
	s.rt.CreateHook = func(spec docker.ContainerSpec) error {
		if spec.DeploymentID == v1.ID {
			return errors.New("no space left on device")
		}
		return nil
	}
	d := s.deploy(web("web:1.1", 2))

	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED: the rollback did not succeed either", d.Status)
	}
	for _, want := range []string{"replica 2 exited with code 1", "the rollback to 1.0 then failed too", "no space left on device"} {
		if !strings.Contains(d.Error, want) {
			t.Errorf("error %q should contain %q: both failures matter", d.Error, want)
		}
	}

	// v1 is still the active deployment, so once the disk is fine again the
	// supervisor completes it on its own.
	s.rt.CreateHook = nil
	for range 5 {
		s.advance(time.Second)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" {
		t.Errorf("containers = %s; reconciliation should have completed v1", got)
	}
}

// ---- reconciliation ----

func TestReconcilerRecreatesMissingReplica(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 2)))
	s.advance(time.Second)

	// Someone runs `docker rm -f` behind Shipwick's back.
	gone := s.container(t, 2)
	s.rt.RemoveContainer(ctx, gone.ID)

	s.advance(time.Second)
	recreated := s.container(t, 2)
	if recreated.ID == gone.ID || !recreated.Running || recreated.Name != "shipwick_web_1_2" {
		t.Fatalf("replica 2 was not recreated: %+v", recreated)
	}
	if spec := s.rt.Spec(recreated.ID); spec.Env["SECRET"] != "hunter2" || spec.MemoryBytes != 256<<20 {
		t.Errorf("the replacement must be built from the deployment's stored spec: %+v", spec)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 {
		t.Errorf("upstreams = %v; a recreated replica with a health check waits for its first passing probe", got)
	}
	s.advance(time.Second)
	s.advance(time.Second)
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("upstreams = %v; it should have rejoined", got)
	}

	events := strings.Join(s.appEvents(t, "web"), "\n")
	for _, want := range []string{"Replica 2's container shipwick_web_1_2 has disappeared", "Recreated replica 2"} {
		if !strings.Contains(events, want) {
			t.Errorf("missing event %q in:\n%s", want, events)
		}
	}
	view, _ := s.engine.Application(ctx, "web")
	if view.Status != api.AppHealthy || len(view.Containers) != 2 {
		t.Errorf("after reconciliation: %s, %d containers", view.Status, len(view.Containers))
	}
}

func TestReconcilerBacksOffWhenItCannotCreate(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))
	s.rt.RemoveContainer(context.Background(), s.container(t, 1).ID)

	attempts := 0
	s.rt.CreateHook = func(docker.ContainerSpec) error {
		attempts++
		return errors.New("no space left on device")
	}
	for range 600 { // ten minutes
		s.advance(time.Second)
	}
	// 1s, 2s, 5s, 10s, 30s, then every 5 minutes.
	if attempts < 6 || attempts > 8 {
		t.Errorf("%d attempts in 10 minutes; a server that is out of disk must not be hammered every second", attempts)
	}

	s.rt.CreateHook = nil
	for range 301 {
		s.advance(time.Second)
	}
	if len(s.rt.Containers()) != 1 {
		t.Error("once the cause is gone, the replica should come back")
	}
}

func TestReconcilerLeavesStoppedAndUnknownApplicationsAlone(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))
	s.engine.Stop(ctx, "web")
	s.rt.RemoveContainer(ctx, s.container(t, 1).ID)
	foreign, _, _ := s.rt.CreateContainer(ctx, containerSpec("not-ours", 42, 1, 1))

	for range 10 {
		s.advance(time.Second)
	}
	containers := s.rt.Containers()
	if len(containers) != 1 || containers[0].ID != foreign {
		t.Errorf("containers = %+v; a stopped application is not reconciled, and a container of an unknown application is never touched", containers)
	}
}

func TestSupervisorSweepsLeftoverContainers(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	d := s.deploy(web("web:1.0", 1))

	// A cleanup that was interrupted: a container of a long-gone deployment.
	stale, _, _ := s.rt.CreateContainer(ctx, docker.ContainerSpec{App: "web", DeploymentID: d.ID + 100, Sequence: 99, Replica: 1, Image: "web:0.9"})
	s.advance(time.Second)

	for _, c := range s.rt.Containers() {
		if c.ID == stale {
			t.Error("leftover container of another deployment was not removed")
		}
	}
	if len(s.rt.Containers()) != 1 {
		t.Errorf("the active replica must survive the sweep: %s", names(s.rt.Containers()))
	}
}

func TestReconcilerDoesNotMistakeAFreshDeploymentForMissingReplicas(t *testing.T) {
	// The supervisor lists containers under the application's lock. If it
	// used a snapshot from before it got the lock, replicas created by a
	// deployment that finished in between would look missing — and be
	// "recreated" on top of themselves.
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 2))
	s.engine.opts.SuperviseInterval = time.Millisecond
	s.engine.sup.inlineProbes = false
	s.engine.StartSupervisor()

	for i := range 8 {
		image := "web:2." + string(rune('0'+i))
		if d := s.deploy(web(image, 2)); d.Status != api.StatusActive {
			t.Fatalf("deploy %s: %s (%s)", image, d.Status, d.Error)
		}
	}
	time.Sleep(50 * time.Millisecond)

	for _, e := range s.appEvents(t, "web") {
		if strings.Contains(e, "disappeared") || strings.Contains(e, "Recreated") || strings.Contains(e, "Could not recreate") {
			t.Errorf("the supervisor interfered with a healthy application: %q", e)
		}
	}
	if len(s.rt.Containers()) != 2 {
		t.Errorf("containers = %s", names(s.rt.Containers()))
	}
}

func TestApplicationStaysHealthyInViewsThroughoutARollout(t *testing.T) {
	// The active deployment's own replicas are retired one by one before the
	// commit. A view that counted only those would call the application
	// DEGRADED, then DOWN, at the very moment it is being upgraded with full
	// capacity. Views must count whoever the rollout has serving.
	for _, tt := range []struct {
		name     string
		from, to int
	}{
		{"same size", 3, 3}, {"scale up", 2, 4}, {"scale down", 4, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, p := newRouted(t)
			s.deploy(web("web:1.0", tt.from))

			var seen []string
			p.onSync = func([]proxy.Route) {
				view, err := s.engine.Application(context.Background(), "web")
				if err != nil {
					return
				}
				seen = append(seen, fmt.Sprintf("%s %d/%d", view.Status, view.Replicas.Healthy, view.Replicas.Desired))
				if view.Status != api.AppHealthy || view.Replicas.Healthy < view.Replicas.Desired {
					t.Errorf("mid-rollout view: %s, %d healthy of %d desired", view.Status, view.Replicas.Healthy, view.Replicas.Desired)
				}
				if !view.Deploying || view.InFlightDeploymentID == nil {
					t.Errorf("mid-rollout view should name the deployment in flight: %+v", view.Application)
				}
			}
			d := s.deploy(web("web:1.1", tt.to))
			p.onSync = nil

			if d.Status != api.StatusActive || len(seen) < 2 {
				t.Fatalf("status=%s, observed %v", d.Status, seen)
			}
			view, _ := s.engine.Application(context.Background(), "web")
			if view.Status != api.AppHealthy || view.Replicas != (api.ReplicaCount{Desired: tt.to, Running: tt.to, Healthy: tt.to}) {
				t.Errorf("after the rollout: %s %+v", view.Status, view.Replicas)
			}
		})
	}
}
