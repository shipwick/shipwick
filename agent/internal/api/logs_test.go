package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// newLogFixture is newFixture with a log archive.
func newLogFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:", store.Options{EncryptionKey: []byte("an-encryption-key-of-32-bytes!!!")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet,
		LogArchive: deploy.LogArchiveOptions{Dir: t.TempDir(), MaxAge: 14 * 24 * time.Hour, MaxBytes: 1 << 30}})

	logs := &bytes.Buffer{}
	apiServer := New(engine, st, sha256.Sum256([]byte(testToken)), slog.New(slog.NewTextHandler(logs, nil)))
	srv := httptest.NewServer(apiServer.Handler())
	t.Cleanup(func() {
		apiServer.Close()
		srv.Close()
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &fixture{t: t, srv: srv, engine: engine, rt: rt, api: apiServer, logs: logs, store: st}
}

// deployAndReplace deploys my-api, lets its two replicas print, and replaces
// them: two entries in the archive, two replicas running.
func deployAndReplace(t *testing.T, f *fixture) {
	t.Helper()
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	for _, c := range f.rt.Containers() {
		f.rt.WriteLogs(c.ID, "listening", "Token=s3cr3t-in-a-log rejected", "shutting down")
	}
	if status, body := f.do("POST", "/api/v1/applications/my-api/redeploy", ""); status != http.StatusAccepted {
		t.Fatalf("redeploy: %d %s", status, body)
	}
	f.engine.Wait()
	for _, c := range f.rt.Containers() {
		f.rt.WriteLogs(c.ID, "listening again")
	}
}

func TestLogArchiveEndpoints(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)

	status, body := f.do("GET", "/api/v1/applications/my-api/logs/archive", "")
	if status != http.StatusOK {
		t.Fatalf("archive: %d %s", status, body)
	}
	entries := decode[[]api.LogArchiveEntry](t, body)
	if len(entries) != 2 || entries[0].ID != 2 || entries[0].Replica != 2 || entries[1].Replica != 1 {
		t.Fatalf("entries, newest first: %+v", entries)
	}
	e := entries[1]
	if e.Application != "my-api" || e.Kind != api.LogKindReplica || e.Reason != api.LogReasonReplaced || e.Deployment != 1 || e.Version != "1.4.2" ||
		e.Container != "shipwick_my-api_1_1" || e.Lines != 3 || e.Truncated {
		t.Errorf("entry: %+v", e)
	}
	for _, field := range []string{`"deployment_id":1`, `"run_id":null`, `"job":""`, `"exit_code":0`, `"oom_killed":false`, `"ended_at":"`, `"first_line_at":"`, `"last_line_at":"`, `"stored_bytes":`, `"truncated":false`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("wire format lacks %s: %s", field, body)
		}
	}
	if strings.Contains(string(body), "s3cr3t") || strings.Contains(string(body), `"output"`) {
		t.Error("the listing must not carry output")
	}

	status, body = f.do("GET", "/api/v1/applications/my-api/logs/archive?replica=1&limit=1", "")
	if narrowed := decode[[]api.LogArchiveEntry](t, body); status != http.StatusOK || len(narrowed) != 1 || narrowed[0].ID != 1 {
		t.Errorf("replica=1: %d %s", status, body)
	}
	status, body = f.do("GET", "/api/v1/applications/my-api/logs/archive?before=2", "")
	if page := decode[[]api.LogArchiveEntry](t, body); status != http.StatusOK || len(page) != 1 || page[0].ID != 1 {
		t.Errorf("before=2: %d %s", status, body)
	}
	status, body = f.do("GET", "/api/v1/applications/my-api/logs/archive?kind=run", "")
	if string(bytes.TrimSpace(body)) != `{"data":[]}` {
		t.Errorf("kind=run: %d %s, want an empty list", status, body)
	}

	status, body = f.do("GET", "/api/v1/applications/my-api/logs/archive/1", "")
	detail := decode[api.LogArchiveDetail](t, body)
	if status != http.StatusOK || detail.ID != 1 || len(detail.Output) != 3 || detail.Output[0].Message != "listening" ||
		detail.Output[0].Replica != 1 || detail.Output[0].Container != "shipwick_my-api_1_1" || detail.Output[0].Stream != "stdout" || detail.Output[0].Time.IsZero() {
		t.Fatalf("detail: %d %s", status, body)
	}
	status, body = f.do("GET", "/api/v1/applications/my-api/logs/archive/1?tail=1", "")
	if tail := decode[api.LogArchiveDetail](t, body); status != http.StatusOK || len(tail.Output) != 1 || tail.Output[0].Message != "shutting down" || tail.Lines != 3 {
		t.Errorf("tail=1: %d %s", status, body)
	}
}

