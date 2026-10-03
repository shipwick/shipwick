package deploy

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/backup/backuptest"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Deriving a key at full strength takes most of a second; these tests are
// about what is backed up, not about that.
func init() { backupfile.Iterations = 1000 }

const (
	testPassphrase = "correct horse battery staple"
	testStateKey   = "an-encryption-key-of-32-bytes!!!"
)

// withBackups gives a harness somewhere to keep backups and returns the
// directory.
func withBackups(h *harness, passphrase string, bucket *backup.S3) string {
	dir := h.t.TempDir()
	h.engine.opts.Backups = BackupOptions{Storage: backup.New(dir, bucket, passphrase), EncryptionKey: []byte(testStateKey)}
	return dir
}

func fakeBucket(t *testing.T) (*backup.S3, *backuptest.S3) {
	t.Helper()
	fake := backuptest.NewS3(t)
	bucket, err := backup.NewS3(backup.S3Config{
		Endpoint: fake.URL, Bucket: backuptest.Bucket, Region: backuptest.Region,
		AccessKeyID: backuptest.AccessKeyID, SecretAccessKey: backuptest.SecretAccessKey,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return bucket, fake
}

func backupApp(b spec.Backups) spec.App {
	a := volumeApp()
	if b.Keep == 0 {
		b.Keep = spec.DefaultBackupKeep
	}
	a.Backups = &b
	return a
}

// deployBackupApp deploys the stateful application with a `backups` block and
// writes files into its volume.
func (h *harness) deployBackupApp(b spec.Backups, files map[string]string) string {
	h.t.Helper()
	h.deploy(backupApp(b))
	id := h.rt.Containers()[0].ID
	for name, content := range files {
		if err := h.rt.PutFile(id, "/var/lib/data/"+name, []byte(content)); err != nil {
			h.t.Fatalf("PutFile: %v", err)
		}
	}
	return id
}

// backup takes a backup by hand and waits for it.
func (h *harness) backup(name string) api.BackupRun {
	h.t.Helper()
	run, err := h.engine.StartBackup(context.Background(), name)
	if err != nil {
		h.t.Fatalf("StartBackup: %v", err)
	}
	h.engine.Wait()
	return h.backupRun(name, run.ID).BackupRun
}

func (h *harness) backupRun(name string, id int64) api.BackupRunDetail {
	h.t.Helper()
	run, err := h.engine.BackupRun(context.Background(), name, id)
	if err != nil {
		h.t.Fatalf("BackupRun: %v", err)
	}
	return run
}

func (h *harness) backups(name string) []api.BackupRun {
	h.t.Helper()
	runs, err := h.engine.Backups(context.Background(), name, 0)
	if err != nil {
		h.t.Fatalf("Backups: %v", err)
	}
	return runs
}

func (h *harness) archive(name string, id int64, volume string) map[string]string {
	h.t.Helper()
	rc, _, err := h.engine.OpenBackupArchive(context.Background(), name, id, volume)
	if err != nil {
		h.t.Fatalf("OpenBackupArchive: %v", err)
	}
	defer rc.Close()
	return untar(h.t, rc)
}

func at(hour, minute, second int) time.Time {
	return time.Date(2026, 3, 1, hour, minute, second, 0, time.UTC)
}

func TestABackupArchivesEveryVolumeAndRecordsWhatItKept(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	a := volumeApp()
	a.Volumes = append(a.Volumes, spec.Volume{Name: "uploads", Path: "/srv/uploads"})
	h.deploy(a)
	id := h.rt.Containers()[0].ID
	h.rt.PutFile(id, "/var/lib/data/a.txt", []byte("alpha"))
	h.rt.PutFile(id, "/srv/uploads/logo.png", []byte("png"))

	run := h.backup("db")
	if run.Status != api.BackupSucceeded || run.Trigger != api.BackupTriggerManual || run.CompletedAt == nil || run.Error != "" || run.Encrypted {
		t.Fatalf("unexpected run: %+v", run)
	}
	if len(run.Volumes) != 2 || run.Volumes[0].Volume != "data" || run.Volumes[1].Volume != "uploads" || run.Volumes[0].SizeBytes == 0 {
		t.Fatalf("unexpected volumes: %+v", run.Volumes)
	}
	if len(run.Destinations) != 1 || run.Destinations[0] != api.BackupDestinationLocal {
		t.Fatalf("unexpected destinations: %v", run.Destinations)
	}
	for _, volume := range []string{"data", "uploads"} {
		if _, err := os.Stat(filepath.Join(dir, "db", "1", volume+".tar")); err != nil {
			t.Errorf("no archive for %s where the documentation says it is: %v", volume, err)
		}
	}
	if got := h.archive("db", run.ID, "data"); len(got) != 1 || got["a.txt"] != "alpha" {
		t.Errorf("data archive = %v", got)
	}
	if got := h.archive("db", run.ID, "uploads"); got["logo.png"] != "png" {
		t.Errorf("uploads archive = %v", got)
	}
	if _, _, err := h.engine.OpenBackupArchive(context.Background(), "db", run.ID, "logs"); !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("an archive of a volume the backup does not hold: %v", err)
	}
	if !h.rt.Containers()[0].Running {
		t.Error("a backup without stop: true stopped the application")
	}
}

func TestABackupNeedsVolumesAndAPlaceToGo(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	ctx := context.Background()
	if _, err := h.engine.StartBackup(ctx, "my-api"); !errors.Is(err, ErrBackupsDisabled) {
		t.Fatalf("without storage: %v", err)
	}
	withBackups(h, "", nil)
	if _, err := h.engine.StartBackup(ctx, "my-api"); !errors.Is(err, ErrNoVolumes) {
		t.Fatalf("without volumes: %v", err)
	}
	if _, err := h.engine.StartBackup(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown application: %v", err)
	}
	// The refusals let go of the application.
	if err := h.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatalf("Stop after refused backups: %v", err)
	}
}

