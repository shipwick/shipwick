//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Multipart uploads against a real S3-compatible server: see realBucket for
// how to start one. The service takes no part smaller than 5 MiB but the
// last, so that is the part size here, and a few megabytes more make several
// parts.
const realPartSize = 5 << 20

func realMultipartBucket(t *testing.T, prefix string) *S3 {
	t.Helper()
	s3 := realBucket(t, prefix)
	s3.partSize = realPartSize
	// Registered after realBucket's cleanup, so run before it: a bucket with
	// uploads in progress is not empty to every service.
	t.Cleanup(func() {
		whole := *s3
		whole.cfg.Prefix = ""
		uploads, _ := whole.listUploads(context.Background(), "")
		for _, u := range uploads {
			whole.abortUpload(context.Background(), u.Key, u.ID)
		}
	})
	return s3
}

func TestRealBucketPutsThePartsOfALargeFileTogether(t *testing.T) {
	s3 := realMultipartBucket(t, "servers/alpha")
	ctx := context.Background()
	content := make([]byte, 2*realPartSize+1<<20+123)
	rand.Read(content)
	sum := sha256.Sum256(content)

	started := time.Now()
	if err := s3.Upload(ctx, "db/7/with space & plus+.tar", bytes.NewReader(content), int64(len(content)), hex.EncodeToString(sum[:]), nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	rc, err := s3.Get(ctx, "db/7/with space & plus+.tar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("%d bytes came back, want the %d that were sent in three parts (%v)", len(got), len(content), err)
	}
	listed, err := s3.List(ctx, "db/7/")
	if err != nil || len(listed) != 1 || listed[0].Size != int64(len(content)) {
		t.Fatalf("listing: %+v, %v", listed, err)
	}
	// Adoption takes a backup's time from here.
	if age := listed[0].Modified.Sub(started); age < -time.Minute || age > time.Minute {
		t.Errorf("the object was modified at %s, it was written at %s", listed[0].Modified, started)
	}
	if uploads, err := s3.listUploads(ctx, "db/7/with space & plus+.tar"); err != nil || len(uploads) != 0 {
		t.Errorf("uploads left open: %+v, %v", uploads, err)
	}
}

func TestRealBucketKeepsNothingOfAnUploadThatFails(t *testing.T) {
	s3 := realMultipartBucket(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	content := make([]byte, 3*realPartSize)
	rand.Read(content)
	sum := sha256.Sum256(content)
	// The context ends while the third part is being read.
	src := &tripwire{ReaderAt: bytes.NewReader(content), at: 2 * realPartSize, tripped: cancel}
	if err := s3.Upload(ctx, "db/8/data.tar", src, int64(len(content)), hex.EncodeToString(sum[:]), nil); err == nil {
		t.Fatal("Upload succeeded although its context ended")
	}
	if uploads, err := s3.listUploads(context.Background(), "db/8/data.tar"); err != nil || len(uploads) != 0 {
		t.Errorf("parts left to be paid for: %+v, %v", uploads, err)
	}
	if objects, err := s3.List(context.Background(), ""); err != nil || len(objects) != 0 {
		t.Errorf("the bucket holds %+v (%v)", objects, err)
	}
}

func TestRealBucketLosesTheUploadsADeadAgentLeft(t *testing.T) {
	s3 := realMultipartBucket(t, "servers/alpha")
	ctx := context.Background()
	dir := t.TempDir()
	part := make([]byte, realPartSize)
	rand.Read(part)
	// Three uploads nobody will finish: one the dead agent noted in its
	// directory, one whose note went with the disk, and one that is not a
	// backup's at all.
	const noted, unnoted, foreign = "db/9/data.tar.enc", "_agent/10/shipwick.db.enc", "notes/draft.txt"
	for _, key := range []string{noted, unnoted, foreign} {
		id, err := s3.createUpload(ctx, key)
		if err != nil {
			t.Fatalf("createUpload %s: %v", key, err)
		}
		// Whatever the test leaves open: a bucket with uploads in it is not
		// empty to every service.
		t.Cleanup(func() { s3.abortUpload(context.Background(), key, id) })
		if _, err := s3.sendPart(ctx, key, id, 1, bytes.NewReader(part), 0, int64(len(part))); err != nil {
			t.Fatalf("sendPart %s: %v", key, err)
		}
		if key == noted {
			if err := os.MkdirAll(filepath.Join(dir, "db", "9"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "db", "9", "data.tar.enc"+uploadSuffix), []byte(id), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	open := func(key string) int {
		t.Helper()
		uploads, err := s3.listUploads(ctx, key)
		if err != nil {
			t.Fatalf("list the uploads of %s: %v", key, err)
		}
		return len(uploads)
	}
	if open(noted) != 1 || open(unnoted) != 1 || open(foreign) != 1 {
		t.Fatal("the service does not list an unfinished upload under its own key")
	}
	// S3 lists the uploads under a prefix; MinIO only those of one key.
	listsByPrefix := open("") == 3
	t.Logf("the service lists unfinished uploads by prefix: %v", listsByPrefix)

	n, err := New(dir, s3, "").AbortLeftovers(ctx)
	if err != nil {
		t.Fatalf("AbortLeftovers: %v", err)
	}
	if open(noted) != 0 {
		t.Error("the upload the directory had a note of is still there")
	}
	if open(foreign) != 1 {
		t.Error("an upload that is not a backup's was aborted")
	}
	if want := map[bool]int{true: 2, false: 1}[listsByPrefix]; n != want || open(unnoted) != 2-want {
		t.Errorf("%d uploads aborted, want %d; the one without a note is open %d times", n, want, open(unnoted))
	}
}

// A file larger than the 5 GB one PUT takes, written to the test's directory
// and sent with the part size the agent uses. It needs that much disk here and
// in the server, and minutes; it runs only when asked for:
//
//	SHIPWICK_TEST_S3_HUGE=1 go test -tags integration -run OverFiveGigabytes -timeout 60m ./agent/internal/backup/
func TestRealBucketTakesAnArchiveOverFiveGigabytes(t *testing.T) {
	if os.Getenv("SHIPWICK_TEST_S3_HUGE") == "" {
		t.Skip("SHIPWICK_TEST_S3_HUGE is not set")
	}
	s3 := realBucket(t, "")
	ctx := context.Background()
	const size = 5<<30 + 3<<20 + 17

	// Blocks of random bytes, each stamped with its number: no two parts of
	// the file are alike, and writing it does not wait for 5 GB of entropy.
	path := filepath.Join(t.TempDir(), "data.tar")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block := make([]byte, 1<<20)
	rand.Read(block)
	whole := sha256.New()
	for written, i := int64(0), uint64(0); written < size; i++ {
		binary.BigEndian.PutUint64(block, i)
		chunk := block[:min(int64(len(block)), size-written)]
		if _, err := io.MultiWriter(f, whole).Write(chunk); err != nil {
			t.Fatal(err)
		}
		written += int64(len(chunk))
	}
	want := hex.EncodeToString(whole.Sum(nil))

	started := time.Now()
	if err := s3.Upload(ctx, "db/1/data.tar", f, size, want, nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	t.Logf("%d bytes went up in parts of %d MiB in %s", int64(size), s3.partSize>>20, time.Since(started).Round(time.Second))

	listed, err := s3.List(ctx, "db/1/")
	if err != nil || len(listed) != 1 || listed[0].Size != size {
		t.Fatalf("listing: %+v, %v", listed, err)
	}
	rc, err := s3.Get(ctx, "db/1/data.tar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	back := sha256.New()
	n, err := io.Copy(back, rc)
	if err != nil || n != size || hex.EncodeToString(back.Sum(nil)) != want {
		t.Fatalf("%d bytes came back (%v); they are not the %d that were sent", n, err, int64(size))
	}
	if uploads, err := s3.listUploads(ctx, "db/1/data.tar"); err != nil || len(uploads) != 0 {
		t.Errorf("uploads left open: %+v, %v", uploads, err)
	}
}
