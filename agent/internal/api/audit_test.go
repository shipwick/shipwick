package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// audit returns the trail as GET /audit answers it for a query.
func (f *fixture) audit(query string) []api.AuditEntry {
	f.t.Helper()
	status, body := f.do("GET", "/api/v1/audit"+query, "")
	if status != http.StatusOK {
		f.t.Fatalf("GET /audit%s: status = %d, body = %s", query, status, body)
	}
	return decode[[]api.AuditEntry](f.t, body)
}

func TestEveryRouteThatChangesSomethingIsAuditedOrSaysWhyNot(t *testing.T) {
	f := newFixture(t)
	registered := map[string]bool{}
	for _, rt := range f.api.routes {
		registered[rt.pattern()] = true
		_, exempt := unauditedRoutes[rt.pattern()]
		switch {
		case rt.audit != nil && exempt:
			t.Errorf("%s is both audited and exempt", rt.pattern())
		case rt.method != http.MethodGet && rt.audit == nil && !exempt:
			t.Errorf("%s changes something and is not in auditedRoutes; if it changes nothing, say why in unauditedRoutes", rt.pattern())
		case rt.audit != nil && rt.audit.action == "":
			t.Errorf("%s is audited without an action", rt.pattern())
		}
	}
	// An entry for a route that is gone, or spelled differently from its
	// registration, records nothing.
	for pattern := range auditedRoutes {
		if !registered[pattern] {
			t.Errorf("auditedRoutes names %s, which is not a registered route", pattern)
		}
	}
	for pattern := range unauditedRoutes {
		if !registered[pattern] {
			t.Errorf("unauditedRoutes names %s, which is not a registered route", pattern)
		}
	}
	if len(registered) < 60 {
		t.Errorf("only %d routes are registered through the route table", len(registered))
	}
}

func TestTheAuditTrailSaysWhoDidWhatAndHowItEnded(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	ci := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api"], "expires_at": "2027-01-01T00:00:00Z"}`)

	clock = clock.Add(time.Minute)
	f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, ci)
	f.engine.Wait()
	f.doWithAuth("POST", "/api/v1/applications/web/deploy", otherConfig, ci)
	f.doWithAuth("DELETE", "/api/v1/applications/my-api", "", ci)
	f.doWithAuth("POST", "/api/v1/applications/my-api/rollback", "", ci)
	f.doWithAuth("GET", "/api/v1/applications/my-api", "", ci)
	f.do("PUT", "/api/v1/secrets/DATABASE_URL", `{"value": "postgres://app:hunter2@db/app"}`)
	f.do("DELETE", "/api/v1/tokens/ci", "")

	want := []api.AuditEntry{
		{Action: "token.revoke", Actor: api.Actor{Kind: "token", Name: "root"}, Target: "ci", Outcome: api.AuditOK, Status: 204},
		{Action: "secret.set", Actor: api.Actor{Kind: "token", Name: "root"}, Target: "DATABASE_URL", Outcome: api.AuditOK, Status: 204},
		{Action: "rollback", Actor: api.Actor{Kind: "token", Name: "ci"}, Application: "my-api", Outcome: api.AuditFailed, Status: 409, Code: api.CodeNoRollbackTarget},
		{Action: "application.delete", Actor: api.Actor{Kind: "token", Name: "ci"}, Application: "my-api", Outcome: api.AuditRefused, Status: 403, Code: api.CodeForbidden},
		{Action: "deploy", Actor: api.Actor{Kind: "token", Name: "ci"}, Application: "web", Outcome: api.AuditRefused, Status: 403, Code: api.CodeTokenLimited},
		{Action: "deploy", Actor: api.Actor{Kind: "token", Name: "ci"}, Application: "my-api", Outcome: api.AuditOK, Status: 202, Detail: "deployment 1"},
		{Action: "token.create", Actor: api.Actor{Kind: "token", Name: "root"}, Target: "ci", Outcome: api.AuditOK, Status: 201,
			Detail: "role deploy, limited to my-api, expires 2027-01-01T00:00:00Z"},
	}
	got := f.audit("")
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d (a GET is not recorded): %+v", len(got), len(want), got)
	}
	for i, e := range got {
		if e.ID != int64(len(want)-i) || e.Address != "127.0.0.1" || e.ForwardedFor != "" {
			t.Errorf("entry %d: id = %d, address = %q, forwarded_for = %q", i, e.ID, e.Address, e.ForwardedFor)
		}
		if at := clock; i == len(want)-1 {
			at = at.Add(-time.Minute)
			if !e.At.Equal(at) {
				t.Errorf("entry %d: at = %s, want %s", i, e.At, at)
			}
		}
		e.ID, e.At, e.Address = 0, time.Time{}, ""
		if e != want[i] {
			t.Errorf("entry %d:\n got %+v\nwant %+v", i, e, want[i])
		}
	}
}