func TestTheScheduleFiresOncePerDueMinute(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()

	h.engine.scheduleBackups(ctx, at(2, 59, 0))
	h.engine.Wait()
	if runs := h.backups("db"); len(runs) != 0 {
		t.Fatalf("%d backups before the schedule fired", len(runs))
	}
	for _, second := range []int{0, 1, 30} {
		h.engine.scheduleBackups(ctx, at(3, 0, second))
		h.engine.Wait()
	}
	h.engine.scheduleBackups(ctx, at(3, 1, 0))
	h.engine.Wait()
	runs := h.backups("db")
	if len(runs) != 1 || runs[0].Trigger != api.BackupTriggerSchedule || runs[0].Status != api.BackupSucceeded {
		t.Fatalf("got %+v, want one scheduled backup", runs)
	}
}

func TestAScheduledBackupWaitsForABusyApplication(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, nil)
	ctx := context.Background()
	h.engine.scheduleBackups(ctx, at(2, 59, 0))

	// A deployment, say, holds the application as the schedule fires.
	if wait, err := h.engine.tryLock("db", false); wait != nil || err != nil {
		t.Fatalf("tryLock: %v", err)
	}
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	h.engine.scheduleBackups(ctx, at(3, 1, 0))
	if runs := h.backups("db"); len(runs) != 0 {
		t.Fatalf("a backup started while the application was busy: %+v", runs)
	}
	h.engine.unlock("db")

	// Not lost, not failed: taken once the application is free.
	h.engine.scheduleBackups(ctx, at(3, 2, 0))
	h.engine.Wait()
	runs := h.backups("db")
	if len(runs) != 1 || runs[0].Status != api.BackupSucceeded {
		t.Fatalf("got %+v, want the backup that was due at 03:00", runs)
	}
}

func TestStoppedApplicationsAreNotBackedUpOnSchedule(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "* * * * *"}, nil)
	ctx := context.Background()
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	h.engine.scheduleBackups(ctx, at(3, 1, 0))
	h.engine.Wait()
	if runs := h.backups("db"); len(runs) != 0 {
		t.Fatalf("got %+v", runs)
	}
	// By hand it still works: the archive is read from the stopped container.
	if run := h.backup("db"); run.Status != api.BackupSucceeded {
		t.Fatalf("a manual backup of a stopped application: %+v", run)
	}
}

func TestBeforeRunsInTheReplicaBeforeTheArchiveIsTaken(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	dump := []string{"pg_dump", "-f", "/var/lib/data/backup.sql"}
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: dump}, nil)

	run := h.backup("db")
	if run.Status != api.BackupSucceeded {
		t.Fatalf("unexpected run: %+v", run)
	}
	calls := h.rt.ExecCalls()
	if len(calls) != 1 || calls[0].Container != "shipwick_db_1_1" || strings.Join(calls[0].Cmd, " ") != strings.Join(dump, " ") {
		t.Fatalf("exec calls = %+v", calls)
	}
}

func TestAFailingBeforeFailsTheBackupAndArchivesNothing(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	rec := notified(h)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "app"}}, map[string]string{"a.txt": "alpha"})
	h.rt.SetExecResult("pg_dump app", dockertest.ExecResult{ExitCode: 1, Output: "noise\npg_dump: error: connection refused\n"})
	notifications := len(rec.Events())

	run := h.backup("db")
	if run.Status != api.BackupFailed || len(run.Volumes) != 0 || len(run.Destinations) != 0 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if !strings.Contains(run.Error, "backups.before exited 1: pg_dump: error: connection refused") {
		t.Errorf("error = %q; it should carry the exit code and the command's last line", run.Error)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db")); len(entries) != 0 {
		t.Errorf("a failed backup left files behind: %v", entries)
	}
	if !strings.Contains(strings.Join(h.appEventMessages("db"), "\n"), "Backup #1 failed") {
		t.Errorf("no event for the failure: %v", h.appEventMessages("db"))
	}
	// Whoever asked for it by hand has just been told.
	if n := len(rec.Events()); n != notifications {
		t.Errorf("a backup taken by hand notified: %+v", rec.Events()[notifications:])
	}

	ctx := context.Background()
	h.engine.scheduleBackups(ctx, at(2, 59, 0))
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	h.engine.Wait()
	ev := lastEvent(t, rec, notify.BackupFailed)
	if ev.Application != "db" || !strings.Contains(ev.Message, "the scheduled backup failed") || !strings.Contains(ev.Message, "shipwick backups db") {
		t.Errorf("unexpected notification: %+v", ev)
	}
}

func TestStopStopsTheApplicationForTheArchiveAndStartsItAgain(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(h, "", bucket)
	id := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Stop: true, Before: []string{"checkpoint"}}, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()

	uploading, release := h.holdUploads(fake)
	run, err := h.engine.StartBackup(ctx, "db")
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	<-uploading
	// The archive is on its way: the command ran while the replica did, and
	// the replica is stopped now.
	if calls := h.rt.ExecCalls(); len(calls) != 1 || calls[0].Cmd[0] != "checkpoint" {
		t.Errorf("exec calls = %+v", calls)
	}
	if c, _ := h.rt.InspectContainer(ctx, id); c.Running {
		t.Error("the replica runs while its volume is archived")
	}
	// It is a user operation: another one is refused, and the supervisor
	// leaves the application alone instead of restarting what is "down".
	if err := h.engine.Stop(ctx, "db"); !errors.Is(err, ErrBusy) {
		t.Errorf("Stop during the backup = %v, want ErrBusy", err)
	}
	h.engine.sup.tick(ctx, time.Now())
	if c, _ := h.rt.InspectContainer(ctx, id); c.Running {
		t.Error("the supervisor restarted a replica that a backup had stopped")
	}
	release()
	h.engine.Wait()

	if got := h.backupRun("db", run.ID); got.Status != api.BackupSucceeded {
		t.Fatalf("unexpected run: %+v", got)
	}
	if c, _ := h.rt.InspectContainer(ctx, id); !c.Running || h.rt.Starts(id) != 2 {
		t.Errorf("the replica was not started again: running=%v starts=%d", c.Running, h.rt.Starts(id))
	}
	if app, _ := h.store.GetApplication(ctx, "db"); app.DesiredState != api.DesiredRunning {
		t.Errorf("desired state = %s; a backup must not change what the application is meant to do", app.DesiredState)
	}
	if events := strings.Join(h.appEventMessages("db"), "\n"); !strings.Contains(events, "Stopped for a backup (backups.stop) and started again after") {
		t.Errorf("nothing in the event feed says why the application was away:\n%s", events)
	}
}

