package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestBackupRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)

	run, err := s.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, now)
	if err != nil {
		t.Fatalf("CreateBackupRun: %v", err)
	}
	if run.ID == 0 || run.Status != api.BackupRunning || !run.StartedAt.Equal(now) {
		t.Fatalf("unexpected run: %+v", run)
	}
	// A backup that is still being taken cannot be verified or restored.
	if err := s.ClaimBackupRun(ctx, run.ID, api.BackupActivityVerify); !errors.Is(err, ErrBackupBusy) {
		t.Fatalf("got %v, want ErrBackupBusy", err)
	}

	run.Status = api.BackupSucceeded
	run.Volumes = []api.BackupVolume{{Volume: "data", SizeBytes: 2048}}
	run.Destinations = []string{"local", "s3"}
	run.Encrypted = true
	if err := s.FinishBackupRun(ctx, run, now.Add(time.Minute)); err != nil {
		t.Fatalf("FinishBackupRun: %v", err)
	}
	got, err := s.GetBackupRun(ctx, "db", run.ID)
	if err != nil {
		t.Fatalf("GetBackupRun: %v", err)
	}
	if got.Status != api.BackupSucceeded || len(got.Volumes) != 1 || got.Volumes[0].SizeBytes != 2048 || len(got.Destinations) != 2 ||
		!got.Encrypted || got.CompletedAt == nil || !got.CompletedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected run: %+v", got)
	}
	// Another application's id is not this application's backup.
	if _, err := s.GetBackupRun(ctx, "other", run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}

	if err := s.ClaimBackupRun(ctx, run.ID, api.BackupActivityVerify); err != nil {
		t.Fatalf("ClaimBackupRun: %v", err)
	}
	if err := s.ClaimBackupRun(ctx, run.ID, api.BackupActivityRestore); !errors.Is(err, ErrBackupBusy) {
		t.Fatalf("a backup being verified was claimed again: %v", err)
	}
	if err := s.FinishBackupVerify(ctx, run.ID, "", "ready to accept connections", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBackupRun(ctx, "db", run.ID)
	if got.Activity != "" || got.VerifiedAt == nil || got.VerifyError != "" || got.VerifyOutput == "" {
		t.Fatalf("unexpected run after a verification: %+v", got)
	}

	// A later verification that fails takes the earlier verdict back.
	if err := s.ClaimBackupRun(ctx, run.ID, api.BackupActivityVerify); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBackupVerify(ctx, run.ID, "exited with code 1", "", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBackupRun(ctx, "db", run.ID)
	if got.VerifiedAt != nil || got.VerifyError != "exited with code 1" {
		t.Fatalf("unexpected run after a failed verification: %+v", got)
	}
}

func TestBackupRunsOutliveTheirApplication(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, testApp("db", "postgres:17"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBackupRun(ctx, "db", api.BackupTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListBackupRuns(ctx, "db", 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("got %d runs after the application was deleted, want 1 (%v)", len(runs), err)
	}
}

func TestInterruptBackupRunsSettlesWhatWasInProgress(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()

	done, _ := s.CreateBackupRun(ctx, "db", api.BackupTriggerManual, now)
	done.Status = api.BackupSucceeded
	s.FinishBackupRun(ctx, done, now)
	s.ClaimBackupRun(ctx, done.ID, api.BackupActivityVerify)
	running, _ := s.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, now)

	interrupted, err := s.InterruptBackupRuns(ctx, now)
	if err != nil {
		t.Fatalf("InterruptBackupRuns: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != running.ID {
		t.Fatalf("got %+v, want the one backup that was running", interrupted)
	}
	got, _ := s.GetBackupRun(ctx, "db", running.ID)
	if got.Status != api.BackupFailed || got.Error == "" || got.CompletedAt == nil {
		t.Fatalf("the running backup was not failed: %+v", got)
	}
	got, _ = s.GetBackupRun(ctx, "db", done.ID)
	if got.Status != api.BackupSucceeded || got.Activity != "" || got.VerifyError == "" {
		t.Fatalf("the verification was not settled: %+v", got)
	}
}

func TestLastBackupRunAndListOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	var ids []int64
	for i, status := range []api.BackupRunStatus{api.BackupSucceeded, api.BackupSucceeded, api.BackupFailed} {
		run, _ := s.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, now.Add(time.Duration(i)*time.Hour))
		run.Status = status
		s.FinishBackupRun(ctx, run, now)
		ids = append(ids, run.ID)
	}
	runs, err := s.ListBackupRuns(ctx, "db", 2)
	if err != nil || len(runs) != 2 || runs[0].ID != ids[2] || runs[1].ID != ids[1] {
		t.Fatalf("unexpected listing: %+v, %v", runs, err)
	}
	last, err := s.LastBackupRun(ctx, "db", api.BackupSucceeded)
	if err != nil || last.ID != ids[1] {
		t.Fatalf("got %+v, %v; want the newer successful backup", last, err)
	}
	if _, err := s.LastBackupRun(ctx, "none", api.BackupSucceeded); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if err := s.DeleteBackupRun(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if last, _ := s.LastBackupRun(ctx, "db", api.BackupSucceeded); last.ID != ids[0] {
		t.Fatalf("got %d after deleting the newest, want %d", last.ID, ids[0])
	}
}

func TestSnapshotIsADatabaseThatOpensWithTheSameKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := []byte("an-encryption-key-of-32-bytes!!!")
	s, err := Open(ctx, filepath.Join(dir, "shipwick.db"), Options{EncryptionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now()); err != nil {
		t.Fatal(err)
	}

	copyPath := filepath.Join(dir, "copy.db")
	if _, err := s.Snapshot(ctx, copyPath); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// What is written after the snapshot is not in it.
	if _, err := s.CreateDeployment(ctx, testApp("later", "nginx:1"), time.Now()); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(ctx, copyPath, Options{EncryptionKey: key})
	if err != nil {
		t.Fatalf("open the snapshot: %v", err)
	}
	defer restored.Close()
	apps, err := restored.ListApplications(ctx)
	if err != nil || len(apps) != 1 || apps[0].Name != "my-api" {
		t.Fatalf("the snapshot holds %+v (%v), want my-api alone", apps, err)
	}
	if _, err := s.Snapshot(ctx, copyPath); err == nil {
		t.Fatal("a snapshot overwrote an existing file")
	}
}

func TestBackupInstallationIsMadeUpOnceAndKept(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	first, err := s.BackupInstallation(ctx)
	if err != nil || len(first) != 32 {
		t.Fatalf("BackupInstallation: %q, %v", first, err)
	}
	if again, _ := s.BackupInstallation(ctx); again != first {
		t.Fatalf("the installation changed its name: %q, then %q", first, again)
	}
	if other, _ := openTest(t).BackupInstallation(ctx); other == first {
		t.Fatal("two databases share one installation id")
	}
}

func TestReserveBackupRunIDsMovesTheNextIDPastTheOnesInUse(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	// Before the table has ever been written to.
	if err := s.ReserveBackupRunIDs(ctx, 41); err != nil {
		t.Fatalf("ReserveBackupRunIDs: %v", err)
	}
	run, _ := s.CreateBackupRun(ctx, "db", api.BackupTriggerManual, time.Now())
	if run.ID != 42 {
		t.Fatalf("the next backup is %d, want 42", run.ID)
	}
	// Never backwards.
	if err := s.ReserveBackupRunIDs(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.CreateBackupRun(ctx, "db", api.BackupTriggerManual, time.Now()); run.ID != 43 {
		t.Fatalf("the next backup is %d, want 43", run.ID)
	}
}