func TestTheAuditTrailIsFilteredAndPaged(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	ci := f.tokenFor(`{"name": "ci", "role": "deploy"}`)
	for range 3 {
		clock = clock.Add(time.Hour)
		f.doWithAuth("POST", "/api/v1/applications/my-api/start", "", ci)
		f.do("POST", "/api/v1/applications/web/start", "")
	}

	actions := func(entries []api.AuditEntry) string {
		var out []string
		for _, e := range entries {
			out = append(out, strconv.FormatInt(e.ID, 10)+":"+e.Actor.Name+":"+e.Application)
		}
		return strings.Join(out, " ")
	}
	for query, want := range map[string]string{
		"?application=web":                  "7:root:web 5:root:web 3:root:web",
		"?actor=ci":                         "6:ci:my-api 4:ci:my-api 2:ci:my-api",
		"?actor=ci&application=web":         "",
		"?since=2026-10-01T15:00:00Z":       "7:root:web 6:ci:my-api",
		"?since=90m":                        "",
		"?since=1h":                         "7:root:web 6:ci:my-api 5:root:web 4:ci:my-api",
		"?limit=2":                          "7:root:web 6:ci:my-api",
		"?limit=2&before=6":                 "5:root:web 4:ci:my-api",
		"?limit=5&before=2":                 "1:root:",
		"?actor=nobody":                     "",
		"?application=web&since=2026-10-01": "7:root:web 5:root:web 3:root:web",
		"?application=web&since=2026-10-02": "",
		"?application=web&limit=1&before=5": "3:root:web",
	} {
		if query == "?since=90m" {
			// Minutes are not a unit the API takes.
			status, body := f.do("GET", "/api/v1/audit"+query, "")
			if e := decodeError(t, body); status != http.StatusBadRequest || !strings.HasPrefix(e.Message, `since: invalid time "90m"`) {
				t.Errorf("%s: status = %d, error = %+v", query, status, e)
			}
			continue
		}
		if got := actions(f.audit(query)); got != want {
			t.Errorf("GET /audit%s = %q, want %q", query, got, want)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=501", "?before=x", "?before=0", "?application=Not_A_Name", "?since=yesterday"} {
		status, body := f.do("GET", "/api/v1/audit"+query, "")
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("GET /audit%s: status = %d, error = %+v", query, status, e)
		}
	}
	if _, body := f.do("GET", "/api/v1/audit?actor=nobody", ""); !strings.Contains(string(body), `"data":[]`) {
		t.Errorf("an empty trail is [], got %s", body)
	}

	status, body := f.doWithAuth("GET", "/api/v1/audit", "", ci)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Details["required"] != "admin" {
		t.Errorf("a deploy token reading the trail: status = %d, error = %+v", status, e)
	}
}