func TestStopStartsTheApplicationAgainWhenTheBackupFails(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, "", bucket)
	// The bucket is this installation's; it is the archive it refuses.
	if err := h.engine.prepareBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.FailPuts(true)
	id := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Stop: true}, map[string]string{"a.txt": "alpha"})

	run := h.backup("db")
	if run.Status != api.BackupFailed || !strings.Contains(run.Error, "volume data") || !strings.Contains(run.Error, "HTTP 500") {
		t.Fatalf("unexpected run: %+v", run)
	}
	if c, _ := h.rt.InspectContainer(context.Background(), id); !c.Running {
		t.Error("the application stayed stopped after a failed backup")
	}
	// Nothing is kept of a backup that did not reach every destination.
	if entries, _ := os.ReadDir(filepath.Join(dir, "db")); len(entries) != 0 {
		t.Errorf("left on the server: %v", entries)
	}
}

func TestBackupsGoToTheBucketAsWell(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})

	run := h.backup("db")
	if strings.Join(run.Destinations, ",") != "local,s3" {
		t.Fatalf("destinations = %v", run.Destinations)
	}
	if keys := bucketKeys(fake); keys != "db/1/data.tar" {
		t.Fatalf("bucket holds %v", keys)
	}
	// The server's disk is gone; the bucket still restores.
	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	if got := h.archive("db", run.ID, "data"); got["a.txt"] != "alpha" {
		t.Errorf("archive from the bucket = %v", got)
	}
}

func TestBackupsAreEncryptedWithAPassphraseAndReadBackInTheClear(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, testPassphrase, bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha, a secret"})

	run := h.backup("db")
	if !run.Encrypted || run.Status != api.BackupSucceeded {
		t.Fatalf("unexpected run: %+v", run)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "db", "1", "data.tar.enc"))
	if err != nil || !backupfile.IsEncrypted(onDisk) || strings.Contains(string(onDisk), "a secret") {
		t.Fatalf("the archive on the server is not encrypted (%v)", err)
	}
	if inBucket := fake.Objects()["db/1/data.tar.enc"]; !backupfile.IsEncrypted(inBucket) || strings.Contains(string(inBucket), "a secret") {
		t.Fatal("the archive in the bucket is not encrypted")
	}
	if got := h.archive("db", run.ID, "data"); got["a.txt"] != "alpha, a secret" {
		t.Errorf("archive = %v", got)
	}
}

func TestRetentionKeepsTheNewestSuccessfulBackupsInBothPlaces(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Keep: 2}, map[string]string{"a.txt": "alpha"})

	for range 4 {
		h.backup("db")
	}
	runs := h.backups("db")
	if len(runs) != 2 || runs[0].ID != 4 || runs[1].ID != 3 {
		t.Fatalf("kept %+v, want backups 4 and 3", runs)
	}
	if keys := bucketKeys(fake); keys != "db/3/data.tar db/4/data.tar" {
		t.Errorf("bucket holds: %s", keys)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "db"))
	if len(entries) != 2 || entries[0].Name() != "3" || entries[1].Name() != "4" {
		t.Errorf("the server holds: %v", entries)
	}
}

func TestRetentionNeverDeletesTheOnlySuccessfulBackup(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Keep: 1, Before: []string{"dump"}}, map[string]string{"a.txt": "alpha"})

	good := h.backup("db")
	h.rt.SetExecResult("dump", dockertest.ExecResult{ExitCode: 1})
	for range 3 {
		if run := h.backup("db"); run.Status != api.BackupFailed {
			t.Fatalf("unexpected run: %+v", run)
		}
	}
	runs := h.backups("db")
	if len(runs) != 4 || runs[3].ID != good.ID || runs[3].Status != api.BackupSucceeded {
		t.Fatalf("got %+v; three failures must not push out the one backup that worked", runs)
	}
	if got := h.archive("db", good.ID, "data"); got["a.txt"] != "alpha" {
		t.Errorf("the surviving backup does not read back: %v", got)
	}
}

func TestOnlyTheLastFailuresAreRemembered(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"dump"}}, nil)
	h.rt.SetExecResult("dump", dockertest.ExecResult{ExitCode: 1})
	for range backupFailuresKept + 3 {
		h.backup("db")
	}
	if runs := h.backups("db"); len(runs) != backupFailuresKept {
		t.Fatalf("%d failed backups remembered, want %d", len(runs), backupFailuresKept)
	}
}

