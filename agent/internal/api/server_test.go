package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

const testToken = "test-token-0123456789abcdef"

type fixture struct {
	t      *testing.T
	srv    *httptest.Server
	engine *deploy.Engine
	rt     *dockertest.Fake
	api    *Server
	logs   *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet})

	logs := &bytes.Buffer{}
	apiServer := New(engine, sha256.Sum256([]byte(testToken)), slog.New(slog.NewTextHandler(logs, nil)))
	handler := apiServer.Handler()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		apiServer.Close() // srv.Close would wait forever on an open log stream
		srv.Close()
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &fixture{t: t, srv: srv, engine: engine, rt: rt, api: apiServer, logs: logs}
}

// do sends an authenticated request and returns the status and raw body.
func (f *fixture) do(method, path, body string) (int, []byte) {
	f.t.Helper()
	return f.doWithAuth(method, path, body, "Bearer "+testToken)
}

func (f *fixture) doWithAuth(method, path, body, auth string) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var envelope api.Response[T]
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope.Data
}

func decodeError(t *testing.T, body []byte) api.Error {
	t.Helper()
	var envelope api.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope.Error
}

const validConfig = `
name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
replicas: 2
env:
  DATABASE_PASSWORD: hunter2
resources:
  cpu: 1
  memory: 512mb
`

func TestHealthNeedsNoToken(t *testing.T) {
	f := newFixture(t)
	status, body := f.doWithAuth("GET", "/api/v1/health", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if h := decode[api.Health](t, body); h.Status != "ok" {
		t.Errorf("unexpected health: %+v", h)
	}
}

func TestAuthentication(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"no header":     "",
		"wrong token":   "Bearer wrong-token-0123456789",
		"wrong scheme":  "Basic " + testToken,
		"empty bearer":  "Bearer ",
		"token as-is":   testToken,
		"prefix of it":  "Bearer " + testToken[:10],
		"with a suffix": "Bearer " + testToken + "x",
	}
	for name, auth := range tests {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/applications", "/api/v1/server", "/api/v1/deployments", "/api/v1/applications/x/logs"} {
				status, body := f.doWithAuth("GET", path, "", auth)
				if status != http.StatusUnauthorized {
					t.Fatalf("%s: status = %d, want 401", path, status)
				}
				if e := decodeError(t, body); e.Code != api.CodeUnauthorized {
					t.Errorf("%s: code = %q", path, e.Code)
				}
			}
		})
	}

	if status, _ := f.do("GET", "/api/v1/applications", ""); status != http.StatusOK {
		t.Errorf("valid token: status = %d, want 200", status)
	}
}

func TestTokenIsNeverLogged(t *testing.T) {
	f := newFixture(t)
	f.do("GET", "/api/v1/applications", "")
	f.doWithAuth("GET", "/api/v1/applications", "", "Bearer some-wrong-token-value")
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	logged := f.logs.String()
	if logged == "" {
		t.Fatal("expected request logs")
	}
	for _, secret := range []string{testToken, "some-wrong-token-value", "hunter2"} {
		if strings.Contains(logged, secret) {
			t.Errorf("logs contain the secret %q", secret)
		}
	}
}

func TestDeployLifecycle(t *testing.T) {
	f := newFixture(t)

	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	d := decode[api.Deployment](t, body)
	if d.Application != "my-api" || d.Version != "1.4.2" || d.Sequence != 1 {
		t.Errorf("unexpected deployment: %+v", d)
	}
	f.engine.Wait()

	status, body = f.do("GET", "/api/v1/deployments/1", "")
	if status != http.StatusOK {
		t.Fatalf("get deployment: status = %d", status)
	}
	detail := decode[api.DeploymentDetail](t, body)
	if detail.Status != api.StatusActive || len(detail.Events) == 0 {
		t.Errorf("unexpected deployment detail: %+v", detail)
	}
	if strings.Contains(string(body), "hunter2") {
		t.Error("deployment detail leaks env values")
	}

	status, body = f.do("GET", "/api/v1/applications/my-api", "")
	if status != http.StatusOK {
		t.Fatalf("get application: status = %d", status)
	}
	app := decode[api.ApplicationDetail](t, body)
	if app.Status != api.AppHealthy || app.Replicas != (api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}) || len(app.Containers) != 2 {
		t.Errorf("unexpected application: %+v", app)
	}
	if _, ok := app.Spec.Env["DATABASE_PASSWORD"]; !ok || strings.Contains(string(body), "hunter2") {
		t.Error("env names should be listed, values masked")
	}

	_, body = f.do("GET", "/api/v1/applications", "")
	if apps := decode[[]api.Application](t, body); len(apps) != 1 || apps[0].Name != "my-api" {
		t.Errorf("unexpected application list: %+v", apps)
	}

	_, body = f.do("GET", "/api/v1/applications/my-api/logs?tail=10", "")
	if lines := decode[[]api.LogLine](t, body); len(lines) != 2 {
		t.Errorf("got %d log lines, want 2", len(lines))
	}

	status, body = f.do("POST", "/api/v1/applications/my-api/stop", "")
	if stopped := decode[api.ApplicationDetail](t, body); status != http.StatusOK || stopped.Status != api.AppStopped {
		t.Errorf("stop: status = %d, app = %+v", status, stopped.Application)
	}
	status, body = f.do("POST", "/api/v1/applications/my-api/start", "")
	if started := decode[api.ApplicationDetail](t, body); status != http.StatusOK || started.Status != api.AppHealthy {
		t.Errorf("start: status = %d, app = %+v", status, started.Application)
	}

	if status, _ = f.do("DELETE", "/api/v1/applications/my-api", ""); status != http.StatusNoContent {
		t.Errorf("delete: status = %d, want 204", status)
	}
	if status, _ = f.do("GET", "/api/v1/applications/my-api", ""); status != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want 404", status)
	}
	if n := len(f.rt.Containers()); n != 0 {
		t.Errorf("%d containers left after delete", n)
	}
}

