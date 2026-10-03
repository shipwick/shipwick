package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup/backuptest"
)

// The signer against the vectors AWS publishes: the get-vanilla and
// post-vanilla cases of the Signature Version 4 test suite, and the worked
// examples of the S3 API reference ("Signature Calculations for the
// Authorization Header: Transferring Payload in a Single Chunk").
func TestSignV4MatchesThePublishedVectors(t *testing.T) {
	const (
		suiteSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		s3Secret    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		s3Host      = "https://examplebucket.s3.amazonaws.com"
	)
	suiteTime := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	s3Time := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	welcome := sha256.Sum256([]byte("Welcome to Amazon S3."))

	tests := map[string]struct {
		method, url    string
		headers        map[string]string
		service        string
		accessKey      string
		secret         string
		payload        string
		at             time.Time
		signedHeaders  string
		wantSignature  string
		wantCredential string
	}{
		"suite: get-vanilla": {
			method: "GET", url: "https://example.amazonaws.com/",
			service: "service", accessKey: "AKIDEXAMPLE", secret: suiteSecret, payload: emptyPayload, at: suiteTime,
			signedHeaders: "host;x-amz-date",
			wantSignature: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31",
		},
		"suite: get-vanilla-query-order-key-case": {
			method: "GET", url: "https://example.amazonaws.com/?Param2=value2&Param1=value1",
			service: "service", accessKey: "AKIDEXAMPLE", secret: suiteSecret, payload: emptyPayload, at: suiteTime,
			signedHeaders: "host;x-amz-date",
			wantSignature: "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500",
		},
		"suite: post-vanilla": {
			method: "POST", url: "https://example.amazonaws.com/",
			service: "service", accessKey: "AKIDEXAMPLE", secret: suiteSecret, payload: emptyPayload, at: suiteTime,
			signedHeaders: "host;x-amz-date",
			wantSignature: "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b",
		},
		"s3: get object": {
			method: "GET", url: s3Host + "/test.txt",
			headers: map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": emptyPayload},
			service: "s3", accessKey: "AKIAIOSFODNN7EXAMPLE", secret: s3Secret, payload: emptyPayload, at: s3Time,
			signedHeaders: "host;range;x-amz-content-sha256;x-amz-date",
			wantSignature: "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		"s3: put object": {
			method: "PUT", url: s3Host + "/test%24file.text",
			headers: map[string]string{
				"Date":                 "Fri, 24 May 2013 00:00:00 GMT",
				"X-Amz-Storage-Class":  "REDUCED_REDUNDANCY",
				"X-Amz-Content-Sha256": hex.EncodeToString(welcome[:]),
			},
			service: "s3", accessKey: "AKIAIOSFODNN7EXAMPLE", secret: s3Secret, payload: hex.EncodeToString(welcome[:]), at: s3Time,
			signedHeaders: "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			wantSignature: "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		"s3: get bucket lifecycle": {
			method: "GET", url: s3Host + "/?lifecycle",
			headers: map[string]string{"X-Amz-Content-Sha256": emptyPayload},
			service: "s3", accessKey: "AKIAIOSFODNN7EXAMPLE", secret: s3Secret, payload: emptyPayload, at: s3Time,
			signedHeaders: "host;x-amz-content-sha256;x-amz-date",
			wantSignature: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		"s3: list objects": {
			method: "GET", url: s3Host + "/?max-keys=2&prefix=J",
			headers: map[string]string{"X-Amz-Content-Sha256": emptyPayload},
			service: "s3", accessKey: "AKIAIOSFODNN7EXAMPLE", secret: s3Secret, payload: emptyPayload, at: s3Time,
			signedHeaders: "host;x-amz-content-sha256;x-amz-date",
			wantSignature: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			signV4(req, tc.service, "us-east-1", tc.accessKey, tc.secret, tc.payload, tc.at)

			want := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s/us-east-1/%s/aws4_request, SignedHeaders=%s, Signature=%s",
				tc.accessKey, tc.at.Format("20060102"), tc.service, tc.signedHeaders, tc.wantSignature)
			if got := req.Header.Get("Authorization"); got != want {
				t.Errorf("Authorization:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestAWSEscapeLeavesOnlyUnreservedCharacters(t *testing.T) {
	if got, want := awsEscape("a b+c/d~e_f-g.h$é"), "a%20b%2Bc%2Fd~e_f-g.h%24%C3%A9"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if got, want := escapePath("/bucket/my app/1/data.tar"), "/bucket/my%20app/1/data.tar"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func testBucket(t *testing.T, prefix string) (*S3, *backuptest.S3) {
	t.Helper()
	fake := backuptest.NewS3(t)
	s3, err := NewS3(S3Config{
		Endpoint: fake.URL, Bucket: backuptest.Bucket, Region: backuptest.Region,
		AccessKeyID: backuptest.AccessKeyID, SecretAccessKey: backuptest.SecretAccessKey, Prefix: prefix,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return s3, fake
}

func put(t *testing.T, s3 *S3, key, content string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	if err := s3.Put(context.Background(), key, strings.NewReader(content), int64(len(content)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

func TestS3PutGetListDelete(t *testing.T) {
	ctx := context.Background()
	s3, fake := testBucket(t, "")
	put(t, s3, "db/7/data.tar", "archive seven")
	put(t, s3, "db/8/data.tar", "archive eight")
	put(t, s3, "db/8/with space & plus+.tar", "odd name")
	put(t, s3, "db/8/empty.tar", "")

	rc, err := s3.Get(ctx, "db/8/with space & plus+.tar")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "odd name" {
		t.Fatalf("got %q back", got)
	}

	objects, err := s3.List(ctx, "db/8/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 3 || objects[0].Key != "db/8/data.tar" || objects[0].Size != 13 {
		t.Fatalf("unexpected listing: %+v", objects)
	}

	if err := s3.Delete(ctx, "db/8/data.tar"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s3.Delete(ctx, "db/8/data.tar"); err != nil {
		t.Fatalf("deleting what is gone must not fail: %v", err)
	}
	if _, err := s3.Get(ctx, "db/8/data.tar"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("got %v, want ErrObjectNotFound", err)
	}
	if keys := fake.Keys(); len(keys) != 3 {
		t.Fatalf("bucket holds %v", keys)
	}
}

func TestS3ListFollowsPagination(t *testing.T) {
	s3, fake := testBucket(t, "")
	fake.SetPageSize(2)
	for i := range 5 {
		put(t, s3, fmt.Sprintf("db/1/v%d.tar", i), "x")
	}
	put(t, s3, "other/1/v.tar", "x")
	objects, err := s3.List(context.Background(), "db/1/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 5 {
		t.Fatalf("got %d objects over the pages, want 5: %+v", len(objects), objects)
	}
}

func TestS3PrefixIsAppliedAndHidden(t *testing.T) {
	s3, fake := testBucket(t, "/servers/alpha/")
	put(t, s3, "db/1/data.tar", "x")
	if keys := fake.Keys(); len(keys) != 1 || keys[0] != "servers/alpha/db/1/data.tar" {
		t.Fatalf("bucket holds %v", keys)
	}
	objects, err := s3.List(context.Background(), "db/")
	if err != nil || len(objects) != 1 || objects[0].Key != "db/1/data.tar" {
		t.Fatalf("listing: %+v, %v", objects, err)
	}
}

func TestS3RefusesABodyThatIsNotWhatWasSigned(t *testing.T) {
	s3, fake := testBucket(t, "")
	sum := sha256.Sum256([]byte("what was meant"))
	err := s3.Put(context.Background(), "db/1/data.tar", strings.NewReader("what was sent!"), 14, hex.EncodeToString(sum[:]))
	if err == nil || len(fake.Keys()) != 0 {
		t.Fatalf("a body that does not match its hash was stored: %v", err)
	}
}

func TestS3ErrorsNameTheServicesReasonAndNoCredential(t *testing.T) {
	fake := backuptest.NewS3(t)
	s3, err := NewS3(S3Config{Endpoint: fake.URL, Bucket: backuptest.Bucket, Region: backuptest.Region,
		AccessKeyID: backuptest.AccessKeyID, SecretAccessKey: "not-the-secret-0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s3.List(context.Background(), "db/")
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("got %v, want the service's code", err)
	}
	if strings.Contains(err.Error(), "not-the-secret") || strings.Contains(err.Error(), backuptest.AccessKeyID) {
		t.Fatalf("the error carries a credential: %v", err)
	}
}

func TestS3RefusesAnArchiveLargerThanOneUploadTakes(t *testing.T) {
	s3, fake := testBucket(t, "")
	err := s3.Put(context.Background(), "db/1/data.tar", bytes.NewReader(nil), maxPutBytes+1, emptyPayload)
	if err == nil || len(fake.Requests()) != 0 {
		t.Fatalf("got %v after %d requests, want a refusal before any", err, len(fake.Requests()))
	}
}

func TestNewS3Validation(t *testing.T) {
	good := S3Config{Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	if s3, err := NewS3(good); err != nil || s3.cfg.Region != "auto" {
		t.Fatalf("a complete configuration was refused, or the region has no default: %v", err)
	}
	for name, change := range map[string]func(*S3Config){
		"no scheme":       func(c *S3Config) { c.Endpoint = "s3.example.com" },
		"credentials":     func(c *S3Config) { c.Endpoint = "https://id:secret@s3.example.com" },
		"query":           func(c *S3Config) { c.Endpoint = "https://s3.example.com/?x=1" },
		"no bucket":       func(c *S3Config) { c.Bucket = "" },
		"bucket path":     func(c *S3Config) { c.Bucket = "a/b" },
		"no access key":   func(c *S3Config) { c.AccessKeyID = "" },
		"no secret":       func(c *S3Config) { c.SecretAccessKey = "" },
		"other transport": func(c *S3Config) { c.Endpoint = "ftp://s3.example.com" },
	} {
		cfg := good
		change(&cfg)
		if _, err := NewS3(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "secret") && name == "credentials" {
			t.Errorf("%s: the error repeats the endpoint's credentials: %v", name, err)
		}
	}
}
