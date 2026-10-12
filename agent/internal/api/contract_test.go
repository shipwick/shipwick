package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// What the API promises of its shape, route by route: every answer is the
// envelope, and a list is never null. These tests walk the route table, so an
// endpoint added later is held to the same two rules without being named.

// concretePath fills the parameters of a route's pattern with values a
// request can carry.
func concretePath(path string) string {
	r := strings.NewReplacer(
		"{name}", "my-api",
		"{id}", "1",
		"{job}", "nightly",
		"{volume}", "data",
		"{registry}", "ghcr.io",
		"{hostname}", "example.com",
		"{email}", "someone@example.com",
	)
	out := r.Replace(path)
	if strings.Contains(out, "{") {
		panic("concretePath: a parameter this test does not know in " + path)
	}
	return out
}

// unauthenticatedRoutes are registered on the mux directly, outside the route
// table, and have to be named here.
var unauthenticatedRoutes = []string{"GET /api/v1/health", "GET /api/v1/auth", "POST /api/v1/auth/exchange"}

func (f *fixture) everyRoute() []string {
	patterns := append([]string(nil), unauthenticatedRoutes...)
	for _, rt := range f.api.routes {
		patterns = append(patterns, rt.pattern())
	}
	sort.Strings(patterns)
	return patterns
}

// envelope decodes body as the error envelope and fails the test when it is
// anything else: plain text from the mux or the server, or a shape of its own.
func envelope(t *testing.T, what string, status int, header http.Header, body []byte) api.Error {
	t.Helper()
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("%s: answered %d with Content-Type %q, not JSON: %q", what, status, ct, body)
	}
	var e api.ErrorResponse
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		t.Fatalf("%s: answered %d without the error envelope: %q", what, status, body)
	}
	if e.Error.Details == nil {
		t.Errorf("%s: details is null; the envelope promises an object", what)
	}
	return e.Error
}

// anyEnvelope accepts a success or an error envelope and fails on anything
// else.
func anyEnvelope(t *testing.T, what string, status int, header http.Header, body []byte) {
	t.Helper()
	if status >= 400 {
		envelope(t, what, status, header, body)
		return
	}
	if status == http.StatusNoContent {
		return
	}
	var answer map[string]json.RawMessage
	if err := json.Unmarshal(body, &answer); err != nil || answer["data"] == nil {
		t.Fatalf("%s: answered %d without the data envelope: %q", what, status, body)
	}
}