func TestDeployAcceptsJSON(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/applications/web/deploy", `{"name": "web", "image": "nginx:1.27", "port": 80}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

func TestDeployValidation(t *testing.T) {
	f := newFixture(t)

	t.Run("invalid config lists every field", func(t *testing.T) {
		status, body := f.do("POST", "/api/v1/applications/my-api/deploy",
			"name: my-api\nimage: nginx\nport: 99999\nresources:\n  memory: abc\n")
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		e := decodeError(t, body)
		fields, _ := e.Details["fields"].([]any)
		if e.Code != api.CodeInvalidConfig || len(fields) != 2 {
			t.Errorf("unexpected error: %+v", e)
		}
	})

	t.Run("name mismatch", func(t *testing.T) {
		status, body := f.do("POST", "/api/v1/applications/other/deploy", validConfig)
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("status = %d, error = %+v", status, e)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		status, _ := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig+"#"+strings.Repeat("x", 70*1024))
		if status != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", status)
		}
	})

	if all := decodeList(t, f); len(all) != 0 {
		t.Errorf("rejected requests must not create deployments, got %d", len(all))
	}
}

func decodeList(t *testing.T, f *fixture) []api.Deployment {
	t.Helper()
	_, body := f.do("GET", "/api/v1/deployments", "")
	return decode[[]api.Deployment](t, body)
}

func TestConcurrentDeployConflicts(t *testing.T) {
	f := newFixture(t)
	f.rt.PullDelay = 300 * time.Millisecond

	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig); status != http.StatusAccepted {
		t.Fatalf("first deploy: status = %d, body = %s", status, body)
	}
	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeDeploymentInProgress {
		t.Errorf("second deploy: status = %d, error = %+v", status, e)
	}

	_, body = f.do("GET", "/api/v1/applications/my-api", "")
	if app := decode[api.ApplicationDetail](t, body); app.Status != api.AppDeploying || !app.Deploying || app.InFlightDeploymentID == nil || *app.InFlightDeploymentID != 1 {
		t.Errorf("during first deploy: %+v; clients need the id of the deployment to follow", app.Application)
	}
}

func TestInvalidPathParameters(t *testing.T) {
	f := newFixture(t)
	tests := []struct{ method, path string }{
		{"GET", "/api/v1/applications/Invalid_Name"},
		{"GET", "/api/v1/applications/..%2F..%2Fetc"},
		{"POST", "/api/v1/applications/-bad-/stop"},
		{"GET", "/api/v1/deployments/abc"},
		{"GET", "/api/v1/deployments/0"},
		{"GET", "/api/v1/deployments?limit=-1"},
		{"GET", "/api/v1/deployments?application=Bad_Name"},
		{"GET", "/api/v1/applications/my-api/logs?tail=999999"},
	}
	for _, tt := range tests {
		status, body := f.do(tt.method, tt.path, "")
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
		}
	}
}

func TestNotFoundAndConflictErrors(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/api/v1/applications/ghost", http.StatusNotFound, api.CodeNotFound},
		{"DELETE", "/api/v1/applications/ghost", http.StatusNotFound, api.CodeNotFound},
		{"POST", "/api/v1/applications/ghost/stop", http.StatusNotFound, api.CodeNotFound},
		{"GET", "/api/v1/applications/ghost/logs", http.StatusNotFound, api.CodeNotFound},
		{"GET", "/api/v1/deployments/12345", http.StatusNotFound, api.CodeNotFound},
		{"GET", "/api/v1/deployments?application=ghost", http.StatusNotFound, api.CodeNotFound},
		// "No such operation" is a different fact from "no such application":
		// clients use it to tell an older agent from a typo.
		{"GET", "/api/v1/nope", http.StatusNotFound, api.CodeEndpointNotFound},
		{"PATCH", "/api/v1/applications", http.StatusNotFound, api.CodeEndpointNotFound},
	}
	for _, tt := range tests {
		status, body := f.do(tt.method, tt.path, "")
		e := decodeError(t, body)
		if status != tt.status || e.Code != tt.code {
			t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
		}
		if e.Details == nil {
			t.Errorf("%s %s: details must always be an object", tt.method, tt.path)
		}
	}
}

