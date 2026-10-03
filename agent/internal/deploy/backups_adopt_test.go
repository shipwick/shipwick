package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// forget removes the records of backups and leaves their files, which is how
// a database restored from an older backup of itself knows them: not at all.
func (h *harness) forget(ids ...int64) {
	h.t.Helper()
	for _, id := range ids {
		if err := h.store.DeleteBackupRun(context.Background(), id); err != nil {
			h.t.Fatalf("DeleteBackupRun: %v", err)
		}
	}
}

func (h *harness) adopt(name string) api.BackupAdoption {
	h.t.Helper()
	adoption, err := h.engine.AdoptBackups(context.Background(), name)
	if err != nil {
		h.t.Fatalf("AdoptBackups: %v", err)
	}
	return adoption
}

func adoptedIDs(a api.BackupAdoption) string {
	var out []string
	for _, b := range a.Adopted {
		owner := b.Application
		if b.Kind != api.BackupKindApplication {
			owner = b.Kind
		}
		out = append(out, owner+"#"+strconv.FormatInt(b.Backup.ID, 10))
	}
	return strings.Join(out, " ")
}

func TestAdoptRecordsTheBackupsTheDatabaseHasForgotten(t *testing.T) {
	h := newHarness(t)
	bucket, _ := fakeBucket(t)
	withBackups(h, testPassphrase, bucket)
	id := h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "known to the database"})
	ctx := context.Background()
	h.backup("db") // 1
	h.rt.PutFile(id, "/var/lib/data/a.txt", []byte("taken after the state that was restored"))
	second := h.backup("db") // 2
	if _, err := h.engine.StartStateBackup(ctx, api.BackupTriggerManual); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	state, err := h.engine.StateBackup(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	h.forget(2, 3)
	if runs := h.backups("db"); len(runs) != 1 {
		t.Fatalf("the database still knows %d backups of db", len(runs))
	}

	adoption := h.adopt("")
	if got := adoptedIDs(adoption); got != "db#2 state#3" || len(adoption.Skipped) != 0 {
		t.Fatalf("adopted %q, skipped %+v; want db#2 and state#3", got, adoption.Skipped)
	}
	got := h.backupRun("db", 2).BackupRun
	if got.Trigger != api.BackupTriggerAdopted || got.Status != api.BackupSucceeded || !got.Encrypted ||
		strings.Join(got.Destinations, ",") != "local,s3" || got.CompletedAt == nil || got.VerifiedAt != nil {
		t.Fatalf("unexpected adopted backup: %+v", got)
	}
	// What the files say is what the backup recorded when it was taken.
	if len(got.Volumes) != 1 || got.Volumes[0] != second.Volumes[0] {
		t.Errorf("volumes = %+v, the backup had recorded %+v", got.Volumes, second.Volumes)
	}
	if age := time.Since(got.StartedAt); age < 0 || age > time.Minute {
		t.Errorf("started_at = %s, want the time the files were written", got.StartedAt)
	}
	adoptedState, err := h.engine.StateBackup(ctx, 3)
	if err != nil || adoptedState.Trigger != api.BackupTriggerAdopted || len(adoptedState.Volumes) != 2 ||
		adoptedState.Volumes[0] != state.Volumes[0] || adoptedState.Volumes[1] != state.Volumes[1] {
		t.Errorf("the state backup came back as %+v (%v), it was %+v", adoptedState, err, state.Volumes)
	}

	// From here on it is a backup like any other.
	if files := h.archive("db", 2, "data"); files["a.txt"] != "taken after the state that was restored" {
		t.Errorf("the adopted backup's archive holds %v", files)
	}
	if v := h.verify("db", 2); v.VerifiedAt == nil {
		t.Errorf("the adopted backup did not verify: %s", v.VerifyError)
	}
	h.rt.PutFile(id, "/var/lib/data/a.txt", []byte("damaged"))
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.RestoreBackup(ctx, "db", 2); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	h.engine.Wait()
	if files := h.rt.Files(h.rt.Containers()[0].ID); string(files["/var/lib/data/a.txt"]) != "taken after the state that was restored" {
		t.Errorf("after restoring the adopted backup the volume holds %q", files["/var/lib/data/a.txt"])
	}

	// Nothing is adopted twice.
	if again := h.adopt(""); len(again.Adopted) != 0 || len(again.Skipped) != 0 {
		t.Errorf("a second adoption: %+v", again)
	}
}