func TestNothingOfARequestBodyReachesTheAuditTrail(t *testing.T) {
	f := newFixture(t)
	const marker = "s3cr3t-marker-in-the-body"
	sent := 0
	for _, rt := range f.api.routes {
		if rt.audit == nil || rt.path == "/api/v1/tokens/{name}" {
			continue
		}
		for _, body := range []string{marker, `{"value": "` + marker + `", "password": "` + marker + `", "passphrase": "` + marker + `", "key": "` + marker + `", "command": ["` + marker + `"], "name": "` + marker + `"}`} {
			req, _ := http.NewRequest(rt.method, f.srv.URL+pathFor(rt, "my-api"), strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set(api.PassphraseHeader, marker)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", rt.pattern(), err)
			}
			resp.Body.Close()
			sent++
		}
	}
	entries, err := f.store.AuditEntries(context.Background(), store.AuditFilter{Limit: 1000})
	if err != nil || len(entries) != sent {
		t.Fatalf("%d entries for %d requests, err = %v", len(entries), sent, err)
	}
	named := 0
	for _, e := range entries {
		// The one thing of a body that is kept: the name of the application a
		// document is about, where the address has none, and only when it is
		// a name.
		if e.Application == marker && e.Action == "deploy" {
			named++
			e.Application = ""
		}
		if strings.Contains(e.Target+e.Detail+e.Code+e.Application+e.Action+e.Actor.Name, marker) {
			t.Errorf("the entry repeats the request body: %+v", e)
		}
	}
	if named != 1 {
		t.Errorf("%d entries name the application of a document, want the one for POST /applications", named)
	}
}

func TestAnAuditEntryKeepsTheAddressTheProxyReported(t *testing.T) {
	f := newFixture(t)
	h := f.api.Handler()
	send := func(forwarded ...string) {
		req := httptest.NewRequest("POST", "/api/v1/applications/my-api/start", nil)
		req.RemoteAddr = "172.18.0.3:51234"
		req.Header.Set("Authorization", "Bearer "+testToken)
		for _, v := range forwarded {
			req.Header.Add("X-Forwarded-For", v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	send()
	send("203.0.113.9")
	send("10.0.0.1, 2001:db8::1")
	send("<script>alert(1)</script>")
	send("198.51.100.1", "203.0.113.77")

	var got []string
	for _, e := range f.audit("") {
		if e.Address != "172.18.0.3" {
			t.Errorf("address = %q, want the connection's", e.Address)
		}
		got = append(got, e.ForwardedFor)
	}
	if want := "203.0.113.77||2001:db8::1|203.0.113.9|"; strings.Join(got, "|") != want {
		t.Errorf("forwarded_for, newest first = %q, want %q: the nearest proxy's word, and only an address", strings.Join(got, "|"), want)
	}
}

func TestAHandlerThatPanicsIsAuditedAsFailed(t *testing.T) {
	f := newFixture(t)
	rt := newRoute("POST /api/v1/applications/{name}/stop", api.RoleDeploy)
	h := f.api.recoverPanics(f.api.authenticate(rt, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })))
	req := httptest.NewRequest("POST", "/api/v1/applications/my-api/stop", nil)
	req.SetPathValue("name", "my-api")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want the panic answered 500 as before", rec.Code)
	}
	entries := f.audit("")
	if len(entries) != 1 || entries[0].Action != "stop" || entries[0].Outcome != api.AuditFailed || entries[0].Status != 500 || entries[0].Code != api.CodeInternal {
		t.Errorf("entries = %+v", entries)
	}
}

func TestAnAuditedDownloadIsRecordedThoughItIsAGet(t *testing.T) {
	f := newFixture(t)
	f.do("GET", "/api/v1/applications/my-api/volumes/data/archive", "")
	f.do("GET", "/api/v1/applications/my-api/backups/7/volumes/data/archive", "")
	entries := f.audit("")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if e := entries[0]; e.Action != "backup.download" || e.Target != "7" || e.Detail != "volume data" || e.Application != "my-api" {
		t.Errorf("backup download = %+v", e)
	}
	if e := entries[1]; e.Action != "volume.download" || e.Target != "data" || e.Outcome != api.AuditFailed {
		t.Errorf("volume download = %+v", e)
	}
}