func TestLogSearchEndpoint(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)

	status, body := f.do("GET", "/api/v1/applications/my-api/logs/search?q=LISTENING", "")
	if status != http.StatusOK {
		t.Fatalf("search: %d %s", status, body)
	}
	result := decode[api.LogSearchResult](t, body)
	if len(result.Lines) != 4 || result.Next != "" || result.Sources != 4 || result.Bytes == 0 {
		t.Fatalf("result: %s", body)
	}
	if live := result.Lines[0]; live.Message != "listening again" || live.ArchiveID != nil || live.Deployment != 2 || live.Replica != 2 {
		t.Errorf("first line, of a running replica: %+v", live)
	}
	if kept := result.Lines[3]; kept.Message != "listening" || kept.ArchiveID == nil || *kept.ArchiveID != 1 || kept.Deployment != 1 || kept.Replica != 1 {
		t.Errorf("last line, of the archive: %+v", kept)
	}
	for _, field := range []string{`"archive_id":null`, `"archive_id":1`, `"deployment_id":2`, `"run_id":null`, `"job":""`, `"stream":"stdout"`, `"container":"shipwick_my-api_2_1"`, `"next":""`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("wire format lacks %s: %s", field, body)
		}
	}

	// Paged: every line once, in the same order.
	var paged []string
	cursor := ""
	for pages := 0; ; pages++ {
		_, body = f.do("GET", "/api/v1/applications/my-api/logs/search?limit=3&cursor="+url.QueryEscape(cursor), "")
		page := decode[api.LogSearchResult](t, body)
		for _, line := range page.Lines {
			paged = append(paged, line.Container+" "+line.Message)
		}
		if cursor = page.Next; cursor == "" {
			break
		}
		if len(page.Lines) != 3 || pages > 10 {
			t.Fatalf("page %d: %s", pages, body)
		}
	}
	if len(paged) != 8 || paged[0] != "shipwick_my-api_2_2 listening again" || paged[7] != "shipwick_my-api_1_1 listening" {
		t.Errorf("paged lines: %q", paged)
	}

	at := result.Lines[3].Time
	query := "?q=listening&deployment=1&replica=1&since=" + url.QueryEscape(at.Format(time.RFC3339Nano)) + "&until=" + url.QueryEscape(at.Format(time.RFC3339Nano))
	_, body = f.do("GET", "/api/v1/applications/my-api/logs/search"+query, "")
	if narrowed := decode[api.LogSearchResult](t, body); len(narrowed.Lines) != 1 || !narrowed.Lines[0].Time.Equal(at) {
		t.Errorf("narrowed to one line by deployment, replica and time: %s", body)
	}
}

func TestLogArchiveEndpointErrors(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)
	f.do("POST", "/api/v1/applications/other/deploy", strings.Replace(validConfig, "my-api", "other", 1))
	f.engine.Wait()

	long := strings.Repeat("x", maxSearchText+1)
	for _, c := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/applications/ghost/logs/archive", 404, api.CodeNotFound},
		{"/api/v1/applications/ghost/logs/archive/1", 404, api.CodeNotFound},
		{"/api/v1/applications/ghost/logs/search?q=x", 404, api.CodeNotFound},
		{"/api/v1/applications/Bad_Name/logs/archive", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive/999", 404, api.CodeNotFound},
		{"/api/v1/applications/other/logs/archive/1", 404, api.CodeNotFound}, // another application's entry
		{"/api/v1/applications/my-api/logs/archive/abc", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive/1?tail=0", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive?kind=container", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive?limit=501", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive?deployment=x", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive?replica=0", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/archive?before=-1", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?q=" + long, 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?q=a%0Ab", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?since=yesterday", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?since=2026-10-02T00:00:00Z&until=2026-10-01T00:00:00Z", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?limit=1001", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?cursor=zzz", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?cursor=" + strings.Repeat("1", maxSearchCursor+1), 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?run=0", 400, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/logs/search?deployment=" + strconv.Itoa(3), 404, api.CodeNotFound}, // other's deployment
	} {
		status, body := f.do("GET", c.path, "")
		if e := decodeError(t, body); status != c.status || e.Code != c.code {
			t.Errorf("GET %s: %d %s (%s), want %d %s", c.path, status, e.Code, e.Message, c.status, c.code)
		}
	}
}