func TestAdoptedBackupsArePrunedLikeAnyOther(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *", Keep: 2}, map[string]string{"a.txt": "alpha"})
	h.backup("db")
	h.backup("db")
	h.forget(1, 2)
	if got := adoptedIDs(h.adopt("db")); got != "db#1 db#2" {
		t.Fatalf("adopted %q", got)
	}

	h.backup("db") // 3: with keep 2, backup 1 goes
	runs := h.backups("db")
	if len(runs) != 2 || runs[0].ID != 3 || runs[1].ID != 2 {
		t.Fatalf("kept %+v, want backups 3 and 2", runs)
	}
	if keys := bucketKeys(fake); keys != "db/2/data.tar db/3/data.tar" {
		t.Errorf("bucket holds: %s", keys)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pruned backup's directory is still there: %v", err)
	}
}

func TestAdoptFindsWhatOnlyTheBucketHolds(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, testPassphrase, bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	taken := h.backup("db")
	h.forget(1)
	// The server is a new one: its disk never held the backup. The bucket
	// says when it was written.
	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 9, 30, 3, 0, 12, 0, time.UTC)
	fake.PutAt("db/1/data.tar.enc", fake.Objects()["db/1/data.tar.enc"], written)

	adoption := h.adopt("db")
	if len(adoption.Adopted) != 1 {
		t.Fatalf("adoption: %+v", adoption)
	}
	got := adoption.Adopted[0].Backup
	if strings.Join(got.Destinations, ",") != "s3" || !got.StartedAt.Equal(written) || got.CompletedAt == nil || !got.CompletedAt.Equal(written) {
		t.Errorf("unexpected adopted backup: %+v", got)
	}
	if got.Volumes[0] != taken.Volumes[0] {
		t.Errorf("volumes = %+v, the backup had recorded %+v", got.Volumes, taken.Volumes)
	}
	if files := h.archive("db", 1, "data"); files["a.txt"] != "alpha" {
		t.Errorf("archive from the bucket = %v", files)
	}
}