func (f *fixture) request(method, path, body string, header map[string]string) (int, http.Header, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	data := make([]byte, 1<<20)
	for {
		n, err := resp.Body.Read(data)
		buf.Write(data[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, []byte(buf.String())
}

func TestEveryRouteAnswersInTheEnvelope(t *testing.T) {
	f := newFixture(t)
	huge := strings.Repeat("x", 6<<20)

	for _, pattern := range f.everyRoute() {
		method, path, _ := strings.Cut(pattern, " ")
		path = concretePath(path)

		// A method the path does not take: the mux must not answer 405 in
		// plain text, and the code must say the operation does not exist.
		t.Run("PATCH "+path, func(t *testing.T) {
			status, header, body := f.request("PATCH", path, "", nil)
			e := envelope(t, "PATCH "+path, status, header, body)
			if status != http.StatusNotFound || e.Code != api.CodeEndpointNotFound {
				t.Errorf("answered %d %s, want 404 %s", status, e.Code, api.CodeEndpointNotFound)
			}
		})
		if method == "GET" {
			continue
		}
		// A body far beyond any limit, and a body that is not JSON: whatever
		// the handler makes of them — an endpoint that takes no body ignores
		// it — it answers in the envelope, never in the server's plain text.
		t.Run(pattern+" with a huge body", func(t *testing.T) {
			status, header, body := f.request(method, path, huge, map[string]string{"Content-Type": "application/octet-stream"})
			anyEnvelope(t, pattern, status, header, body)
		})
		t.Run(pattern+" with a broken body", func(t *testing.T) {
			status, header, body := f.request(method, path, "{", map[string]string{"Content-Type": "application/json"})
			anyEnvelope(t, pattern, status, header, body)
		})
	}

	// Paths the agent has nothing at, with and without a token.
	for _, path := range []string{"/", "/api", "/api/v1", "/api/v1/nope", "/api/v2/health", "/api/v1/applications/my-api/nope", "/metrics/nope", "/api/v1/applications/my-api/"} {
		for _, auth := range []string{"", "Bearer " + testToken} {
			status, body := f.doWithAuth("GET", path, "", auth)
			var e api.ErrorResponse
			if err := json.Unmarshal(body, &e); err != nil || status != http.StatusNotFound || e.Error.Code != api.CodeEndpointNotFound {
				t.Errorf("GET %s (auth %q): %d %q, want 404 %s", path, auth, status, body, api.CodeEndpointNotFound)
			}
		}
	}
}

// listKeys are the JSON names of every slice-typed field of the API's types,
// omitempty ones aside: in an answer each must be [] when there is nothing,
// never null. The types are walked, so a type added to the list is covered
// at every depth.
func listKeys() (always, omitted map[string]bool) {
	always, omitted = map[string]bool{}, map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] || t == reflect.TypeOf(time.Time{}) {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			fld := t.Field(i)
			name, opts, _ := strings.Cut(fld.Tag.Get("json"), ",")
			if fld.Anonymous && name == "" {
				walk(fld.Type)
				continue
			}
			if name == "-" || name == "" {
				continue
			}
			ft := fld.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() != reflect.Uint8 {
				if strings.Contains(opts, "omitempty") {
					omitted[name] = true
				} else {
					always[name] = true
				}
			}
			walk(fld.Type)
		}
	}
	for _, v := range []any{
		api.Server{}, api.ApplicationDetail{}, api.DeploymentDetail{}, api.Metrics{}, api.MetricsHistory{},
		api.Token{}, api.CreatedToken{}, api.Job{}, api.RunDetail{}, api.Volume{}, api.VolumeInfo{}, api.Secret{},
		api.Registry{}, api.Certificate{}, api.Traffic{}, api.Request{}, api.BackupRunDetail{}, api.Import{},
		api.Standby{}, api.Promotion{}, api.AuditEntry{}, api.LogArchiveDetail{}, api.LogSearchResult{},
		api.ApplicationConfig{}, api.Alert{}, api.KeyRotation{}, api.BackupAdoption{}, api.Validation{},
		api.LoadedImage{}, api.StaticUpload{}, api.MissingLayers{}, api.Event{}, api.LogLine{}, api.HostnameCertificate{},
		api.CloudflareStatus{}, api.AccessRule{}, api.Session{}, api.SignInConfig{}, api.SignInStatus{}, api.AuditPage{}, api.SignedOut{},
		spec.App{},
	} {
		walk(reflect.TypeOf(v))
	}
	return always, omitted
}

// nullLists walks a decoded answer and names every list key that is null.
func nullLists(v any, path string, keys map[string]bool, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := path + "." + k
			if child == nil && keys[k] {
				*out = append(*out, p)
			}
			nullLists(child, p, keys, out)
		}
	case []any:
		for _, child := range x {
			nullLists(child, path+"[]", keys, out)
		}
	}
}

func TestListsAreNeverNull(t *testing.T) {
	f := newFixture(t)
	always, _ := listKeys()
	if len(always) < 20 {
		t.Fatalf("found only %d list keys; the walk is broken", len(always))
	}

	// An application with a deployment, so that the answers have something in
	// them, and a second one that is stopped.
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	f.createToken("ci", api.RoleDeploy)

	var failures []string
	for _, pattern := range f.everyRoute() {
		method, path, _ := strings.Cut(pattern, " ")
		if method != "GET" || path == "/metrics" || strings.HasSuffix(path, "/archive") && strings.Contains(path, "volumes") {
			continue
		}
		path = concretePath(path)
		status, header, body := f.request("GET", path, "", nil)
		if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
			continue // a stream or a file, not an envelope
		}
		var answer map[string]any
		if err := json.Unmarshal(body, &answer); err != nil {
			t.Errorf("GET %s: %d, not JSON: %q", path, status, body)
			continue
		}
		if status != http.StatusOK {
			continue
		}
		var nulls []string
		nullLists(answer["data"], "data", always, &nulls)
		for _, n := range nulls {
			failures = append(failures, "GET "+path+": "+n)
		}
	}
	sort.Strings(failures)
	for _, failure := range failures {
		t.Error(failure + " is null; a list with nothing in it is []")
	}
}
