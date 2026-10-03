// Package backuptest provides an in-memory S3-compatible server for tests. It
// checks every request's signature with an implementation of its own, so a
// client that signs wrongly fails here the way it would against a real one.
package backuptest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Credentials and names the server accepts.
const (
	AccessKeyID     = "AKIDTESTTESTTEST"
	SecretAccessKey = "test-secret-access-key/with+symbols"
	Bucket          = "backups"
	Region          = "auto"
)

// S3 is the fake service. Its URL is the endpoint to configure.
type S3 struct {
	URL string

	mu      sync.Mutex
	objects map[string][]byte
	// pageSize is how many keys one listing page holds, 0 meaning 1000;
	// failPuts makes every upload answer 500. See SetPageSize and FailPuts.
	pageSize int
	failPuts bool
	requests []string
	// hold, when set, is what every upload waits for; arrived is closed when
	// the first one is waiting. See HoldPuts.
	hold, arrived chan struct{}
}

// NewS3 starts the server; it stops with the test.
func NewS3(t *testing.T) *S3 {
	s := &S3{objects: map[string][]byte{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Objects returns what the bucket holds, by key.
func (s *S3) Objects() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.objects))
	for k, v := range s.objects {
		out[k] = v
	}
	return out
}

// Keys returns the bucket's keys, sorted.
func (s *S3) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys("")
}

// Requests lists the requests so far as "METHOD path?query".
func (s *S3) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *S3) keys(prefix string) []string {
	var out []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *S3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	if r.Method == http.MethodPut {
		s.mu.Lock()
		hold, arrived := s.hold, s.arrived
		if arrived != nil {
			close(arrived)
			s.arrived = nil
		}
		s.mu.Unlock()
		if hold != nil {
			<-hold
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())

	if err := verify(r, body); err != nil {
		fail(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+Bucket)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		fail(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.")
		return
	}
	key := strings.TrimPrefix(rest, "/")

	switch {
	case r.Method == http.MethodGet && key == "":
		s.list(w, r)
	case r.Method == http.MethodPut:
		if s.failPuts {
			fail(w, http.StatusInternalServerError, "InternalError", "We encountered an internal error.")
			return
		}
		s.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		data, ok := s.objects[key]
		if !ok {
			fail(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		w.Write(data)
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
	}
}

func (s *S3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		fail(w, http.StatusBadRequest, "InvalidArgument", "only ListObjectsV2 is implemented")
		return
	}
	keys := s.keys(q.Get("prefix"))
	if token := q.Get("continuation-token"); token != "" {
		i := sort.SearchStrings(keys, token)
		keys = keys[i:]
	}
	size := s.pageSize
	if size == 0 {
		size = 1000
	}
	type content struct {
		Key  string
		Size int
	}
	var page struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		IsTruncated           bool
		NextContinuationToken string `xml:",omitempty"`
		Contents              []content
	}
	if len(keys) > size {
		page.IsTruncated, page.NextContinuationToken = true, keys[size]
		keys = keys[:size]
	}
	for _, k := range keys {
		page.Contents = append(page.Contents, content{Key: k, Size: len(s.objects[k])})
	}
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(page)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, message)
}

// verify recomputes the request's Signature Version 4 from what arrived.
func verify(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !ok {
		return fmt.Errorf("no AWS4-HMAC-SHA256 authorization")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ", ") {
		k, v, _ := strings.Cut(part, "=")
		fields[k] = v
	}
	credential := strings.Split(fields["Credential"], "/")
	if len(credential) != 5 || credential[0] != AccessKeyID || credential[2] != Region || credential[3] != "s3" || credential[4] != "aws4_request" {
		return fmt.Errorf("unexpected credential scope %q", fields["Credential"])
	}
	date := r.Header.Get("X-Amz-Date")
	if !strings.HasPrefix(date, credential[1]) {
		return fmt.Errorf("x-amz-date %q does not match the scope's day %s", date, credential[1])
	}
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != payload {
		return fmt.Errorf("x-amz-content-sha256 is %s, the body hashes to %s", got, payload)
	}

	signed := strings.Split(fields["SignedHeaders"], ";")
	for _, required := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		found := false
		for _, name := range signed {
			found = found || name == required
		}
		if !found {
			return fmt.Errorf("header %s is not signed", required)
		}
	}
	var headers strings.Builder
	for _, name := range signed {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		headers.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
	}
	// The query as it arrived, sorted: a client that sent it in another
	// encoding than it signed fails here.
	query := strings.Split(r.URL.RawQuery, "&")
	sort.Strings(query)
	canonical := strings.Join([]string{
		r.Method, r.URL.EscapedPath(), strings.Join(query, "&"), headers.String(), fields["SignedHeaders"], payload,
	}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + date + "\n" + strings.Join(credential[1:], "/") + "\n" + hex.EncodeToString(hashed[:])

	key := []byte("AWS4" + SecretAccessKey)
	for _, part := range append(credential[1:], toSign) {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	if want := hex.EncodeToString(key); fields["Signature"] != want {
		return fmt.Errorf("the signature does not match the request as it arrived")
	}
	return nil
}

// HoldPuts makes uploads wait until release is called. arrived is closed when
// the first upload is waiting: a test that wants to look at the world in the
// middle of a backup waits for it, with nothing to sleep through.
func (s *S3) HoldPuts() (arrived <-chan struct{}, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hold, first := make(chan struct{}), make(chan struct{})
	s.hold, s.arrived = hold, first
	return first, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.hold == hold {
			close(hold)
			s.hold = nil
		}
	}
}

// Put places an object in the bucket directly, as another writer would have.
func (s *S3) Put(key string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = content
}

// SetPageSize makes listings come in pages of n keys.
func (s *S3) SetPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// FailPuts makes every upload that follows answer 500, or stops doing so.
func (s *S3) FailPuts(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPuts = fail
}
