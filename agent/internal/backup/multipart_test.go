package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/backup/backuptest"
)

const testPartSize = 1024

// multipartBucket is a bucket whose parts are a kilobyte, so that a few
// kilobytes are a multipart upload.
func multipartBucket(t *testing.T, prefix string) (*S3, *backuptest.S3) {
	t.Helper()
	s3, fake := testBucket(t, prefix)
	s3.partSize = testPartSize
	fake.SetMinPartSize(testPartSize)
	return s3, fake
}

// pattern is n bytes in which no two parts are alike.
func pattern(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i/testPartSize*31 + i%251)
	}
	return out
}

func upload(s3 *S3, ctx context.Context, key string, content []byte) error {
	sum := sha256.Sum256(content)
	return s3.Upload(ctx, key, bytes.NewReader(content), int64(len(content)), hex.EncodeToString(sum[:]), nil)
}

func countRequests(fake *backuptest.S3, method, containing string) int {
	n := 0
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, method+" ") && strings.Contains(r, containing) {
			n++
		}
	}
	return n
}

func TestUploadSendsWhatFitsAPartInOneRequest(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	content := pattern(testPartSize)
	if err := upload(s3, context.Background(), "db/1/data.tar", content); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got := fake.Requests(); len(got) != 1 || got[0] != "PUT /backups/db/1/data.tar" {
		t.Fatalf("requests: %v", got)
	}
	if !bytes.Equal(fake.Objects()["db/1/data.tar"], content) {
		t.Fatal("the bucket holds something else than what was sent")
	}
}

func TestUploadSendsALargerFileInPartsThatTheBucketPutsTogether(t *testing.T) {
	s3, fake := multipartBucket(t, "servers/alpha")
	content := pattern(5*testPartSize + 17)
	if err := upload(s3, context.Background(), "db/1/with space.tar", content); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !bytes.Equal(fake.Objects()["servers/alpha/db/1/with space.tar"], content) {
		t.Fatalf("the bucket holds %d bytes that are not the %d sent", len(fake.Objects()["servers/alpha/db/1/with space.tar"]), len(content))
	}
	if parts := countRequests(fake, "PUT", "partNumber="); parts != 6 {
		t.Fatalf("%d parts were sent, want 6: %v", parts, fake.Requests())
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("uploads left open: %v", left)
	}
}

func TestUploadSendsAPartAgainWhenTheBucketFalters(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	fake.FailNextPuts(partAttempts - 1)
	content := pattern(2*testPartSize + 1)
	if err := upload(s3, context.Background(), "db/1/data.tar", content); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !bytes.Equal(fake.Objects()["db/1/data.tar"], content) {
		t.Fatal("the bucket holds something else than what was sent")
	}
	if tries := countRequests(fake, "PUT", "partNumber=1&"); tries != partAttempts {
		t.Fatalf("part 1 was sent %d times, want %d", tries, partAttempts)
	}
}

func TestUploadDoesNotRepeatAPartTheBucketRefuses(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	wrong := *s3
	wrong.cfg.SecretAccessKey = "not-the-secret-0123456789"
	// Created with the right secret, so that there is an upload to refuse a
	// part of.
	id, err := s3.createUpload(context.Background(), "db/1/data.tar")
	if err != nil {
		t.Fatalf("createUpload: %v", err)
	}
	_, err = wrong.sendPart(context.Background(), "db/1/data.tar", id, 1, bytes.NewReader(pattern(10)), 0, 10)
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("got %v, want the service's refusal", err)
	}
	if tries := countRequests(fake, "PUT", "partNumber=1&"); tries != 1 {
		t.Fatalf("a refused part was sent %d times", tries)
	}
}

// tripwire is a file that reports when reading has reached an offset.
type tripwire struct {
	io.ReaderAt
	at      int64
	tripped func()
}

func (r *tripwire) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.at && r.tripped != nil {
		r.tripped()
		r.tripped = nil
	}
	return r.ReaderAt.ReadAt(p, off)
}

func TestUploadThatFailsHalfWayLeavesNoPartsBehind(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	content := pattern(4 * testPartSize)
	sum := sha256.Sum256(content)
	// The bucket stops taking uploads once the third part is being read.
	src := &tripwire{ReaderAt: bytes.NewReader(content), at: 2 * testPartSize, tripped: func() { fake.FailPuts(true) }}
	err := s3.Upload(context.Background(), "db/1/data.tar", src, int64(len(content)), hex.EncodeToString(sum[:]), nil)
	if err == nil || !strings.Contains(err.Error(), "part 3") {
		t.Fatalf("got %v, want a failure that names part 3", err)
	}
	if countRequests(fake, "PUT", "partNumber=2&") != 1 {
		t.Fatalf("the test is not testing a failure half-way: %v", fake.Requests())
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("parts left to be paid for: %v", left)
	}
	if keys := fake.Keys(); len(keys) != 0 {
		t.Fatalf("the bucket holds %v", keys)
	}
}

