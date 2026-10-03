package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/backup/backuptest"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// smallPartBucket is fakeBucket with parts of a kilobyte, so that an archive
// of a few kilobytes goes up the way one of many gigabytes does.
func smallPartBucket(t *testing.T) (*backup.S3, *backuptest.S3) {
	t.Helper()
	fake := backuptest.NewS3(t)
	fake.SetMinPartSize(1024)
	bucket, err := backup.NewS3(backup.S3Config{
		Endpoint: fake.URL, Bucket: backuptest.Bucket, Region: backuptest.Region,
		AccessKeyID: backuptest.AccessKeyID, SecretAccessKey: backuptest.SecretAccessKey,
		PartSize: 1024, RetryPause: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return bucket, fake
}

// incompressible is n bytes that are not a repetition of anything.
func incompressible(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + i/251)
	}
	return string(out)
}

func partsSent(fake *backuptest.S3, key string) int {
	n := 0
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "PUT /"+backuptest.Bucket+"/"+key+"?partNumber=") {
			n++
		}
	}
	return n
}

func TestAnArchiveLargerThanOnePartGoesToTheBucketInParts(t *testing.T) {
	h := newHarness(t)
	bucket, fake := smallPartBucket(t)
	dir := withBackups(h, testPassphrase, bucket)
	content := incompressible(9000)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.bin": content})

	run := h.backup("db")
	if run.Status != api.BackupSucceeded || strings.Join(run.Destinations, ",") != "local,s3" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if n := partsSent(fake, "db/1/data.tar.enc"); n < 9 {
		t.Fatalf("the archive went up in %d parts; the test is not about a multipart upload", n)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "db", "1", "data.tar.enc"))
	if err != nil || !bytes.Equal(fake.Objects()["db/1/data.tar.enc"], onDisk) {
		t.Fatalf("the bucket does not hold the file the server holds (%v)", err)
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("uploads left open: %v", left)
	}
	// The server's disk is gone; what the parts were put together to restores.
	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	if got := h.archive("db", run.ID, "data"); got["a.bin"] != content {
		t.Errorf("the archive from the bucket holds %d bytes of a.bin, want %d", len(got["a.bin"]), len(content))
	}
}

func TestABackupWhoseUploadFailsLeavesNoPartsToBePaidFor(t *testing.T) {
	h := newHarness(t)
	bucket, fake := smallPartBucket(t)
	dir := withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.bin": incompressible(5000)})
	if err := h.engine.prepareBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.FailPuts(true)

	run := h.backup("db")
	if run.Status != api.BackupFailed || !strings.Contains(run.Error, "part 1") {
		t.Fatalf("unexpected run: %+v", run)
	}
	if partsSent(fake, "db/1/data.tar") == 0 {
		t.Fatal("no part was tried; the test is not about a multipart upload")
	}
	if left, keys := fake.Uploads(), bucketKeys(fake); len(left) != 0 || keys != "" {
		t.Fatalf("left in the bucket: uploads %v, objects %q", left, keys)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db")); len(entries) != 0 {
		t.Errorf("left on the server: %v", entries)
	}
}

func TestTheFirstUseOfTheBucketAbortsTheUploadsOfAnAgentThatDied(t *testing.T) {
	h := newHarness(t)
	bucket, fake := smallPartBucket(t)
	withBackups(h, "", bucket)
	ctx := context.Background()
	// An agent before this one was killed while the third part of backup 1
	// was on its way. The database knows that backup as running.
	if _, err := h.store.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, time.Now()); err != nil {
		t.Fatal(err)
	}
	part := []byte(incompressible(1024))
	fake.LeaveUpload("db/1/data.tar", part, part)
	fake.LeaveUpload("someone/else.bin", part)

	if err := h.engine.StartBackups(ctx); err != nil {
		t.Fatalf("StartBackups: %v", err)
	}
	if left := fake.Uploads(); len(left) != 1 || left[0] != "someone/else.bin (1 parts)" {
		t.Fatalf("uploads after the start: %v; want the agent's own aborted and nothing else touched", left)
	}
}

func TestUploadsInABucketOfAnotherInstallationAreNotAborted(t *testing.T) {
	h := newHarness(t)
	bucket, fake := smallPartBucket(t)
	withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	fake.Put("_agent/installation", []byte("another installation"))
	fake.LeaveUpload("db/7/data.tar", []byte(incompressible(1024)))

	if run := h.backup("db"); run.Status != api.BackupFailed {
		t.Fatalf("unexpected run: %+v", run)
	}
	if left := fake.Uploads(); len(left) != 1 {
		t.Fatalf("the upload of the installation the bucket belongs to was aborted: %v", left)
	}
}

func TestAScheduledExportGoesToTheBucketInPartsToo(t *testing.T) {
	a := seeded(t)
	bucket, fake := smallPartBucket(t)
	withBackups(a, testPassphrase, bucket)
	if err := a.rt.PutFile(a.replica("db").ID, "/var/lib/data/base/2", []byte(incompressible(6000))); err != nil {
		t.Fatal(err)
	}
	started, err := a.engine.StartExport(context.Background(), api.BackupTriggerSchedule)
	if err != nil {
		t.Fatalf("StartExport: %v", err)
	}
	a.engine.Wait()
	run, err := a.engine.ExportRun(context.Background(), started.ID)
	if err != nil || run.Status != api.BackupSucceeded {
		t.Fatalf("unexpected export: %+v, %v", run, err)
	}
	key := ExportOwner + "/1/" + exportFile + ".enc"
	if n := partsSent(fake, key); n < 6 {
		t.Fatalf("the export went up in %d parts", n)
	}
	if _, ok := fake.Objects()[key]; !ok || len(fake.Uploads()) != 0 {
		t.Fatalf("bucket holds %v, uploads open: %v", fake.Keys(), fake.Uploads())
	}
}

func TestBeforeIsGivenTheTimeItsDeployYamlAllows(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"dump"}, BeforeTimeout: spec.Duration(3 * time.Hour)}, nil)
	if run := h.backup("db"); run.Status != api.BackupSucceeded {
		t.Fatalf("unexpected run: %+v", run)
	}
	if calls := h.rt.ExecCalls(); len(calls) != 1 || calls[0].Timeout != 3*time.Hour {
		t.Fatalf("exec calls = %+v, want one with three hours", calls)
	}
}

func TestBeforeHasAnHourInADeploymentFromBeforeTheKeyExisted(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"dump"}}, nil)
	h.backup("db")
	if calls := h.rt.ExecCalls(); len(calls) != 1 || calls[0].Timeout != time.Hour {
		t.Fatalf("exec calls = %+v, want one with an hour", calls)
	}
}

func TestABeforeThatRunsPastItsLimitFailsTheBackupAndNamesTheKey(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"dump"}, BeforeTimeout: spec.Duration(90 * time.Minute)},
		map[string]string{"a.txt": "alpha"})
	h.rt.SetExecResult("dump", dockertest.ExecResult{Err: context.DeadlineExceeded})

	run := h.backup("db")
	if run.Status != api.BackupFailed {
		t.Fatalf("unexpected run: %+v", run)
	}
	for _, want := range []string{"did not finish within 1h30m", "backups.before_timeout", "nothing was archived"} {
		if !strings.Contains(run.Error, want) {
			t.Errorf("error = %q; it should contain %q", run.Error, want)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db")); len(entries) != 0 {
		t.Errorf("a failed backup left files behind: %v", entries)
	}
}
