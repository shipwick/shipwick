package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/backupfile"
)

func readAll(t *testing.T, s *Storage, owner string, run int64, name string, encrypted bool) string {
	t.Helper()
	rc, err := s.Open(context.Background(), owner, run, name, encrypted)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestStorageWritesARunsFilesUnderItsOwnerAndID(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, nil, "")
	n, err := s.Write(context.Background(), "db", 12, "data.tar", strings.NewReader("the archive"))
	if err != nil || n != 11 {
		t.Fatalf("Write: %d, %v", n, err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "db", "12", "data.tar"))
	if err != nil || string(onDisk) != "the archive" {
		t.Fatalf("file: %q, %v", onDisk, err)
	}
	if got := readAll(t, s, "db", 12, "data.tar", false); got != "the archive" {
		t.Fatalf("read back %q", got)
	}
	if d := s.Destinations(); len(d) != 1 || d[0] != Local || s.Encrypted() {
		t.Fatalf("destinations %v, encrypted %v", d, s.Encrypted())
	}
}

func TestStorageEncryptsWithAPassphrase(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, nil, "a passphrase")
	if _, err := s.Write(context.Background(), "db", 1, "data.tar", strings.NewReader("the archive")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "1", "data.tar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an encrypted file carries the plain name: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "db", "1", "data.tar.enc"))
	if err != nil || !backupfile.IsEncrypted(onDisk) || bytes.Contains(onDisk, []byte("the archive")) {
		t.Fatalf("the file on disk is not encrypted: %v", err)
	}
	if got := readAll(t, s, "db", 1, "data.tar", true); got != "the archive" {
		t.Fatalf("read back %q", got)
	}

	// The passphrase was taken away, or never given to a rebuilt server.
	if _, err := New(dir, nil, "").Open(context.Background(), "db", 1, "data.tar", true); !errors.Is(err, ErrNoPassphrase) {
		t.Fatalf("got %v, want ErrNoPassphrase", err)
	}
	rc, err := New(dir, nil, "another").Open(context.Background(), "db", 1, "data.tar", true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); !errors.Is(err, backupfile.ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

func TestStorageReadsWhatWasWrittenBeforeAPassphraseWasSet(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, nil, "").Write(context.Background(), "db", 1, "data.tar", strings.NewReader("plain")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, New(dir, nil, "now set"), "db", 1, "data.tar", false); got != "plain" {
		t.Fatalf("read back %q", got)
	}
}

func TestStorageUploadsToTheBucketAndReadsFromItWhenTheDirectoryIsGone(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := testBucket(t, "alpha")
	s := New(dir, bucket, "a passphrase")
	content := strings.Repeat("archive ", 50_000) // several chunks
	if _, err := s.Write(context.Background(), "db", 3, "data.tar", strings.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "db", "3", "data.tar.enc"))
	if got := fake.Objects()["alpha/db/3/data.tar.enc"]; !bytes.Equal(got, onDisk) || len(got) == 0 {
		t.Fatalf("the bucket holds %d bytes, the directory %d", len(got), len(onDisk))
	}
	if d := s.Destinations(); len(d) != 2 || d[1] != Remote {
		t.Fatalf("destinations %v", d)
	}

	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s, "db", 3, "data.tar", true); got != content {
		t.Fatalf("read %d bytes back from the bucket, want %d", len(got), len(content))
	}
}