func TestUploadIsAbortedWhenItsContextEnds(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	arrived, release := fake.HoldPuts()
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- upload(s3, ctx, "db/1/data.tar", pattern(3*testPartSize)) }()
	<-arrived
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the context's error", err)
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("parts left to be paid for: %v", left)
	}
}

func TestUploadFailsWhenTheBucketCannotPutThePartsTogether(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	fake.FailComplete(true)
	err := upload(s3, context.Background(), "db/1/data.tar", pattern(2*testPartSize))
	// The service says so inside a 200.
	if err == nil || !strings.Contains(err.Error(), "InternalError") {
		t.Fatalf("got %v, want the error the answer carried", err)
	}
	if left, keys := fake.Uploads(), fake.Keys(); len(left) != 0 || len(keys) != 0 {
		t.Fatalf("left behind: uploads %v, objects %v", left, keys)
	}
}

func TestUploadSaysSoWhenItCannotRemoveItsParts(t *testing.T) {
	s3, fake := multipartBucket(t, "")
	fake.FailComplete(true)
	fake.FailAborts(true)
	err := upload(s3, context.Background(), "db/1/data.tar", pattern(2*testPartSize))
	if err == nil || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("got %v, want it to say that the parts are still there", err)
	}
}

func TestUploadRefusesWhatNoBucketHolds(t *testing.T) {
	s3, fake := testBucket(t, "")
	err := s3.Upload(context.Background(), "db/1/data.tar", bytes.NewReader(nil), maxObjectBytes+1, emptyPayload, nil)
	if err == nil || len(fake.Requests()) != 0 {
		t.Fatalf("got %v after %d requests, want a refusal before any", err, len(fake.Requests()))
	}
}

func TestPartsGrowSoThatTheLargestObjectFitsTheirNumber(t *testing.T) {
	for _, size := range []int64{1, defaultPartSize * maxParts, defaultPartSize*maxParts + 1, 1 << 40, maxObjectBytes} {
		part := partSizeFor(size, defaultPartSize)
		if parts := (size + part - 1) / part; parts > maxParts || part < defaultPartSize || part%(1<<20) != 0 || part > 5<<30 {
			t.Errorf("%d bytes: parts of %d bytes, %d of them", size, part, parts)
		}
	}
	if part := partSizeFor(100<<30, defaultPartSize); part != defaultPartSize {
		t.Errorf("a 100 GiB archive goes up in parts of %d bytes, want the usual %d", part, defaultPartSize)
	}
}

func TestStorageWritesALargeArchiveToTheBucketInParts(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := multipartBucket(t, "alpha")
	s := New(dir, bucket, "a passphrase")
	content := string(pattern(10 * testPartSize))
	if _, err := s.Write(context.Background(), "db", 3, "data.tar", strings.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if countRequests(fake, "PUT", "partNumber=") < 10 {
		t.Fatalf("the archive did not go up in parts: %v", fake.Requests())
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "db", "3", "data.tar.enc"))
	if got := fake.Objects()["alpha/db/3/data.tar.enc"]; !bytes.Equal(got, onDisk) || len(got) == 0 {
		t.Fatalf("the bucket holds %d bytes, the directory %d", len(got), len(onDisk))
	}
	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s, "db", 3, "data.tar", true); got != content {
		t.Fatalf("read %d bytes back from the bucket, want %d", len(got), len(content))
	}
}

func TestStorageLeavesNothingAnywhereWhenALargeUploadFails(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := multipartBucket(t, "")
	fake.FailPuts(true)
	s := New(dir, bucket, "")
	if _, err := s.Write(context.Background(), "db", 4, "data.tar", bytes.NewReader(pattern(3*testPartSize))); err == nil {
		t.Fatal("Write succeeded although the bucket refused every part")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db", "4")); len(entries) != 0 {
		t.Fatalf("left in the directory: %v", entries)
	}
	if left, keys := fake.Uploads(), fake.Keys(); len(left) != 0 || len(keys) != 0 {
		t.Fatalf("left in the bucket: uploads %v, objects %v", left, keys)
	}
}