func TestAManualBackupHoldsTheApplicationUntilItIsWritten(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()

	uploading, release := h.holdUploads(fake)
	run, err := h.engine.StartBackup(ctx, "db")
	if err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	<-uploading
	if got := h.backupRun("db", run.ID); got.Status != api.BackupRunning || got.CompletedAt != nil {
		t.Errorf("unexpected run while it uploads: %+v", got)
	}
	if _, err := h.engine.Deploy(ctx, backupApp(spec.Backups{Schedule: "0 3 * * *"})); !errors.Is(err, ErrBusy) {
		t.Errorf("Deploy during a backup = %v, want ErrBusy", err)
	}
	if _, err := h.engine.StartBackup(ctx, "db"); !errors.Is(err, ErrBusy) {
		t.Errorf("a second backup during the first = %v, want ErrBusy", err)
	}
	// A backup that is still being taken is nothing to restore from yet.
	if _, err := h.engine.VerifyBackup(ctx, "db", run.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("VerifyBackup during the backup = %v, want ErrBusy", err)
	}
	release()
	h.engine.Wait()
	if _, err := h.engine.Deploy(ctx, backupApp(spec.Backups{Schedule: "0 3 * * *"})); err != nil {
		t.Errorf("Deploy after the backup: %v", err)
	}
	h.engine.Wait()
}

func TestUserOperationsWaitForAScheduledBackupInsteadOfFailing(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	h.engine.scheduleBackups(ctx, at(2, 59, 0))

	uploading, release := h.holdUploads(fake)
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	<-uploading

	stopped := make(chan error, 1)
	go func() { stopped <- h.engine.Stop(ctx, "db") }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned %v while the scheduled backup held the application", err)
	default:
	}
	release()
	if err := <-stopped; err != nil {
		t.Fatalf("Stop after waiting for the backup: %v", err)
	}
	h.engine.Wait()
	if runs := h.backups("db"); len(runs) != 1 || runs[0].Status != api.BackupSucceeded {
		t.Fatalf("got %+v", runs)
	}
}

func TestDeleteBackupRemovesItsFilesAndItsRecord(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	first, second := h.backup("db"), h.backup("db")
	ctx := context.Background()

	if err := h.engine.DeleteBackup(ctx, "db", first.ID); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
	if runs := h.backups("db"); len(runs) != 1 || runs[0].ID != second.ID {
		t.Fatalf("got %+v", runs)
	}
	if keys := bucketKeys(fake); keys != "db/2/data.tar" {
		t.Errorf("bucket holds %v", keys)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the backup's directory is still there: %v", err)
	}
	if err := h.engine.DeleteBackup(ctx, "db", first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting it again = %v, want not found", err)
	}
}

// healthChecked gives the stateful application an HTTP health check that
// answers what *healthy says.
func healthChecked(h *harness, healthy *bool, onProbe func()) spec.App {
	h.engine.opts.Probe = func(context.Context, string, int, string, time.Duration) error {
		if onProbe != nil {
			onProbe()
		}
		if !*healthy {
			return errors.New("HTTP 503")
		}
		return nil
	}
	a := backupApp(spec.Backups{Schedule: "0 3 * * *"})
	a.Health = &spec.Health{Path: "/health", Interval: spec.Duration(10 * time.Millisecond), Timeout: spec.Duration(10 * time.Millisecond), Retries: 3}
	return a
}

func (h *harness) verify(name string, id int64) api.BackupRunDetail {
	h.t.Helper()
	run, err := h.engine.VerifyBackup(context.Background(), name, id)
	if err != nil {
		h.t.Fatalf("VerifyBackup: %v", err)
	}
	if run.Activity != api.BackupActivityVerify {
		h.t.Errorf("activity = %q right after the verification started", run.Activity)
	}
	h.engine.Wait()
	return h.backupRun(name, run.ID)
}

func TestVerifyRestoresIntoAScratchContainerAndHoldsItToTheHealthCheck(t *testing.T) {
	h := newHarness(t)
	withBackups(h, testPassphrase, nil)
	ctx := context.Background()
	healthy := true
	var seen []docker.Container
	var seenFiles map[string][]byte
	var seenSpec docker.ContainerSpec
	var listed []api.VolumeInfo
	var scratch []string
	// Probes run where they are asked for, so that the supervisor's own, below,
	// are over before the verification goes on.
	h.engine.sup.inlineProbes = true
	a := healthChecked(h, &healthy, func() {
		jobs := h.rt.JobContainers()
		if len(jobs) != 1 || len(seen) > 0 {
			return // the deployment's own probes, or the supervisor's below
		}
		c, _ := h.rt.InspectContainer(ctx, jobs[0].ID)
		seen, seenFiles, seenSpec = append(seen, c), h.rt.Files(c.ID), h.rt.Spec(c.ID)
		listed, _ = h.engine.ManagedVolumes(ctx)
		scratch = h.rt.ScratchVolumes()
		// The supervisor comes by while the verification runs.
		app, _ := h.store.GetApplication(ctx, "db")
		h.engine.sup.superviseApp(ctx, time.Now(), app)
	})
	a.Publish = []spec.Publish{{Port: 5432, Host: 15432, Protocol: "tcp"}}
	h.deploy(a)
	replica := h.rt.Containers()[0]
	h.rt.PutFile(replica.ID, "/var/lib/data/a.txt", []byte("as backed up"))
	run := h.backup("db")
	// What the application wrote after the backup is not in it.
	h.rt.PutFile(replica.ID, "/var/lib/data/a.txt", []byte("written later"))

	got := h.verify("db", 0)
	if got.ID != run.ID || got.VerifiedAt == nil || got.VerifyError != "" || got.Activity != "" {
		t.Fatalf("unexpected run after the verification: %+v", got)
	}
	if !strings.Contains(got.VerifyOutput, "log line from shipwick_db_job_backup.verify_1") {
		t.Errorf("verify output = %q, want the container's last output", got.VerifyOutput)
	}

	if len(seen) == 0 {
		t.Fatal("the verification container was never probed")
	}
	c := seen[0]
	if c.Name != "shipwick_db_job_backup.verify_1" || c.Job != docker.VerifyJob || !c.Running || c.Image != "postgres:17" {
		t.Errorf("unexpected verification container: %+v", c)
	}
	if len(c.ServiceNames) != 0 {
		t.Errorf("the verification container answers to %v on the services network", c.ServiceNames)
	}
	if string(seenFiles["/var/lib/data/a.txt"]) != "as backed up" {
		t.Errorf("the container saw %q, want the backup's contents", seenFiles["/var/lib/data/a.txt"])
	}
	// The scratch volume exists while the container runs, and is nobody's volume in the listing.
	if len(scratch) != 1 || scratch[0] != "shipwick_db_data_verify_1" || len(listed) != 1 || listed[0].Volume != "data" {
		t.Errorf("during the verification: scratch volumes %v, listed volumes %+v", scratch, listed)
	}
	if len(seenSpec.Publish) != 0 {
		t.Errorf("the verification container publishes %+v: those ports are the replica's", seenSpec.Publish)
	}
	if len(seenSpec.Mounts) != 1 || seenSpec.Mounts[0].Volume != "data_verify_1" || seenSpec.Env["SECRET"] != "hunter2" {
		t.Errorf("unexpected mounts or environment: %+v", seenSpec)
	}

	// Gone without a trace, and the application none the wiser.
	if jobs := h.rt.JobContainers(); len(jobs) != 0 {
		t.Errorf("the verification container is still there: %+v", jobs)
	}
	if scratch := h.rt.ScratchVolumes(); len(scratch) != 0 {
		t.Errorf("scratch volumes left behind: %v", scratch)
	}
	if got := h.rt.Files(replica.ID)["/var/lib/data/a.txt"]; string(got) != "written later" {
		t.Errorf("the application's own volume now holds %q", got)
	}
	if c, _ := h.rt.InspectContainer(ctx, replica.ID); !c.Running || h.rt.Starts(replica.ID) != 1 {
		t.Error("the application's replica was touched by the verification")
	}
	events := strings.Join(h.appEventMessages("db"), "\n")
	if !strings.Contains(events, "Backup #1 verified") || strings.Contains(events, "leftover") {
		t.Errorf("events:\n%s", events)
	}
	volumes, _ := h.engine.ManagedVolumes(ctx)
	if len(volumes) != 1 {
		t.Errorf("volume listing: %+v", volumes)
	}
}

