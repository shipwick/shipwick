package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testApp(name, image string) spec.App {
	return spec.App{
		Name:     name,
		Image:    image,
		Replicas: 2,
		Env:      map[string]string{"KEY": "value"},
		Health:   &spec.Health{Path: "/health", Interval: spec.Duration(10 * time.Second), Timeout: spec.Duration(3 * time.Second), Retries: 3},
		Restart:  spec.Restart{Policy: spec.RestartAlways},
		Deploy:   spec.Deploy{Strategy: spec.StrategyRolling},
	}
}

// advance walks a deployment through the happy path up to `to`.
func advance(t *testing.T, s *Store, id int64, to api.DeploymentStatus) {
	t.Helper()
	path := []api.DeploymentStatus{api.StatusPending, api.StatusBuilding, api.StatusStarting, api.StatusHealthChecking, api.StatusHealthy}
	for i := 0; i < len(path)-1; i++ {
		if err := s.TransitionDeployment(context.Background(), id, path[i], path[i+1], ""); err != nil {
			t.Fatalf("transition %s -> %s: %v", path[i], path[i+1], err)
		}
		if path[i+1] == to {
			return
		}
	}
}

func TestOpenPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "shipwick.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now()); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	s.Close()

	s, err = Open(ctx, path) // migrations must be a no-op the second time
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if _, err := s.GetApplication(ctx, "my-api"); err != nil {
		t.Errorf("application lost after reopen: %v", err)
	}
}

func TestCreateDeploymentAssignsSequencePerApplication(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	d1, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	d2, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), time.Now())
	other, err := s.CreateDeployment(ctx, testApp("web", "nginx:1"), time.Now())
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	if d1.Sequence != 1 || d2.Sequence != 2 || other.Sequence != 1 {
		t.Errorf("sequences = %d, %d, %d; want 1, 2, 1", d1.Sequence, d2.Sequence, other.Sequence)
	}
	if d1.ApplicationID != d2.ApplicationID || d1.ApplicationID == other.ApplicationID {
		t.Error("deployments attached to the wrong applications")
	}
	if d2.Version != "2" || d2.Status != api.StatusPending {
		t.Errorf("unexpected deployment: %+v", d2)
	}
}

func TestGetDeploymentRoundTripsSpecAndUTC(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	local := time.Date(2026, 3, 1, 15, 4, 5, 0, time.FixedZone("TRT", 3*3600))
	created, err := s.CreateDeployment(ctx, testApp("api", "nginx:1"), local)
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	got, err := s.GetDeployment(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}

	if got.StartedAt.Location() != time.UTC || !got.StartedAt.Equal(local) {
		t.Errorf("StartedAt = %v, want %v in UTC", got.StartedAt, local)
	}
	if got.Spec.Env["KEY"] != "value" || got.Spec.Health.Interval.Std() != 10*time.Second || got.Spec.Replicas != 2 {
		t.Errorf("spec did not round trip: %+v", got.Spec)
	}
	if got.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil", got.CompletedAt)
	}

	if _, err := s.GetDeployment(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing deployment: err = %v, want ErrNotFound", err)
	}
}

func TestTransitionDeploymentIsCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	if err := s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, ""); err != nil {
		t.Fatalf("transition: %v", err)
	}
	// A stale writer still believes the deployment is PENDING.
	err := s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusFailed, "boom")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition: err = %v, want ErrConflict", err)
	}

	if err := s.TransitionDeployment(ctx, d.ID, api.StatusBuilding, api.StatusFailed, "pull failed"); err != nil {
		t.Fatalf("transition to FAILED: %v", err)
	}
	got, _ := s.GetDeployment(ctx, d.ID)
	if got.Status != api.StatusFailed || got.Error != "pull failed" {
		t.Errorf("unexpected deployment after failure: %+v", got)
	}
	if got.CompletedAt != nil {
		t.Error("a transition alone must not stamp completion: cleanup is still pending")
	}
}

func TestCompleteDeploymentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	first := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := s.CompleteDeployment(ctx, d.ID, first); err != nil {
		t.Fatalf("CompleteDeployment: %v", err)
	}
	if err := s.CompleteDeployment(ctx, d.ID, first.Add(time.Hour)); err != nil {
		t.Fatalf("second CompleteDeployment: %v", err)
	}
	got, _ := s.GetDeployment(ctx, d.ID)
	if got.CompletedAt == nil || !got.CompletedAt.Equal(first) {
		t.Errorf("CompletedAt = %v, want the first stamp %v", got.CompletedAt, first)
	}
}

func TestCompleteAllDeployments(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d1, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	d2, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), time.Now())
	early := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.CompleteDeployment(ctx, d1.ID, early)

	if err := s.CompleteAllDeployments(ctx, early.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteAllDeployments: %v", err)
	}
	got1, _ := s.GetDeployment(ctx, d1.ID)
	got2, _ := s.GetDeployment(ctx, d2.ID)
	if !got1.CompletedAt.Equal(early) {
		t.Error("an existing completion time must be preserved")
	}
	if got2.CompletedAt == nil {
		t.Error("the unfinished deployment should have been stamped")
	}
}

func TestActivateDeploymentSupersedesPrevious(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	d1, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	advance(t, s, d1.ID, api.StatusHealthy)
	prev, err := s.ActivateDeployment(ctx, d1.ID, time.Now())
	if err != nil {
		t.Fatalf("ActivateDeployment: %v", err)
	}
	if prev != nil {
		t.Errorf("first activation returned a previous deployment: %+v", prev)
	}

	d2, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), time.Now())
	advance(t, s, d2.ID, api.StatusHealthy)
	prev, err = s.ActivateDeployment(ctx, d2.ID, time.Now())
	if err != nil {
		t.Fatalf("ActivateDeployment: %v", err)
	}
	if prev == nil || prev.ID != d1.ID || prev.Status != api.StatusSuperseded {
		t.Errorf("previous = %+v, want superseded deployment %d", prev, d1.ID)
	}

	app, _ := s.GetApplication(ctx, "api")
	if app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != d2.ID {
		t.Errorf("ActiveDeploymentID = %v, want %d", app.ActiveDeploymentID, d2.ID)
	}
	got, _ := s.GetDeployment(ctx, d2.ID)
	if got.Status != api.StatusActive {
		t.Errorf("unexpected active deployment: %+v", got)
	}
	if got.CompletedAt != nil {
		t.Error("activation must not stamp completion: the old version is still to be retired")
	}
}

func TestActivateDeploymentRequiresHealthy(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	if _, err := s.ActivateDeployment(ctx, d.ID, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	app, _ := s.GetApplication(ctx, "api")
	if app.ActiveDeploymentID != nil {
		t.Error("failed activation must not repoint the application")
	}
}

func TestListDeploymentsFilters(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	d2, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), time.Now())
	s.CreateDeployment(ctx, testApp("web", "nginx:1"), time.Now())
	advance(t, s, d2.ID, api.StatusBuilding)

	all, _ := s.ListDeployments(ctx, DeploymentFilter{})
	if len(all) != 3 || all[0].ID < all[1].ID {
		t.Errorf("want 3 deployments newest first, got %+v", all)
	}
	byApp, _ := s.ListDeployments(ctx, DeploymentFilter{Application: "api"})
	if len(byApp) != 2 {
		t.Errorf("filter by application: got %d, want 2", len(byApp))
	}
	byStatus, _ := s.ListDeployments(ctx, DeploymentFilter{Statuses: []api.DeploymentStatus{api.StatusBuilding}})
	if len(byStatus) != 1 || byStatus[0].ID != d2.ID {
		t.Errorf("filter by status: got %+v", byStatus)
	}
	limited, _ := s.ListDeployments(ctx, DeploymentFilter{Limit: 1})
	if len(limited) != 1 {
		t.Errorf("limit: got %d, want 1", len(limited))
	}
}

