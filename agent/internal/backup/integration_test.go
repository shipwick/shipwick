//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The client against a real S3-compatible server. Start one, point the test
// at it, and remove it afterwards:
//
//	docker run -d --name shipwick-test-minio -p 127.0.0.1:19000:9000 \
//	    -e MINIO_ROOT_USER=shipwick -e MINIO_ROOT_PASSWORD=shipwick-test-secret \
//	    --user 0 cgr.dev/chainguard/minio server /data
//	SHIPWICK_TEST_S3_ENDPOINT=http://127.0.0.1:19000 \
//	SHIPWICK_TEST_S3_ACCESS_KEY_ID=shipwick \
//	SHIPWICK_TEST_S3_SECRET_ACCESS_KEY=shipwick-test-secret \
//	    go test -tags integration ./agent/internal/backup/
//	docker rm -f shipwick-test-minio
func realBucket(t *testing.T, prefix string) *S3 {
	t.Helper()
	endpoint := os.Getenv("SHIPWICK_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SHIPWICK_TEST_S3_ENDPOINT is not set; see the comment above realBucket")
	}
	s3, err := NewS3(S3Config{
		Endpoint:        endpoint,
		Bucket:          fmt.Sprintf("shipwick-test-%d", time.Now().UnixNano()),
		Region:          os.Getenv("SHIPWICK_TEST_S3_REGION"),
		AccessKeyID:     os.Getenv("SHIPWICK_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("SHIPWICK_TEST_S3_SECRET_ACCESS_KEY"),
		Prefix:          prefix,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	// The bucket is the test's own: created here, emptied and removed after.
	bucketRequest := func(method string) {
		t.Helper()
		req, err := s3.request(context.Background(), method, "", nil, nil, emptyPayload)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := s3.http.Do(req)
		if err != nil {
			t.Fatalf("%s bucket: %v", method, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s bucket: %v", method, responseError(resp))
		}
	}
	bucketRequest(http.MethodPut)
	t.Cleanup(func() {
		whole := *s3
		whole.cfg.Prefix = ""
		objects, _ := whole.List(context.Background(), "")
		for _, o := range objects {
			whole.Delete(context.Background(), o.Key)
		}
		bucketRequest(http.MethodDelete)
	})
	return s3
}

func TestRealBucketPutGetListDelete(t *testing.T) {
	s3 := realBucket(t, "servers/alpha")
	ctx := context.Background()

	// Larger than one read, with a key that needs escaping.
	big := make([]byte, 3<<20)
	rand.Read(big)
	sum := sha256.Sum256(big)
	objects := map[string][]byte{
		"db/7/data.tar":                big,
		"db/7/with space & plus+é.tar": []byte("odd name"),
		"db/8/empty.tar":               {},
		"other/1/data.tar":             []byte("another application"),
	}
	for key, content := range objects {
		h := sha256.Sum256(content)
		if err := s3.Put(ctx, key, bytes.NewReader(content), int64(len(content)), hex.EncodeToString(h[:])); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	for key, content := range objects {
		rc, err := s3.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("Get %s: %d bytes back, want %d (%v)", key, len(got), len(content), err)
		}
	}

	listed, err := s3.List(ctx, "db/7/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 || listed[0].Key != "db/7/data.tar" || listed[0].Size != int64(len(big)) {
		t.Fatalf("unexpected listing: %+v", listed)
	}

	// The service checks the body against the hash that was signed.
	if err := s3.Put(ctx, "db/9/data.tar", strings.NewReader("not what was signed"), 19, hex.EncodeToString(sum[:])); err == nil {
		t.Error("a body that does not match its signed hash was accepted")
	}

	if err := s3.Delete(ctx, "db/7/data.tar"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s3.Delete(ctx, "db/7/data.tar"); err != nil {
		t.Fatalf("deleting what is gone: %v", err)
	}
	if _, err := s3.Get(ctx, "db/7/data.tar"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("got %v, want ErrObjectNotFound", err)
	}
}

func TestRealBucketRejectsAWrongSecretInWords(t *testing.T) {
	s3 := realBucket(t, "")
	wrong := *s3
	wrong.cfg.SecretAccessKey = "not-the-secret-0123456789"
	_, err := wrong.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") || strings.Contains(err.Error(), "not-the-secret") {
		t.Fatalf("got %v, want the service's SignatureDoesNotMatch and no secret", err)
	}
}

func TestRealBucketHoldsEncryptedBackupsThatReadBack(t *testing.T) {
	s3 := realBucket(t, "")
	dir := t.TempDir()
	s := New(dir, s3, "a passphrase for the test")
	ctx := context.Background()
	content := make([]byte, 1<<20+123)
	rand.Read(content)

	if _, err := s.Write(ctx, "db", 3, "data.tar", bytes.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := s.Write(ctx, "db", 4, "data.tar", bytes.NewReader(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The server's disk is gone; the bucket still restores.
	if err := os.RemoveAll(filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Open(ctx, "db", 3, "data.tar", true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read %d bytes back from the bucket, want %d (%v)", len(got), len(content), err)
	}

	if err := s.Remove(ctx, "db", 3); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	left, err := s3.List(ctx, "db/")
	if err != nil || len(left) != 1 || left[0].Key != "db/4/data.tar.enc" {
		t.Fatalf("after removing run 3 the bucket holds %+v (%v)", left, err)
	}
}