func TestVerifyReportsAContainerThatDoesNotBecomeHealthy(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	healthy := true
	h.deploy(healthChecked(h, &healthy, nil))
	h.backup("db")

	healthy = false
	got := h.verify("db", 1)
	if got.VerifiedAt != nil || !strings.Contains(got.VerifyError, "did not become healthy on the restored data within 30ms") ||
		!strings.Contains(got.VerifyError, "GET /health on port 8080: HTTP 503") {
		t.Fatalf("unexpected run: %+v", got)
	}
	if got.VerifyOutput == "" {
		t.Error("no output of the container that failed")
	}
	if len(h.rt.JobContainers()) != 0 || len(h.rt.ScratchVolumes()) != 0 {
		t.Errorf("left behind: %+v, %v", h.rt.JobContainers(), h.rt.ScratchVolumes())
	}
	if !strings.Contains(strings.Join(h.appEventMessages("db"), "\n"), "Backup #1 did not verify") {
		t.Errorf("events: %v", h.appEventMessages("db"))
	}

	// A later verification that passes replaces the verdict.
	healthy = true
	if got := h.verify("db", 1); got.VerifiedAt == nil || got.VerifyError != "" {
		t.Fatalf("unexpected run: %+v", got)
	}
}

func TestVerifyWithoutAHealthCheckWantsTheContainerToStayUp(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	h.backup("db")

	got := h.verify("db", 1)
	if got.VerifiedAt == nil || got.VerifyError != "" {
		t.Fatalf("unexpected run: %+v", got)
	}
	if events := strings.Join(h.appEventMessages("db"), "\n"); !strings.Contains(events, "stayed up for 50ms (it has no health check)") {
		t.Errorf("the event does not say what was established:\n%s", events)
	}

	h.rt.CrashNames["shipwick_db_job_backup.verify_1"] = true
	got = h.verify("db", 1)
	if got.VerifiedAt != nil || !strings.Contains(got.VerifyError, "exited with code 1 on the restored data") {
		t.Fatalf("unexpected run: %+v", got)
	}
	if len(h.rt.JobContainers()) != 0 || len(h.rt.ScratchVolumes()) != 0 {
		t.Errorf("left behind: %+v, %v", h.rt.JobContainers(), h.rt.ScratchVolumes())
	}
}

func TestVerifyRefusesWhatCannotBeVerified(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"dump"}}, nil)
	ctx := context.Background()

	if _, err := h.engine.VerifyBackup(ctx, "db", 0); !errors.Is(err, ErrNoBackup) {
		t.Errorf("with no backup at all: %v", err)
	}
	h.rt.SetExecResult("dump", dockertest.ExecResult{ExitCode: 1})
	failed := h.backup("db")
	if _, err := h.engine.VerifyBackup(ctx, "db", failed.ID); !errors.Is(err, ErrBackupNotUsable) {
		t.Errorf("a failed backup: %v", err)
	}
	if _, err := h.engine.VerifyBackup(ctx, "db", 0); !errors.Is(err, ErrNoBackup) {
		t.Errorf("the latest, with only a failed one: %v", err)
	}
	if _, err := h.engine.VerifyBackup(ctx, "db", 99); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown backup: %v", err)
	}
	if _, _, err := h.engine.OpenBackupArchive(ctx, "db", failed.ID, "data"); !errors.Is(err, ErrBackupNotUsable) {
		t.Errorf("downloading a failed backup: %v", err)
	}
	h.engine.Wait()
	if len(h.rt.JobContainers()) != 0 {
		t.Errorf("a refused verification started a container: %+v", h.rt.JobContainers())
	}
}

