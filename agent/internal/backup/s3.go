package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/outbound"
)

// S3Config names a bucket on an S3-compatible service. The two keys are
// credentials and are never logged; errors name the bucket and the object,
// never the keys.
type S3Config struct {
	Endpoint        string // https://s3.eu-central-1.amazonaws.com, https://<account>.r2.cloudflarestorage.com, ...
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// Prefix is put in front of every object's key, for a bucket shared with
	// something else or between servers.
	Prefix string
	// PartSize and RetryPause are not settings: zero means the defaults of
	// multipart.go, and tests make them small.
	PartSize   int64
	RetryPause time.Duration
}

// emptyPayload is the SHA-256 of nothing, the payload hash of requests
// without a body.
const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// ErrObjectNotFound means the bucket holds no object under that key.
var ErrObjectNotFound = errors.New("no such object in the bucket")

// S3 is a client for the operations backups need — put, get, list, delete,
// and the multipart upload of what does not fit one put — on an S3-compatible
// service, addressed path-style (endpoint/bucket/key), which every such
// service accepts. Requests are signed with AWS Signature Version 4.
type S3 struct {
	cfg      S3Config
	endpoint *url.URL
	http     *http.Client
	now      func() time.Time
	// See Upload.
	partSize   int64
	retryPause time.Duration
}

// NewS3 validates the configuration. It makes no request.
func NewS3(cfg S3Config) (*S3, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("the endpoint must be a URL such as https://s3.eu-central-1.amazonaws.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("the endpoint must be a plain URL, without credentials or a query")
	}
	if cfg.Bucket == "" || strings.ContainsAny(cfg.Bucket, "/ \t\r\n") {
		return nil, errors.New("the bucket must be a bucket's name")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("the access key id and the secret access key are both required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	u.Path = strings.TrimSuffix(u.Path, "/")
	if cfg.PartSize <= 0 {
		cfg.PartSize = defaultPartSize
	}
	if cfg.RetryPause <= 0 {
		cfg.RetryPause = defaultRetryPause
	}
	return &S3{
		partSize:   cfg.PartSize,
		retryPause: cfg.RetryPause,
		cfg:        cfg,
		endpoint:   u,
		// No overall timeout: an archive takes as long as it takes. A server
		// that stops answering is caught by the header timeout.
		http: &http.Client{Transport: &http.Transport{
			Proxy:                  outbound.Proxy,
			OnProxyConnectResponse: outbound.ProxyRefused,
			TLSClientConfig:        outbound.TLSConfig(),
			ResponseHeaderTimeout:  2 * time.Minute,
			TLSHandshakeTimeout:    15 * time.Second,
			IdleConnTimeout:        time.Minute,
		}},
		now: time.Now,
	}, nil
}

// Host is the endpoint's host, for logs: where backups go, without a secret.
func (s *S3) Host() string { return s.endpoint.Host }

// Bucket is the bucket's name.
func (s *S3) Bucket() string { return s.cfg.Bucket }

func (s *S3) key(key string) string {
	if s.cfg.Prefix == "" {
		return key
	}
	return s.cfg.Prefix + "/" + key
}

// Put stores size bytes read from body under key, in one request. sha256Hex
// is the SHA-256 of those bytes: the signature covers it, so the service
// refuses a body that arrives different from what was signed. What may be
// larger than a part goes through Upload.
func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, sha256Hex string) error {
	req, err := s.request(ctx, http.MethodPut, s.key(key), nil, body, sha256Hex)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload %s: %w", key, responseError(resp))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// Get streams the object under key. The caller closes it.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := s.request(ctx, http.MethodGet, s.key(key), nil, nil, emptyPayload)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, redactURL(err))
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %w", key, ErrObjectNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("download %s: %w", key, responseError(resp))
	}
	return resp.Body, nil
}

// Delete removes the object under key. A missing object is not an error, as
// on the service itself.
func (s *S3) Delete(ctx context.Context, key string) error {
	req, err := s.request(ctx, http.MethodDelete, s.key(key), nil, nil, emptyPayload)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, redactURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete %s: %w", key, responseError(resp))
	}
	return nil
}