func TestReplicas(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	for i := 1; i <= 2; i++ {
		r := Replica{DeploymentID: d.ID, Index: i, ContainerID: "c" + string(rune('0'+i)), ContainerName: "name"}
		if err := s.AddReplica(ctx, r, time.Now()); err != nil {
			t.Fatalf("AddReplica: %v", err)
		}
	}
	if err := s.MarkReplicaRemoved(ctx, "c1", time.Now()); err != nil {
		t.Fatalf("MarkReplicaRemoved: %v", err)
	}
	if err := s.MarkReplicaRemoved(ctx, "unknown", time.Now()); err != nil {
		t.Errorf("MarkReplicaRemoved must be idempotent: %v", err)
	}

	replicas, _ := s.ListReplicas(ctx, d.ID)
	if len(replicas) != 1 || replicas[0].ContainerID != "c2" {
		t.Errorf("replicas = %+v, want only c2", replicas)
	}
}

func TestEventsAndCascadeDelete(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	s.AddEvent(ctx, d.ApplicationID, &d.ID, api.LevelInfo, api.EventStep, "Pulled image", time.Now())
	s.AddEvent(ctx, d.ApplicationID, &d.ID, api.LevelError, api.EventState, "Deployment failed", time.Now())
	events, err := s.ListDeploymentEvents(ctx, d.ID)
	if err != nil || len(events) != 2 || events[0].Message != "Pulled image" {
		t.Fatalf("events = %+v, err = %v", events, err)
	}

	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := s.GetDeployment(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deployment should be deleted with its application, err = %v", err)
	}
	events, _ = s.ListDeploymentEvents(ctx, d.ID)
	if len(events) != 0 {
		t.Errorf("events should be deleted with their application, got %d", len(events))
	}
	if err := s.DeleteApplication(ctx, d.ApplicationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestSetDesiredState(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	if err := s.SetDesiredState(ctx, d.ApplicationID, api.DesiredStopped, time.Now()); err != nil {
		t.Fatalf("SetDesiredState: %v", err)
	}
	app, _ := s.GetApplication(ctx, "api")
	if app.DesiredState != api.DesiredStopped {
		t.Errorf("DesiredState = %q", app.DesiredState)
	}
}

func TestMigrationsUpgradeAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")

	// A database as the first release left it: only migration 1 applied, and
	// rows written with the columns that existed then.
	all := migrations
	migrations = all[:1]
	old, err := Open(ctx, path)
	migrations = all
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	ts := formatTime(time.Now())
	for _, stmt := range []string{
		`INSERT INTO applications (id, name, created_at, updated_at) VALUES (1, 'api', '` + ts + `', '` + ts + `')`,
		`INSERT INTO deployments (id, application_id, sequence, version, image, spec, status, started_at)
		 VALUES (1, 1, 1, '1', 'nginx:1', '{"name":"api","image":"nginx:1","replicas":1}', 'ACTIVE', '` + ts + `')`,
		`INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, container_name, created_at)
		 VALUES (1, 1, 'c1', 'n1', '` + ts + `')`,
	} {
		if _, err := old.db.ExecContext(ctx, stmt); err != nil {
			old.Close()
			t.Fatalf("seed v1 data: %v", err)
		}
	}
	old.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open with newer migrations: %v", err)
	}
	defer s.Close()

	replicas, err := s.ListReplicas(ctx, 1)
	if err != nil || len(replicas) != 1 || replicas[0].Restarts != 0 {
		t.Errorf("replicas must survive the upgrade with defaults: %+v, %v", replicas, err)
	}
	d, err := s.GetDeployment(ctx, 1)
	if err != nil || d.Kind != api.KindDeploy || d.SourceID != nil {
		t.Errorf("deployments from before `kind` existed are plain deploys: %+v, %v", d, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")
	s, _ := Open(ctx, path)
	s.db.ExecContext(ctx, "PRAGMA user_version = 999")
	s.Close()

	if _, err := Open(ctx, path); err == nil {
		t.Error("an agent must not run against a schema from its future")
	}
}

func TestReplicaRestartCount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	s.AddReplica(ctx, Replica{DeploymentID: d.ID, Index: 1, ContainerID: "c1", ContainerName: "n1"}, time.Now())

	for range 3 {
		if err := s.IncrementReplicaRestarts(ctx, "c1"); err != nil {
			t.Fatalf("IncrementReplicaRestarts: %v", err)
		}
	}
	replicas, _ := s.ListReplicas(ctx, d.ID)
	if replicas[0].Restarts != 3 {
		t.Errorf("Restarts = %d, want 3", replicas[0].Restarts)
	}
}

func TestApplicationEventsAndPruning(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	s.AddEvent(ctx, d.ApplicationID, &d.ID, api.LevelInfo, api.EventStep, "deployment event", time.Now())
	for i := range 10 {
		s.AddEvent(ctx, d.ApplicationID, nil, api.LevelWarn, api.EventApp, "restart "+string(rune('0'+i)), time.Now())
	}

	events, err := s.ListApplicationEvents(ctx, d.ApplicationID, 3)
	if err != nil || len(events) != 3 || events[0].Message != "restart 9" || events[2].Message != "restart 7" {
		t.Fatalf("want the 3 newest application events, newest first; got %+v, %v", events, err)
	}
	for _, e := range events {
		if e.DeploymentID != nil {
			t.Errorf("deployment events do not belong in the application feed: %+v", e)
		}
	}

	if err := s.PruneApplicationEvents(ctx, d.ApplicationID, 4); err != nil {
		t.Fatalf("PruneApplicationEvents: %v", err)
	}
	events, _ = s.ListApplicationEvents(ctx, d.ApplicationID, 100)
	if len(events) != 4 || events[3].Message != "restart 6" {
		t.Errorf("pruning should keep the 4 newest, got %+v", events)
	}
	if depEvents, _ := s.ListDeploymentEvents(ctx, d.ID); len(depEvents) != 1 {
		t.Error("pruning must never touch deployment events")
	}
}

func TestTransitionKeepsTheRecordedError(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusFailed, "replica 2 exited with code 1")
	if err := s.TransitionDeployment(ctx, d.ID, api.StatusFailed, api.StatusRollback, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDeployment(ctx, d.ID)
	if got.Status != api.StatusRollback || got.Error != "replica 2 exited with code 1" {
		t.Errorf("after FAILED → ROLLBACK: %s %q; rolling back must not erase why", got.Status, got.Error)
	}

	s.TransitionDeployment(ctx, d.ID, api.StatusRollback, api.StatusFailed, "the rollback failed too")
	if got, _ = s.GetDeployment(ctx, d.ID); got.Error != "the rollback failed too" {
		t.Errorf("a new message should replace the old one, got %q", got.Error)
	}
}

func TestCreateDeploymentFromRecordsItsOrigin(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	first, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())

	rollback, err := s.CreateDeploymentFrom(ctx, testApp("api", "nginx:1"), api.KindRollback, &first.ID, time.Now())
	if err != nil {
		t.Fatalf("CreateDeploymentFrom: %v", err)
	}
	got, _ := s.GetDeployment(ctx, rollback.ID)
	if got.Kind != api.KindRollback || got.SourceID == nil || *got.SourceID != first.ID || got.Sequence != 2 {
		t.Errorf("unexpected record: %+v", got)
	}
	if plain, _ := s.GetDeployment(ctx, first.ID); plain.Kind != api.KindDeploy || plain.SourceID != nil {
		t.Errorf("a plain deployment has no source: %+v", plain)
	}
}