func TestVerifyDetectsABackupThatWasDamaged(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, testPassphrase, nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	h.backup("db")

	path := filepath.Join(dir, "db", "1", "data.tar.enc")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-7], 0o600); err != nil {
		t.Fatal(err)
	}
	got := h.verify("db", 1)
	if got.VerifiedAt != nil || !strings.Contains(got.VerifyError, "volume data could not be restored") {
		t.Fatalf("a truncated backup verified: %+v", got)
	}
}

func TestRestoreBackupReplacesTheVolumesOfAStoppedApplication(t *testing.T) {
	h := newHarness(t)
	withBackups(h, testPassphrase, nil)
	id := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "as backed up"})
	run := h.backup("db")
	h.rt.PutFile(id, "/var/lib/data/a.txt", []byte("damaged since"))
	h.rt.PutFile(id, "/var/lib/data/junk.txt", []byte("junk"))
	ctx := context.Background()

	if _, err := h.engine.RestoreBackup(ctx, "db", run.ID); !errors.Is(err, ErrNotStopped) {
		t.Fatalf("restore into a running application = %v, want ErrNotStopped", err)
	}
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	started, err := h.engine.RestoreBackup(ctx, "db", run.ID)
	if err != nil || started.Activity != api.BackupActivityRestore {
		t.Fatalf("RestoreBackup: %+v, %v", started, err)
	}
	h.engine.Wait()

	got := h.backupRun("db", run.ID)
	if got.RestoredAt == nil || got.RestoreError != "" || got.Activity != "" {
		t.Fatalf("unexpected run after the restore: %+v", got)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].Running {
		t.Fatalf("after a restore the application has one container, stopped: %+v", containers)
	}
	files := h.rt.Files(containers[0].ID)
	if string(files["/var/lib/data/a.txt"]) != "as backed up" || len(files) != 1 {
		t.Errorf("the volume holds %v, want exactly what the backup held", files)
	}
	if err := h.engine.Start(ctx, "db"); err != nil {
		t.Errorf("Start after the restore: %v", err)
	}
}

func TestRestoreBackupReportsWhyItFailed(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, testPassphrase, nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	run := h.backup("db")
	ctx := context.Background()
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	// The passphrase on the agent is no longer the one the backup was
	// written with.
	h.engine.opts.Backups.Storage = backup.New(dir, nil, "another passphrase")

	if _, err := h.engine.RestoreBackup(ctx, "db", run.ID); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	h.engine.Wait()
	got := h.backupRun("db", run.ID)
	if got.RestoredAt != nil || !strings.Contains(got.RestoreError, "volume data: the passphrase does not match") {
		t.Fatalf("unexpected run: %+v", got)
	}
	// Refused before anything was removed.
	if files := h.rt.Files(h.rt.Containers()[0].ID); string(files["/var/lib/data/a.txt"]) != "alpha" {
		t.Errorf("the volume was touched: %v", files)
	}
}