func TestAbortLeftoversEndsTheUploadsOfAnAgentThatDied(t *testing.T) {
	bucket, fake := multipartBucket(t, "alpha")
	fake.SetPageSize(2)
	part := pattern(testPartSize)
	fake.LeaveUpload("alpha/db/7/data.tar.enc", part, part)
	fake.LeaveUpload("alpha/db/7/data.tar.enc", part) // the same key, twice: two attempts
	fake.LeaveUpload("alpha/_agent/8/shipwick.db.enc", part)
	fake.LeaveUpload("alpha/_export/9/export.swexport.enc", part)
	fake.LeaveUpload("alpha/media/video.mp4", part)  // in our prefix, not shaped like ours
	fake.LeaveUpload("alphabet/db/7/data.tar", part) // another prefix
	fake.LeaveUpload("beta/db/7/data.tar", part)

	s := New(t.TempDir(), bucket, "")
	n, err := s.AbortLeftovers(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("AbortLeftovers: %d, %v; want 4 aborted", n, err)
	}
	want := []string{"alpha/media/video.mp4 (1 parts)", "alphabet/db/7/data.tar (1 parts)", "beta/db/7/data.tar (1 parts)"}
	if got := fake.Uploads(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("uploads left: %v, want %v", got, want)
	}
	if n, err := New(t.TempDir(), nil, "").AbortLeftovers(context.Background()); n != 0 || err != nil {
		t.Fatalf("without a bucket: %d, %v", n, err)
	}
}

func TestWriteNotesAMultipartUploadForAsLongAsItLasts(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := multipartBucket(t, "")
	s := New(dir, bucket, "")
	arrived, release := fake.HoldPuts()
	done := make(chan error, 1)
	go func() {
		_, err := s.Write(context.Background(), "db", 5, "data.tar", bytes.NewReader(pattern(3*testPartSize)))
		done <- err
	}()
	<-arrived
	// The first part is on its way: an agent killed now leaves this behind.
	note, err := os.ReadFile(filepath.Join(dir, "db", "5", "data.tar.upload"))
	if err != nil || string(note) != "upload-1" {
		t.Fatalf("the note holds %q (%v), want the upload's id", note, err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Write: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db", "5")); len(entries) != 1 || entries[0].Name() != "data.tar" {
		t.Fatalf("after the upload the directory holds %v, want the archive alone", entries)
	}
}

func TestAbortLeftoversAbortsWhatTheNotesNameWhereTheBucketWillNotList(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := multipartBucket(t, "alpha")
	fake.ListUploadsByKeyOnly(true)
	s := New(dir, bucket, "")
	// An agent died while backup 7 went up: the file it was sending is gone
	// with the process's cleanup or still there half-named, the note is there.
	part := pattern(testPartSize)
	id := fake.LeaveUpload("alpha/db/7/data.tar.enc", part, part)
	run := filepath.Join(dir, "db", "7")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "data.tar.enc.upload"), []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
	// A note that was cut off before the id was written names nothing.
	if err := os.WriteFile(filepath.Join(run, "logs.tar.enc.upload"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := s.AbortLeftovers(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("AbortLeftovers: %d, %v; want the noted upload aborted", n, err)
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("uploads left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the notes, or the directory that held nothing else, are still there: %v", err)
	}
}

func TestAnUploadThatCouldNotBeAbortedStaysNotedUntilItIs(t *testing.T) {
	dir := t.TempDir()
	bucket, fake := multipartBucket(t, "")
	fake.ListUploadsByKeyOnly(true)
	fake.FailComplete(true)
	fake.FailAborts(true)
	s := New(dir, bucket, "")
	ctx := context.Background()
	if _, err := s.Write(ctx, "db", 6, "data.tar", bytes.NewReader(pattern(2*testPartSize))); !errors.Is(err, errPartsRemain) {
		t.Fatalf("got %v, want a failure that says the parts are still there", err)
	}
	// What a failed backup does next: remove what it wrote. The bucket still
	// refuses, and the note must outlive that.
	if err := s.Remove(ctx, "db", 6); err == nil {
		t.Fatal("Remove succeeded although the upload could not be aborted")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db", "6")); len(entries) != 1 || entries[0].Name() != "data.tar.upload" {
		t.Fatalf("the directory holds %v, want the note alone", entries)
	}
	if left := fake.Uploads(); len(left) != 1 {
		t.Fatalf("uploads: %v", left)
	}

	fake.FailAborts(false)
	if n, err := s.AbortLeftovers(ctx); err != nil || n != 1 {
		t.Fatalf("AbortLeftovers: %d, %v", n, err)
	}
	if left := fake.Uploads(); len(left) != 0 {
		t.Fatalf("uploads left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the note is still there: %v", err)
	}
}