func TestStorageLeavesNothingBehindWhenTheUploadFails(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := testBucket(t, "")
	fake.FailPuts(true)
	s := New(dir, bucket, "")
	if _, err := s.Write(context.Background(), "db", 4, "data.tar", strings.NewReader("x")); err == nil {
		t.Fatal("Write succeeded although the bucket refused the upload")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "4"))
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestStorageRemoveDeletesInBothPlaces(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := testBucket(t, "")
	s := New(dir, bucket, "")
	ctx := context.Background()
	for _, run := range []int64{1, 2} {
		for _, name := range []string{"data.tar", "uploads.tar"} {
			if _, err := s.Write(ctx, "db", run, name, strings.NewReader("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Run 1 must not take run 10 or 11 with it.
	if _, err := s.Write(ctx, "db", 10, "data.tar", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "db", 1); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if keys := strings.Join(fake.Keys(), " "); keys != "db/10/data.tar db/2/data.tar db/2/uploads.tar" {
		t.Fatalf("bucket holds: %s", keys)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the run's directory is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db", "2", "data.tar")); err != nil {
		t.Fatalf("another run's file is gone: %v", err)
	}
	if err := s.Remove(ctx, "db", 1); err != nil {
		t.Fatalf("removing what is gone must not fail: %v", err)
	}
}

func TestStorageStopsCopyingWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if _, err := New(dir, nil, "").Write(ctx, "db", 1, "data.tar", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "db", "1"))
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

// Deriving a key at full strength takes most of a second; the tests are not
// about that.
func init() { backupfile.Iterations = 1000 }

func TestPrepareMarksTheBucketAndRefusesAnotherInstallation(t *testing.T) {
	ctx := context.Background()
	bucket, fake := testBucket(t, "alpha")
	s := New(t.TempDir(), bucket, "")

	if highest, err := s.Prepare(ctx, "installation-one"); err != nil || highest != 0 {
		t.Fatalf("Prepare on an empty bucket: %d, %v", highest, err)
	}
	if got := string(fake.Objects()["alpha/_agent/installation"]); got != "installation-one" {
		t.Fatalf("the bucket is marked %q", got)
	}
	if _, err := s.Prepare(ctx, "installation-one"); err != nil {
		t.Fatalf("the installation that marked the bucket is refused: %v", err)
	}

	// A server set up afresh, pointed at the same bucket and prefix.
	before := len(fake.Keys())
	if _, err := New(t.TempDir(), bucket, "").Prepare(ctx, "installation-two"); !errors.Is(err, ErrForeignBucket) {
		t.Fatalf("got %v, want ErrForeignBucket", err)
	}
	if len(fake.Keys()) != before || string(fake.Objects()["alpha/_agent/installation"]) != "installation-one" {
		t.Fatal("the refused installation changed the bucket")
	}

	// Under a prefix of its own it is welcome.
	other, err := NewS3(S3Config{Endpoint: fake.URL, Bucket: bucket.cfg.Bucket, Region: bucket.cfg.Region,
		AccessKeyID: bucket.cfg.AccessKeyID, SecretAccessKey: bucket.cfg.SecretAccessKey, Prefix: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.TempDir(), other, "").Prepare(ctx, "installation-two"); err != nil {
		t.Fatalf("Prepare under another prefix: %v", err)
	}
}

func TestPrepareReportsTheHighestRunIDInUseAnywhere(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bucket, fake := testBucket(t, "")
	s := New(dir, bucket, "")

	for _, run := range []int64{3, 12} {
		if _, err := New(dir, nil, "").Write(ctx, "db", run, "data.tar", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if highest, err := s.Prepare(ctx, "one"); err != nil || highest != 12 {
		t.Fatalf("with runs on the server only: %d, %v", highest, err)
	}
	// What a database restored from an older backup no longer knows about.
	fake.Put("db/41/data.tar", []byte("x"))
	fake.Put("_agent/40/shipwick.db.enc", []byte("x"))
	fake.Put("unrelated.txt", []byte("x"))
	if highest, err := s.Prepare(ctx, "one"); err != nil || highest != 41 {
		t.Fatalf("with newer runs in the bucket: %d, %v", highest, err)
	}
	if highest, err := New(t.TempDir(), nil, "").Prepare(ctx, "one"); err != nil || highest != 0 {
		t.Fatalf("with nothing anywhere: %d, %v", highest, err)
	}
}