func TestTheAgentsStateIsBackedUpDailyAndOnlyEncrypted(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	ctx := context.Background()
	h.deploy(app("my-api", "my-api:1.0", 1))

	// Without a passphrase the key is written nowhere, and the agent says why.
	h.engine.scheduleBackups(ctx, time.Now())
	h.engine.Wait()
	if _, err := h.engine.StartStateBackup(ctx, api.BackupTriggerManual); !errors.Is(err, ErrStateNotEncrypted) {
		t.Fatalf("StartStateBackup without a passphrase = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("written without a passphrase: %v", entries)
	}
	status := h.engine.BackupStatus(ctx)
	if status.Destination != api.BackupDestinationLocal || status.Encrypted || status.StateLastAt != nil ||
		!strings.Contains(status.StateError, "SHIPWICK_BACKUP_PASSPHRASE is not set") {
		t.Fatalf("unexpected status: %+v", status)
	}

	bucket, fake := fakeBucket(t)
	dir = withBackups(h, testPassphrase, bucket)
	now := time.Now()
	h.engine.scheduleBackups(ctx, now)
	h.engine.Wait()
	h.engine.scheduleBackups(ctx, now.Add(time.Hour))
	h.engine.scheduleBackups(ctx, now.Add(23*time.Hour))
	h.engine.Wait()
	runs, err := h.engine.StateBackups(ctx, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("got %d state backups within a day (%v), want 1", len(runs), err)
	}
	run := runs[0]
	if run.Status != api.BackupSucceeded || !run.Encrypted || run.Trigger != api.BackupTriggerSchedule || len(run.Volumes) != 2 ||
		run.Volumes[0].Volume != "shipwick.db" || run.Volumes[1].Volume != "encryption.key" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if keys := bucketKeys(fake); keys != "_agent/1/encryption.key.enc _agent/1/shipwick.db.enc" {
		t.Errorf("bucket holds: %s", keys)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "_agent" {
		t.Errorf("the backup directory holds %v; the unencrypted copy of the database must not stay", entries)
	}
	status = h.engine.BackupStatus(ctx)
	if status.Destination != api.BackupDestinationS3 || !status.Encrypted || status.StateLastAt == nil || status.StateError != "" {
		t.Fatalf("unexpected status: %+v", status)
	}

	// What was written restores: the key as the key file holds it, and a
	// database that opens with it.
	decrypt := func(name string) []byte {
		t.Helper()
		f, err := os.Open(filepath.Join(dir, "_agent", "1", name+".enc"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		r, err := backupfile.NewReader(f, testPassphrase)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decrypt %s: %v", name, err)
		}
		return data
	}
	if key := string(decrypt("encryption.key")); key != hex.EncodeToString([]byte(testStateKey))+"\n" {
		t.Errorf("the key in the backup is %q", key)
	}
	restoredPath := filepath.Join(t.TempDir(), "shipwick.db")
	if err := os.WriteFile(restoredPath, decrypt("shipwick.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, restoredPath, store.Options{EncryptionKey: []byte(testStateKey)})
	if err != nil {
		t.Fatalf("open the restored database: %v", err)
	}
	defer restored.Close()
	d, err := restored.ListDeployments(ctx, store.DeploymentFilter{Application: "my-api"})
	if err != nil || len(d) != 1 || d[0].Spec.Env["SECRET"] != "hunter2" {
		t.Fatalf("the restored database does not hold the deployment with its secrets: %+v, %v", d, err)
	}
	// The copy was taken before this backup was recorded: restored, it must
	// not find its own backup "running" and clean up the files it came from.
	if own, err := restored.ListBackupRuns(ctx, backup.StateOwner, 0); err != nil || len(own) != 0 {
		t.Errorf("the restored database knows %d backups of the state (%v), want none: the first was taken from it", len(own), err)
	}

	h.engine.scheduleBackups(ctx, now.Add(25*time.Hour))
	h.engine.Wait()
	if runs, _ := h.engine.StateBackups(ctx, 0); len(runs) != 2 {
		t.Fatalf("got %d state backups after a day, want 2", len(runs))
	}
}

func TestAStateBackupAfterAKeyRotationHoldsTheNewKey(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, testPassphrase, nil)
	ctx := context.Background()
	h.deploy(app("my-api", "my-api:1.0", 1))
	if _, err := h.store.RotateKey(ctx); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}

	if _, err := h.engine.StartStateBackup(ctx, api.BackupTriggerManual); err != nil {
		t.Fatalf("StartStateBackup: %v", err)
	}
	h.engine.Wait()

	decrypt := func(name string) []byte {
		t.Helper()
		f, err := os.Open(filepath.Join(dir, "_agent", "1", name+".enc"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		r, err := backupfile.NewReader(f, testPassphrase)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decrypt %s: %v", name, err)
		}
		return data
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(decrypt("encryption.key"))))
	if err != nil || string(key) == testStateKey {
		t.Fatalf("the key in the backup is the one from before the rotation (%v)", err)
	}
	restoredPath := filepath.Join(t.TempDir(), "shipwick.db")
	if err := os.WriteFile(restoredPath, decrypt("shipwick.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, restoredPath, store.Options{EncryptionKey: key})
	if err != nil {
		t.Fatalf("open the restored database with the key next to it: %v", err)
	}
	defer restored.Close()
	d, err := restored.ListDeployments(ctx, store.DeploymentFilter{Application: "my-api"})
	if err != nil || len(d) != 1 || d[0].Spec.Env["SECRET"] != "hunter2" {
		t.Fatalf("the restored database does not open its secrets with that key: %+v, %v", d, err)
	}
}

func TestAFailedStateBackupIsReportedAndTriedAgainInAnHour(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	fake.FailPuts(true)
	dir := withBackups(h, testPassphrase, bucket)
	rec := notified(h)
	ctx := context.Background()
	now := time.Now()

	h.engine.scheduleBackups(ctx, now)
	h.engine.Wait()
	status := h.engine.BackupStatus(ctx)
	if status.StateLastAt != nil || !strings.Contains(status.StateError, "HTTP 500") {
		t.Fatalf("unexpected status: %+v", status)
	}
	if ev := lastEvent(t, rec, notify.BackupFailed); !strings.Contains(ev.Message, "agent's own state") {
		t.Errorf("unexpected notification: %+v", ev)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "_agent")); len(entries) != 0 {
		t.Errorf("a failed state backup left files: %v", entries)
	}

	fake.FailPuts(false)
	h.engine.scheduleBackups(ctx, now.Add(30*time.Minute))
	h.engine.Wait()
	if runs, _ := h.engine.StateBackups(ctx, 0); len(runs) != 1 {
		t.Fatalf("retried after half an hour: %d runs", len(runs))
	}
	h.engine.scheduleBackups(ctx, now.Add(61*time.Minute))
	h.engine.Wait()
	status = h.engine.BackupStatus(ctx)
	if status.StateLastAt == nil || status.StateError != "" {
		t.Fatalf("unexpected status after the retry: %+v", status)
	}
}

func TestStartBackupsSettlesWhatAnEarlierAgentLeftBehind(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	good := h.backup("db")
	ctx := context.Background()

	// An agent died while it took a backup and while it verified another.
	interrupted, err := h.store.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dir, "db", "2")
	os.MkdirAll(half, 0o700)
	os.WriteFile(filepath.Join(half, "data.tar.partial"), []byte("half"), 0o600)
	if err := h.store.ClaimBackupRun(ctx, good.ID, api.BackupActivityVerify); err != nil {
		t.Fatal(err)
	}
	h.rt.CreateContainer(ctx, docker.ContainerSpec{App: "db", Image: "postgres:17",
		Mounts: []docker.Mount{{Volume: docker.ScratchVolume("data", good.ID), Path: "/var/lib/data"}},
		Job:    &docker.JobSpec{Name: docker.VerifyJob, RunID: good.ID}})

	if err := h.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := h.engine.StartBackups(ctx); err != nil {
		t.Fatalf("StartBackups: %v", err)
	}
	// Leftover containers are retired in the background.
	h.engine.Wait()
	if got := h.backupRun("db", interrupted.ID); got.Status != api.BackupFailed || !strings.Contains(got.Error, "restarted") {
		t.Errorf("the interrupted backup: %+v", got)
	}
	if _, err := os.Stat(half); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the interrupted backup's files are still there: %v", err)
	}
	if got := h.backupRun("db", good.ID); got.Activity != "" || got.VerifiedAt != nil || !strings.Contains(got.VerifyError, "restarted") {
		t.Errorf("the interrupted verification: %+v", got)
	}
	if len(h.rt.JobContainers()) != 0 || len(h.rt.ScratchVolumes()) != 0 {
		t.Errorf("left behind: %+v, %v", h.rt.JobContainers(), h.rt.ScratchVolumes())
	}
	if !h.rt.VolumeExists("db", "data") {
		t.Error("the application's own volume was swept away with the scratch ones")
	}
}

func TestBackupsOutliveTheApplicationLikeItsVolumes(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	run := h.backup("db")
	ctx := context.Background()

	if err := h.engine.Delete(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "1", "data.tar")); err != nil {
		t.Fatalf("the archive went with the application: %v", err)
	}
	if _, err := h.engine.Backups(ctx, "db", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("backups of a deleted application = %v, want not found", err)
	}
	// Deployed again under its name, it finds them.
	h.deploy(backupApp(spec.Backups{Schedule: "0 3 * * *"}))
	if runs := h.backups("db"); len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("got %+v", runs)
	}
}