func TestAdoptFindsWhatTheDirectoryHoldsWithoutABucket(t *testing.T) {
	h := newHarness(t)
	dir := withBackups(h, "", nil)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	taken := h.backup("db")
	h.forget(1)
	written := time.Date(2026, 9, 30, 3, 0, 12, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "db", "1", "data.tar"), written, written); err != nil {
		t.Fatal(err)
	}
	// A file that was still being written when the agent died is not a
	// backup's.
	if err := os.WriteFile(filepath.Join(dir, "db", "1", "more.tar.partial"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	adoption := h.adopt("")
	if len(adoption.Adopted) != 1 {
		t.Fatalf("adoption: %+v", adoption)
	}
	got := adoption.Adopted[0].Backup
	if got.Encrypted || strings.Join(got.Destinations, ",") != "local" || !got.StartedAt.Equal(written) ||
		len(got.Volumes) != 1 || got.Volumes[0] != taken.Volumes[0] {
		t.Errorf("unexpected adopted backup: %+v, the backup had recorded %+v", got, taken.Volumes)
	}
}

func TestAdoptTakesOnlyTheApplicationItIsAskedFor(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	withBackups(h, "", bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	h.backup("db")
	h.forget(1)
	// An application the restored database does not know at all.
	fake.Put("queue/2/data.tar", []byte("an archive"))

	if got := adoptedIDs(h.adopt("queue")); got != "queue#2" {
		t.Fatalf("adopted %q, want queue#2 alone", got)
	}
	if got := adoptedIDs(h.adopt("")); got != "db#1" {
		t.Fatalf("adopted %q, want what was left: db#1", got)
	}
	if _, err := h.engine.AdoptBackups(context.Background(), "../etc"); err == nil {
		t.Error("a name that is no application's was accepted")
	}
}

func TestAdoptLeavesAloneWhatIsNotAWholeBackup(t *testing.T) {
	h := newHarness(t)
	bucket, fake := fakeBucket(t)
	dir := withBackups(h, testPassphrase, bucket)
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	h.backup("db") // 1
	whole := fake.Objects()["db/1/data.tar.enc"]
	ctx := context.Background()

	// A backup of the state that the agent died over, after its first file.
	fake.Put("_agent/20/shipwick.db.enc", whole)
	// An archive cut off inside its first chunk, and one that is there in two
	// forms.
	fake.Put("db/21/data.tar.enc", whole[:40])
	fake.Put("db/22/data.tar.enc", whole)
	fake.Put("db/22/data.tar", []byte("in the clear"))
	// Two volumes, one encrypted.
	fake.Put("db/23/data.tar.enc", whole)
	fake.Put("db/23/logs.tar", []byte("in the clear"))
	// The directory holds another file than the bucket.
	fake.Put("db/24/data.tar.enc", whole)
	if err := os.MkdirAll(filepath.Join(dir, "db", "24"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "24", "data.tar.enc"), whole[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	// Not the agent's at all: keys that are no run's, names that are nobody's,
	// a name that would leave the directory.
	fake.Put("db/notes.txt", []byte("x"))
	fake.Put("db/25/readme.md", []byte("x"))
	fake.Put("Somebody_Else/26/data.tar", []byte("x"))
	fake.Put("../27/data.tar", []byte("x"))
	fake.Put("db/28/..", []byte("x"))
	// A backup that is being taken right now has a record, and is not found
	// a second time.
	running, err := h.store.CreateBackupRun(ctx, "db", api.BackupTriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake.Put("db/"+strconv.FormatInt(running.ID, 10)+"/data.tar.enc", whole)

	adoption := h.adopt("")
	if len(adoption.Adopted) != 0 {
		t.Fatalf("adopted: %+v", adoption.Adopted)
	}
	reasons := map[int64]string{}
	for _, s := range adoption.Skipped {
		reasons[s.ID] = s.Kind + ": " + s.Reason
	}
	for id, want := range map[int64]string{
		20: "state: encryption.key.enc is missing",
		21: "application: data.tar.enc is not a whole encrypted backup",
		22: "application: data.tar is there twice",
		23: "application: some of its files are encrypted and some are not",
		24: "application: data.tar.enc has one size in the directory and another in the bucket",
	} {
		if !strings.HasPrefix(reasons[id], want) {
			t.Errorf("run %d: %q, want it to start with %q", id, reasons[id], want)
		}
	}
	if len(adoption.Skipped) != 5 {
		t.Errorf("skipped %+v, want the five runs above and nothing else", adoption.Skipped)
	}
	if run, err := h.store.GetBackupRun(ctx, "db", running.ID); err != nil || run.Status != api.BackupRunning {
		t.Errorf("the backup in progress was touched: %+v, %v", run, err)
	}
}

func TestAdoptNeverReadsABucketOfAnotherInstallation(t *testing.T) {
	old := newHarness(t)
	bucket, _ := fakeBucket(t)
	withBackups(old, "", bucket)
	old.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "the old server's"})
	old.backup("db")

	fresh := newHarness(t)
	withBackups(fresh, "", bucket)
	adoption, err := fresh.engine.AdoptBackups(context.Background(), "")
	if !errors.Is(err, backup.ErrForeignBucket) || len(adoption.Adopted) != 0 {
		t.Fatalf("got %+v, %v; want ErrForeignBucket and nothing adopted", adoption, err)
	}
	if ids, _ := fresh.store.BackupRunIDs(context.Background()); len(ids) != 0 {
		t.Fatalf("the new installation recorded backups of the old one: %v", ids)
	}

	none := newHarness(t)
	if _, err := none.engine.AdoptBackups(context.Background(), ""); !errors.Is(err, ErrBackupsDisabled) {
		t.Fatalf("without a place for backups: %v", err)
	}
}
