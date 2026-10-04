package deploy

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// besideSpecs records the containers created next to another one.
func besideSpecs(h *harness) func() []docker.ContainerSpec {
	var mu sync.Mutex
	var specs []docker.ContainerSpec
	h.rt.CreateHook = func(s docker.ContainerSpec) error {
		if s.Beside != nil {
			mu.Lock()
			defer mu.Unlock()
			specs = append(specs, s)
		}
		return nil
	}
	return func() []docker.ContainerSpec {
		mu.Lock()
		defer mu.Unlock()
		return append([]docker.ContainerSpec(nil), specs...)
	}
}

func TestBeforeInAContainerRunsBesideTheReplicaWithItsVolumesAndIsRemoved(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	created := besideSpecs(h)
	dump := []string{"pg_dump", "-h", "localhost", "-f", "/var/lib/data/backup.sql"}
	replica := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: dump, BeforeIn: spec.BeforeInContainer}, map[string]string{"a.txt": "alpha"})

	run := h.backup("db")
	if run.Status != api.BackupSucceeded || len(run.Volumes) != 1 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if calls := h.rt.ExecCalls(); len(calls) != 0 {
		t.Errorf("the command also ran inside the replica: %+v", calls)
	}
	specs := created()
	if len(specs) != 1 {
		t.Fatalf("containers beside the replica: %+v", specs)
	}
	s := specs[0]
	if s.Beside.Container != replica || !reflect.DeepEqual(s.Command, dump) || s.Image != "postgres:17" || s.Env["SECRET"] != "hunter2" {
		t.Errorf("spec = %+v; want the replica's network, image and environment, and the command", s)
	}
	if !reflect.DeepEqual(s.Mounts, []docker.Mount{{Volume: "data", Path: "/var/lib/data"}}) {
		t.Errorf("mounts = %+v; want the application's volumes where the replica has them", s.Mounts)
	}
	if s.Job == nil || s.Job.Name != beforeJobName || s.Job.RunID != run.ID || !s.Init || len(s.Publish) != 0 {
		t.Errorf("spec = %+v", s)
	}
	if spec.ValidateJobName(beforeJobName) == nil {
		t.Errorf("%q could be the name of a job in deploy.yaml, and their containers would share a name", beforeJobName)
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("the command's container was not removed: %+v", left)
	}
}

func TestAFailingBeforeInAContainerFailsTheBackupWithItsLastLine(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.rt.JobExits[beforeJobName] = 3
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"}, BeforeIn: spec.BeforeInContainer}, nil)

	run := h.backup("db")
	if run.Status != api.BackupFailed || len(run.Volumes) != 0 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if want := "backups.before exited 3: log line from shipwick_db_job_backup_before_1; nothing was archived"; run.Error != want {
		t.Errorf("error = %q, want %q", run.Error, want)
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("the command's container was not removed: %+v", left)
	}
}

func TestBeforeInAContainerIsEndedAtItsTimeout(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.rt.HoldJobs = true
	replica := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"},
		BeforeTimeout: spec.Duration(20 * time.Millisecond), BeforeIn: spec.BeforeInContainer}, nil)

	run := h.backup("db")
	if run.Status != api.BackupFailed || len(run.Volumes) != 0 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if !strings.Contains(run.Error, "backups.before did not finish within 20ms and was stopped; nothing was archived") ||
		!strings.Contains(run.Error, "backups.before_timeout") {
		t.Errorf("error = %q", run.Error)
	}
	// Nothing of the command is left, and the replica never noticed. It was
	// asked to stop before it was removed.
	if h.rt.StoppedAt("shipwick_db_job_backup_before_1").IsZero() {
		t.Error("the command's container was removed without being stopped first")
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("the command's container outlived its timeout: %+v", left)
	}
	if c, err := h.rt.InspectContainer(context.Background(), replica); err != nil || !c.Running {
		t.Errorf("replica = %+v, err = %v", c, err)
	}
	// The application is free again: the next backup begins.
	h.rt.HoldJobs = false
	if next := h.backup("db"); next.Status != api.BackupSucceeded {
		t.Errorf("the backup after it: %+v", next)
	}
}

func TestBeforeInAContainerNeedsARunningReplica(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"}, BeforeIn: spec.BeforeInContainer}, nil)
	if err := h.engine.Stop(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}

	run := h.backup("db")
	if run.Status != api.BackupFailed || !strings.Contains(run.Error, "backups.before could not run") || !strings.Contains(run.Error, "nothing was archived") {
		t.Errorf("unexpected run: %+v", run)
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("the command's container was not removed: %+v", left)
	}
}

func TestAnAgentThatShutsDownEndsBeforeInAContainer(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.rt.HoldJobs = true
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"}, BeforeIn: spec.BeforeInContainer}, nil)

	began := make(chan struct{})
	h.rt.CreateHook = func(s docker.ContainerSpec) error {
		if s.Beside != nil {
			close(began)
		}
		return nil
	}
	if _, err := h.engine.StartBackup(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-began:
	case <-time.After(10 * time.Second):
		t.Fatal("no container was created beside the replica")
	}
	if err := h.engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("the command's container outlived the agent: %+v", left)
	}
}

func TestBeforeStaysInTheReplicaUnlessTheApplicationAsks(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	created := besideSpecs(h)
	for _, in := range []string{"", spec.BeforeInReplica} {
		a := backupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"}, BeforeIn: in})
		h.deploy(a)
		if run := h.backup("db"); run.Status != api.BackupSucceeded {
			t.Fatalf("before_in %q: %+v", in, run)
		}
	}
	if calls := h.rt.ExecCalls(); len(calls) != 2 {
		t.Errorf("exec calls = %+v, want one for each backup", calls)
	}
	if specs := created(); len(specs) != 0 {
		t.Errorf("a container was created beside the replica: %+v", specs)
	}
}