// Object is one entry of a listing.
type Object struct {
	Key  string // without the configured prefix
	Size int64
	// Modified is when the object was written; zero when the service did not
	// say.
	Modified time.Time
}

// List returns the objects whose key starts with prefix, in key order,
// following the service's pagination to the end.
func (s *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {s.key(prefix)}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		req, err := s.request(ctx, http.MethodGet, "", q, nil, emptyPayload)
		if err != nil {
			return nil, err
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, redactURL(err))
		}
		if resp.StatusCode != http.StatusOK {
			err := responseError(resp)
			resp.Body.Close()
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		var page struct {
			IsTruncated           bool
			NextContinuationToken string
			Contents              []struct {
				Key          string
				Size         int64
				LastModified string
			}
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list %s: the answer is not a bucket listing: %w", prefix, err)
		}
		for _, o := range page.Contents {
			key := o.Key
			if s.cfg.Prefix != "" {
				key = strings.TrimPrefix(key, s.cfg.Prefix+"/")
			}
			modified, _ := time.Parse(time.RFC3339Nano, o.LastModified)
			out = append(out, Object{Key: key, Size: o.Size, Modified: modified})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// request builds a signed request for an object of the bucket; an empty key
// addresses the bucket itself.
func (s *S3) request(ctx context.Context, method, key string, query url.Values, body io.Reader, payloadHash string) (*http.Request, error) {
	u := *s.endpoint
	u.Path += "/" + s.cfg.Bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath = escapePath(u.Path)
	u.RawQuery = canonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signV4(req, "s3", s.cfg.Region, s.cfg.AccessKeyID, s.cfg.SecretAccessKey, payloadHash, s.now())
	return req, nil
}

// signV4 signs req with AWS Signature Version 4: it sets X-Amz-Date and
// Authorization. Every header set on the request so far is signed, and Host.
// payloadHash is the hex SHA-256 of the body.
//
// The canonical path is the request's escaped path as it is sent, encoded
// once, which is S3's rule; other AWS services encode it twice.
func signV4(req *http.Request, service, region, accessKeyID, secret, payloadHash string, now time.Time) {
	now = now.UTC()
	stamp, day := now.Format("20060102T150405Z"), now.Format("20060102")
	req.Header.Set("X-Amz-Date", stamp)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = strings.Join(trimAll(values), ",")
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + headers[name] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method,
		path,
		canonicalQuery(req.URL.Query()),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := day + "/" + region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// trimAll trims each value and collapses runs of spaces inside it, as the
// canonical form of a header wants.
func trimAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

// canonicalQuery is the query string in the signature's canonical form:
// sorted by name, then by value, both percent-encoded the AWS way. It is also
// what is sent, so that what was signed is what arrives.
func canonicalQuery(q url.Values) string {
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		values := append([]string(nil), q[name]...)
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, awsEscape(name)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// escapePath percent-encodes each segment of a path and keeps the slashes.
func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = awsEscape(s)
	}
	return strings.Join(segments, "/")
}

// awsEscape percent-encodes everything but the unreserved characters of
// RFC 3986, with upper-case hex: url.QueryEscape would write a space as "+"
// and leave the choice of what else to escape to the standard library.
func awsEscape(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

// responseError turns an error response into one line: the status and the
// service's own code and message, which is where "the bucket does not exist"
// and "the signature does not match" are told apart.
func responseError(resp *http.Response) error {
	var body struct {
		Code    string
		Message string
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if xml.Unmarshal(data, &body) == nil && body.Code != "" {
		return fmt.Errorf("the bucket answered HTTP %d %s: %s", resp.StatusCode, body.Code, strings.TrimSuffix(body.Message, "."))
	}
	return fmt.Errorf("the bucket answered HTTP %d", resp.StatusCode)
}

// redactURL strips the URL from a transport error. It carries nothing secret
// — the credentials travel in a header — but it repeats the object's key,
// which the message around it already names.
func redactURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