func TestAScheduledBackupWaitsForTheSupervisorInsteadOfSkippingTheMinute(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, nil)
	ctx := context.Background()
	h.engine.scheduleBackups(ctx, at(2, 59, 0))

	// The supervisor ticks at the scheduler's pace and has the application
	// in hand, for a moment, every time the scheduler looks.
	if wait, err := h.engine.tryLock("db", true); wait != nil || err != nil {
		t.Fatalf("tryLock: %v", err)
	}
	release := time.AfterFunc(20*time.Millisecond, func() { h.engine.unlock("db") })
	defer release.Stop()
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	h.engine.Wait()
	if runs := h.backups("db"); len(runs) != 1 || runs[0].Status != api.BackupSucceeded {
		t.Fatalf("got %+v, want the backup taken as soon as the supervisor let go", runs)
	}
}

func TestTheScheduleFollowsTheDeploymentThatIsActiveWhenItFires(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, nil)
	ctx := context.Background()
	h.engine.scheduleBackups(ctx, at(2, 59, 0))

	// Deployed again without backups: the old schedule no longer applies.
	h.deploy(volumeApp())
	h.engine.scheduleBackups(ctx, at(3, 0, 0))
	h.engine.Wait()
	if runs := h.backups("db"); len(runs) != 0 {
		t.Fatalf("got %+v from a schedule that was removed", runs)
	}
}

// holdUploads makes the bucket hold on to the uploads that follow. The bucket
// is made the installation's own first, as the first backup would: that is
// an upload too, and not the one a test wants to stand still at.
func (h *harness) holdUploads(fake *backuptest.S3) (uploading <-chan struct{}, release func()) {
	h.t.Helper()
	if err := h.engine.prepareBackups(context.Background()); err != nil {
		h.t.Fatalf("prepareBackups: %v", err)
	}
	return fake.HoldPuts()
}

// bucketKeys lists what backups put into the bucket, without the mark of the
// installation they belong to.
func bucketKeys(fake *backuptest.S3) string {
	var keys []string
	for _, k := range fake.Keys() {
		if k != "_agent/installation" {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, " ")
}

func TestAReinstalledServerDoesNotWriteIntoTheBucketOfTheOneItReplaces(t *testing.T) {
	old := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(old, testPassphrase, bucket)
	old.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "the old server's data"})
	old.backup("db")
	ctx := context.Background()
	if _, err := old.engine.StartStateBackup(ctx, api.BackupTriggerManual); err != nil {
		t.Fatal(err)
	}
	old.engine.Wait()
	before := fake.Objects()

	// The server is lost. A new one is installed with the same bucket in its
	// configuration, and starts counting its backups at 1 again.
	fresh := newHarness(t)
	rec := notified(fresh)
	withBackups(fresh, testPassphrase, bucket)
	fresh.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "an empty new database"})

	run := fresh.backup("db")
	if run.Status != api.BackupFailed || !strings.Contains(run.Error, "another Shipwick installation") {
		t.Fatalf("unexpected run: %+v", run)
	}
	fresh.engine.scheduleBackups(ctx, time.Now())
	fresh.engine.Wait()
	status := fresh.engine.BackupStatus(ctx)
	if status.StateLastAt != nil || !strings.Contains(status.StateError, "restore its state on this server") {
		t.Fatalf("unexpected status: %+v", status)
	}
	if ev := lastEvent(t, rec, notify.BackupFailed); !strings.Contains(ev.Message, "another Shipwick installation") {
		t.Errorf("unexpected notification: %+v", ev)
	}

	after := fake.Objects()
	if len(after) != len(before) {
		t.Fatalf("the bucket went from %d to %d objects", len(before), len(after))
	}
	for key, content := range before {
		if string(after[key]) != string(content) {
			t.Errorf("%s was written over", key)
		}
	}
}

func TestBackupIDsContinuePastTheOnesTheDestinationsAlreadyHold(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	// The database was restored from a backup older than these.
	installation, err := h.store.BackupInstallation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fake.Put("_agent/installation", []byte(installation))
	fake.Put("db/41/data.tar", []byte("taken after the state backup that was restored"))

	run := h.backup("db")
	if run.ID != 42 || run.Status != api.BackupSucceeded {
		t.Fatalf("unexpected run: %+v", run)
	}
	if got := string(fake.Objects()["db/41/data.tar"]); got != "taken after the state backup that was restored" {
		t.Errorf("the backup the database had forgotten was written over: %q", got)
	}
}

func TestStartBackupsRemovesAnUnencryptedCopyOfTheDatabaseLeftBehind(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, testPassphrase, nil)
	leftover := filepath.Join(dir, ".state-123456.db")
	if err := os.WriteFile(leftover, []byte("a copy of the database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.StartBackups(context.Background()); err != nil {
		t.Fatalf("StartBackups: %v", err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy is still there: %v", err)
	}
}
