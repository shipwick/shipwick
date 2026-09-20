package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// finish waits for a background deployment and returns its final record.
func (h *harness) finish(d store.Deployment, err error) store.Deployment {
	h.t.Helper()
	if err != nil {
		h.t.Fatalf("start deployment: %v", err)
	}
	h.engine.Wait()
	final, err := h.store.GetDeployment(context.Background(), d.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return final
}

func TestRollbackToPreviousVersion(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))
	a2 := web("web:1.1", 3)
	a2.Env = map[string]string{"SECRET": "rotated", "NEW_FLAG": "on"}
	s.deploy(a2)

	rb := s.finish(s.engine.Rollback(ctx, "web", 0))

	if rb.Status != api.StatusActive || rb.Kind != api.KindRollback || rb.SourceID == nil || *rb.SourceID != v1.ID {
		t.Fatalf("unexpected rollback deployment: %+v", rb)
	}
	if rb.Sequence != 3 || rb.Version != "1.0" {
		t.Errorf("a rollback is a new record with the old version: sequence=%d version=%s", rb.Sequence, rb.Version)
	}
	// The whole old configuration comes back, not just the image.
	containers := s.rt.Containers()
	if len(containers) != 2 {
		t.Fatalf("replicas = %d, want the 2 of the first version", len(containers))
	}
	for _, c := range containers {
		spec := s.rt.Spec(c.ID)
		if c.Image != "web:1.0" || spec.Env["SECRET"] != "hunter2" || spec.Env["NEW_FLAG"] != "" {
			t.Errorf("rollback must restore the full old configuration, real env values included: %+v", spec)
		}
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 || got[0] != "shipwick_web_3_1:8080" {
		t.Errorf("upstreams = %v", got)
	}
	// History is appended to, never rewritten.
	old, _ := s.store.GetDeployment(ctx, v1.ID)
	if old.Status != api.StatusSuperseded {
		t.Errorf("the record of the first version must stay SUPERSEDED, got %s", old.Status)
	}
}

func TestRollbackUsesTheSameEngine(t *testing.T) {
	// A rollback gets no shortcuts: rolling replacement, at most one extra
	// container, and if the old version no longer comes up, it is itself undone.
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 3))
	v2 := s.deploy(web("web:1.1", 3))

	s.rt.ResetPeak()
	s.rt.CrashNames["shipwick_web_3_2"] = true // the second replica of the rollback
	rb := s.finish(s.engine.Rollback(context.Background(), "web", 0))

	if rb.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s), want ROLLED_BACK: a failing rollback is rolled back like any deployment", rb.Status, rb.Error)
	}
	if peak := s.rt.PeakContainers(); peak > 4 {
		t.Errorf("peak = %d containers; a rollback must roll too", peak)
	}
	for _, c := range s.rt.Containers() {
		if c.DeploymentID != v2.ID || !c.Running {
			t.Errorf("the second version should be whole again: %+v", c)
		}
	}
}

func TestRollbackToChosenDeployment(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	v1 := s.deploy(web("web:1.0", 1))
	s.deploy(web("web:1.1", 1))
	s.deploy(web("web:1.2", 1))

	rb := s.finish(s.engine.Rollback(ctx, "web", v1.ID))
	if rb.Status != api.StatusActive || rb.Version != "1.0" || *rb.SourceID != v1.ID {
		t.Errorf("unexpected rollback: %+v", rb)
	}
	// "Previous" now means the version that was active before the rollback.
	again := s.finish(s.engine.Rollback(ctx, "web", 0))
	if again.Version != "1.2" {
		t.Errorf("rolling back a rollback returned to %s, want 1.2", again.Version)
	}
}

func TestRollbackTargets(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)

	if _, err := s.engine.Rollback(ctx, "ghost", 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v", err)
	}
	v1 := s.deploy(web("web:1.0", 1))
	if _, err := s.engine.Rollback(ctx, "web", 0); !errors.Is(err, ErrNoRollbackTarget) {
		t.Errorf("first deployment: err = %v, want ErrNoRollbackTarget", err)
	}
	if _, err := s.engine.Rollback(ctx, "web", v1.ID); !errors.Is(err, ErrNoRollbackTarget) {
		t.Errorf("rolling back to the active deployment: err = %v", err)
	}

	s.rt.CrashImages["web:bad"] = true
	bad := s.deploy(web("web:bad", 1))
	if _, err := s.engine.Rollback(ctx, "web", bad.ID); !errors.Is(err, ErrNoRollbackTarget) {
		t.Errorf("a FAILED deployment is not a version to return to: err = %v", err)
	}
	if _, err := s.engine.Rollback(ctx, "web", 0); !errors.Is(err, ErrNoRollbackTarget) {
		t.Errorf("a failed attempt must not count as a previous version: err = %v", err)
	}

	other := s.deploy(app("other", "other:1.0", 1))
	s.deploy(app("other", "other:1.1", 1))
	if _, err := s.engine.Rollback(ctx, "web", other.ID); !errors.Is(err, ErrNoRollbackTarget) {
		t.Errorf("a deployment of another application: err = %v", err)
	}
	if all, _ := s.store.ListDeployments(ctx, store.DeploymentFilter{Application: "web"}); len(all) != 2 {
		t.Errorf("refused rollbacks must not leave records: %d", len(all))
	}
	// A refusal must also give the lock back.
	if d := s.deploy(web("web:1.1", 1)); d.Status != api.StatusActive {
		t.Errorf("deploy after refused rollbacks: %s %q", d.Status, d.Error)
	}
}

func TestRedeploy(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)

	if _, err := s.engine.Redeploy(ctx, "web", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v", err)
	}
	v1 := s.deploy(web("web:1.0", 2))

	same := s.finish(s.engine.Redeploy(ctx, "web", ""))
	if same.Status != api.StatusActive || same.Kind != api.KindRedeploy || same.Image != "web:1.0" || *same.SourceID != v1.ID {
		t.Errorf("redeploy without image: %+v", same)
	}

	bumped := s.finish(s.engine.Redeploy(ctx, "web", "web:1.1"))
	if bumped.Status != api.StatusActive || bumped.Version != "1.1" {
		t.Fatalf("redeploy with image: %+v", bumped)
	}
	for _, c := range s.rt.Containers() {
		if spec := s.rt.Spec(c.ID); c.Image != "web:1.1" || spec.Env["SECRET"] != "hunter2" {
			t.Errorf("only the image may change; env must carry over with its real values: %+v", spec)
		}
	}
	if len(s.rt.Containers()) != 2 {
		t.Errorf("replica count must carry over, got %d", len(s.rt.Containers()))
	}

	var bad *InvalidImageError
	if _, err := s.engine.Redeploy(ctx, "web", "Not A Valid Image"); !errors.As(err, &bad) {
		t.Errorf("err = %v, want InvalidImageError", err)
	}
}