func TestArchivedOutputIsReadWithTheRoleThatReadsLogsAndIsNeverLoggedOrRecorded(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)

	for _, path := range []string{"/logs/archive", "/logs/archive/1", "/logs/search?q=token"} {
		if status, _ := f.doWithAuth("GET", "/api/v1/applications/my-api"+path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: %d", path, status)
		}
	}
	status, body := f.do("POST", "/api/v1/tokens", `{"name": "viewer", "role": "read"}`)
	if status != http.StatusCreated {
		t.Fatalf("create token: %d %s", status, body)
	}
	viewer := "Bearer " + decode[api.CreatedToken](t, body).Token
	status, body = f.doWithAuth("GET", "/api/v1/applications/my-api/logs/search?q=token", "", viewer)
	if result := decode[api.LogSearchResult](t, body); status != http.StatusOK || len(result.Lines) != 2 || !strings.Contains(result.Lines[0].Message, "s3cr3t") {
		t.Fatalf("a read token searches the archive: %d %s", status, body)
	}

	// What was found stays in the answer: not in the agent's log, not in
	// the application's events, not in the audit trail.
	if strings.Contains(f.logs.String(), "s3cr3t") || strings.Contains(f.logs.String(), "q=") {
		t.Errorf("the agent's log carries output or the question:\n%s", f.logs.String())
	}
	_, body = f.do("GET", "/api/v1/applications/my-api/events?limit=100", "")
	if strings.Contains(string(body), "s3cr3t") {
		t.Errorf("events carry archived output: %s", body)
	}
	_, body = f.do("GET", "/api/v1/audit", "")
	if strings.Contains(string(body), "s3cr3t") || strings.Contains(string(body), "logs/") {
		t.Errorf("the audit trail carries the archive: %s", body)
	}
}

func TestDeletedApplicationHasNoArchive(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)
	if status, body := f.do("DELETE", "/api/v1/applications/my-api", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, body)
	}
	if status, _ := f.do("GET", "/api/v1/applications/my-api/logs/archive", ""); status != http.StatusNotFound {
		t.Errorf("archive of a deleted application: %d", status)
	}
	// A new application of the same name starts with an empty archive.
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	_, body := f.do("GET", "/api/v1/applications/my-api/logs/archive", "")
	if entries := decode[[]api.LogArchiveEntry](t, body); len(entries) != 0 {
		t.Errorf("the new application inherited entries: %s", body)
	}
	_, body = f.do("GET", "/api/v1/applications/my-api/logs/search?q=listening", "")
	if result := decode[api.LogSearchResult](t, body); len(result.Lines) != 0 {
		t.Errorf("the new application finds the old one's lines: %s", body)
	}
}

func TestServerReportsTheLogArchive(t *testing.T) {
	f := newLogFixture(t)
	deployAndReplace(t, f)
	_, body := f.do("GET", "/api/v1/server", "")
	server := decode[api.Server](t, body)
	if a := server.LogArchive; a == nil || !a.Enabled || a.Entries != 2 || a.Bytes == 0 || a.MaxBytes != 1<<30 || a.RetentionDays != 14 {
		t.Errorf("log_archive: %+v in %s", a, body)
	}
	if !strings.Contains(string(body), `"log_archive":{"enabled":true,"entries":2,"bytes":`) {
		t.Errorf("wire format: %s", body)
	}
}
