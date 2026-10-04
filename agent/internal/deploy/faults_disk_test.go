package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// What a disk without room does to the files the agent writes cannot be
// produced with a switch: these tests want a filesystem of a few megabytes,
// named by SHIPWICK_TEST_SMALL_DIR, and are skipped without one.
// `make test-full-disk` gives them an 8 MB tmpfs in a container.

// smallDisk returns a directory of the test's own on the small filesystem.
func smallDisk(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SHIPWICK_TEST_SMALL_DIR")
	if root == "" {
		t.Skip("SHIPWICK_TEST_SMALL_DIR is not set: no small filesystem to fill (make test-full-disk)")
	}
	dir, err := os.MkdirTemp(root, "faults-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// tooLarge is more than the small filesystem holds, and does not compress.
func tooLarge(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16<<20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// filesUnder lists every file below dir, relative to it.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAFolderThatDoesNotFitOnTheDiskIsRefusedAsAFullDiskAndLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	s, _ := newStaticHarness(t)
	s.engine.opts.UploadDir = smallDisk(t)
	first := s.upload("site", map[string]string{"index.html": "<h1>hello</h1>"})
	before := filesUnder(t, s.engine.opts.UploadDir)

	_, err := s.engine.StoreStatic(ctx, "site", bytes.NewReader(tarOf(t, map[string]string{"index.html": "<h1>hello</h1>", "video.bin": tooLarge(t)})))
	if !IsDiskFull(err) {
		t.Fatalf("StoreStatic of a folder larger than the disk: err = %v, want one that says the disk is full", err)
	}
	var bad *InvalidUploadError
	if errors.As(err, &bad) {
		t.Errorf("the upload is blamed for the disk: %q", bad.Reason)
	}
	if !strings.Contains(DiskFullMessage(err), "no space left on device") {
		t.Errorf("message = %q", DiskFullMessage(err))
	}

	// The part that was written is gone, the upload before it is not: the
	// version it belongs to can still be deployed.
	if after := filesUnder(t, s.engine.opts.UploadDir); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("files in the upload directory = %v, want what was there before: %v", after, before)
	}
	if d := s.finish(s.engine.DeployStatic(ctx, site("site"), first.Digest)); d.Status != api.StatusActive {
		t.Errorf("deploying the earlier upload: %s (%s)", d.Status, d.Error)
	}
	if !s.free("site") {
		t.Error("the application stays locked after the upload was refused")
	}
}

func TestABackupThatDoesNotFitOnTheDiskFailsAndKeepsTheEarlierOne(t *testing.T) {
	h := newHarness(t)
	dir := smallDisk(t)
	h.engine.opts.Backups = BackupOptions{Storage: backup.New(dir, nil, ""), EncryptionKey: []byte(testStateKey)}
	id := h.deployBackupApp(spec.Backups{}, map[string]string{"orders.csv": "1,2,3"})

	good := h.backup("db")
	if good.Status != api.BackupSucceeded {
		t.Fatalf("the first backup: %s (%s)", good.Status, good.Error)
	}
	before := filesUnder(t, dir)

	if err := h.rt.PutFile(id, "/var/lib/data/blob.bin", []byte(tooLarge(t))); err != nil {
		t.Fatal(err)
	}
	failed := h.backup("db")
	if failed.Status != api.BackupFailed || !strings.Contains(failed.Error, "no space left on device") {
		t.Fatalf("a backup larger than the disk: %s %q, want failed for lack of space", failed.Status, failed.Error)
	}
	if after := filesUnder(t, dir); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("files in the backup directory = %v, want only the earlier backup's: %v", after, before)
	}
	if got := h.archive("db", good.ID, "data"); got["orders.csv"] != "1,2,3" {
		t.Errorf("the earlier backup reads %v", got)
	}
	if !h.free("db") {
		t.Error("the application stays locked after the backup failed")
	}
	if got := running(h.rt.Containers()); got != "shipwick_db_1_1" {
		t.Errorf("running = %q: the application must not be affected", got)
	}
}
