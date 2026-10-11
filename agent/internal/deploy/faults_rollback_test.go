package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

// refuseWritesAtSecondReplica makes the database refuse every write from the
// moment the second container of a deployment other than `before` is created:
// one replica of the old version has been replaced by then, and the rollback
// the failure calls for cannot be recorded any more than the failure can.
func refuseWritesAtSecondReplica(h *harness, before int64) {
	created := 0
	h.rt.CreateHook = func(c docker.ContainerSpec) error {
		if c.DeploymentID == before {
			return nil
		}
		if created++; created == 2 {
			if err := h.store.RefuseWrites(context.Background(), true); err != nil {
				h.t.Errorf("RefuseWrites: %v", err)
			}
		}
		return nil
	}
}

func TestARolloutThatFailsHalfWayWhileTheDatabaseRefusesWritesIsMadeWholeAgainOnceItTakesThem(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	v1 := s.deploy(app("my-api", "my-api:1.0", 2))

	refuseWritesAtSecondReplica(s.harness, v1.ID)
	d := s.begin(app("my-api", "my-api:1.1", 2))
	s.engine.Wait()
	s.rt.CreateHook = nil

	// The first replica of 1.1 had replaced the first of 1.0 when the disk
	// filled. The rollback cannot begin — its first step is a write — so the
	// new replica is removed, and the old version runs short of the one it
	// lost: nothing can be put back without a row that says it exists.
	if up := running(s.rt.Containers()); up != "shipwick_my-api_1_2" || len(s.rt.Containers()) != 1 {
		t.Fatalf("containers = %s, want the old version's remaining replica alone", names(s.rt.Containers()))
	}
	if got := s.deployment(d.ID); got.Status != api.StatusHealthChecking || got.CompletedAt != nil {
		t.Fatalf("record while the database refuses writes: %s, completed %v; want it as the last write left it", got.Status, got.CompletedAt)
	}
	if !s.free("my-api") {
		t.Error("the application stays locked by a deployment that has ended")
	}

	// The supervisor tries to complete the old version every tick its backoff
	// allows, and keeps nothing it cannot record: desired 2, present 1, and no
	// container that nobody knows.
	for range 4 {
		s.advance(time.Second)
	}
	if got := names(s.rt.Containers()); got != "shipwick_my-api_1_2" {
		t.Fatalf("containers while the database refuses writes = %s, want only the replica that was left", got)
	}

	if err := s.store.RefuseWrites(ctx, false); err != nil {
		t.Fatal(err)
	}
	// The attempts backed off while they failed; the longest step is 30s.
	for range 4 {
		s.advance(10 * time.Second)
	}
	if got := running(s.rt.Containers()); got != "shipwick_my-api_1_1 shipwick_my-api_1_2" {
		t.Errorf("running once the database takes writes = %s, want the old version whole again", got)
	}
	containers := s.rt.Containers()
	for _, c := range containers {
		if c.DeploymentID != v1.ID || c.Image != "my-api:1.0" {
			t.Errorf("container %s is of deployment %d (%s), want the old version", c.Name, c.DeploymentID, c.Image)
		}
	}
	if events := strings.Join(s.appEvents(t, "my-api"), "\n"); !strings.Contains(events, "Recreated replica 1") {
		t.Errorf("events do not say that the replica was recreated:\n%s", events)
	}
	// The end of the deployment is written on the supervisor's tick as well
	// (see settle.go), which the loop does after a pass.
	s.engine.retrySettling(ctx)
	got := s.deployment(d.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("record once the database takes writes: %s, completed %v; want FAILED and completed", got.Status, got.CompletedAt)
	}
	if !strings.Contains(got.Error, "record replica: attempt to write a readonly database") {
		t.Errorf("error = %q; it must be why the deployment failed when it did", got.Error)
	}
	if a, _ := s.store.GetApplication(ctx, "my-api"); a.ActiveDeploymentID == nil || *a.ActiveDeploymentID != v1.ID {
		t.Errorf("the active deployment is %v, want the old version throughout", a.ActiveDeploymentID)
	}
}