func TestResponsesCarrySecurityHeaders(t *testing.T) {
	f := newFixture(t)
	resp, err := http.Get(f.srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing security headers: %v", resp.Header)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// openStream starts a follow request and returns the response for line-by-line reading.
func (f *fixture) openStream(ctx context.Context, path string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", f.srv.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestFollowLogsStreamsNDJSON(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := f.openStream(ctx, "/api/v1/applications/my-api/logs?follow=true&tail=10")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status = %d, content type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// One line per replica arrives while the stream stays open.
	dec := json.NewDecoder(resp.Body)
	replicas := map[int]bool{}
	for range 2 {
		var line api.LogLine
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("decode streamed line: %v", err)
		}
		if line.Message == "" || line.Container == "" {
			t.Errorf("malformed line: %+v", line)
		}
		replicas[line.Replica] = true
	}
	if len(replicas) != 2 {
		t.Errorf("want lines from both replicas, got %v", replicas)
	}
}

func TestFollowLogsEndsWhenServerCloses(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := f.openStream(ctx, "/api/v1/applications/my-api/logs?follow=true&tail=0")

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	f.api.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stream should end cleanly, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open after Server.Close; agent shutdown would hang on it")
	}
}

func TestFollowLogsErrorsAreRegularJSON(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/applications/ghost/logs?follow=true", http.StatusNotFound, api.CodeNotFound},
		{"/api/v1/applications/my-api/logs?follow=maybe", http.StatusBadRequest, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs?follow=true&tail=-1", http.StatusBadRequest, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs?tail=0", http.StatusBadRequest, api.CodeInvalidRequest}, // 0 only makes sense when following
	}
	for _, tt := range tests {
		status, body := f.do("GET", tt.path, "")
		if e := decodeError(t, body); status != tt.status || e.Code != tt.code {
			t.Errorf("%s: status = %d, error = %+v", tt.path, status, e)
		}
	}
}

func TestApplicationEvents(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	f.do("POST", "/api/v1/applications/my-api/stop", "")
	f.do("POST", "/api/v1/applications/my-api/start", "")

	status, body := f.do("GET", "/api/v1/applications/my-api/events?limit=10", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	events := decode[[]api.Event](t, body)
	if len(events) != 2 || events[0].Message != "Application started" || events[1].Message != "Application stopped" {
		t.Errorf("want the application's own events, newest first; got %+v", events)
	}
	for _, e := range events {
		if e.DeploymentID != nil {
			t.Errorf("deployment events belong to GET /deployments/:id, not here: %+v", e)
		}
	}

	if status, _ := f.do("GET", "/api/v1/applications/ghost/events", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: status = %d, want 404", status)
	}
	if status, _ := f.do("GET", "/api/v1/applications/my-api/events?limit=0", ""); status != http.StatusBadRequest {
		t.Errorf("limit=0: status = %d, want 400", status)
	}
}

func TestDomainConflictIsAConfigError(t *testing.T) {
	f := newFixture(t)
	const first = "name: web\nimage: nginx:1.27\nport: 80\ndomain: www.example.com\n"
	if status, body := f.do("POST", "/api/v1/applications/web/deploy", first); status != http.StatusAccepted {
		t.Fatalf("first deploy: %d %s", status, body)
	}
	f.engine.Wait()

	status, body := f.do("POST", "/api/v1/applications/other/deploy", "name: other\nimage: nginx:1.27\nport: 80\ndomain: www.example.com\n")
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig {
		t.Fatalf("status = %d, error = %+v; to the user this is a line of deploy.yaml to change", status, e)
	}
	fields, _ := e.Details["fields"].([]any)
	field, _ := fields[0].(map[string]any)
	if len(fields) != 1 || field["field"] != "domain" || !strings.Contains(field["message"].(string), `application "web"`) {
		t.Errorf("details = %+v, want the domain field naming the owner", e.Details)
	}
	if all := decodeList(t, f); len(all) != 1 {
		t.Errorf("a refused deployment must not be recorded, got %d records", len(all))
	}
}

func TestServerReportsProxyStatus(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("GET", "/api/v1/server", "")
	if !strings.Contains(string(body), `"proxy":{"enabled":false,"reachable":false,"error":"","routes":0}`) {
		t.Errorf("server view should always carry the proxy status, even when disabled: %s", body)
	}
}
