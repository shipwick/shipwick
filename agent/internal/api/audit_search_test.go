package api

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// seedTrail writes a trail with tokens and a person in it, actions of three
// families and all three outcomes:
//
//	1 root   token.create           ok
//	2 root   token.create           ok
//	3 ci     start    my-api        failed (there is no such application)
//	4 viewer stop     my-api        refused
//	5 root   secret.set             failed (the body is not JSON)
//	6 ada    signin                 ok
//	7 root   token.revoke viewer    ok
func seedTrail(t *testing.T) *fixture {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	ci := f.tokenFor(`{"name": "ci", "role": "deploy"}`)
	viewer := f.tokenFor(`{"name": "viewer", "role": "read"}`)
	f.doWithAuth("POST", "/api/v1/applications/my-api/start", "", ci)
	f.doWithAuth("POST", "/api/v1/applications/my-api/stop", "", viewer)
	f.do("PUT", "/api/v1/secrets/DATABASE_URL", "not json")
	if _, err := f.store.AddAuditEntry(t.Context(), api.AuditEntry{At: clock, Actor: api.Actor{Kind: api.ActorUser, Name: "ada@example.com"},
		Address: "127.0.0.1", Action: "signin", Outcome: api.AuditOK, Status: 200, Detail: "role read"}); err != nil {
		t.Fatal(err)
	}
	f.do("DELETE", "/api/v1/tokens/viewer", "")
	return f
}

func ids(entries []api.AuditEntry) string {
	var out []string
	for _, e := range entries {
		out = append(out, strconv.FormatInt(e.ID, 10))
	}
	return strings.Join(out, " ")
}

func TestTheAuditTrailIsSearchedByActionOutcomeAndKindOfActor(t *testing.T) {
	f := seedTrail(t)
	for query, want := range map[string]string{
		"":                                       "7 6 5 4 3 2 1",
		"?action=token.create":                   "2 1",
		"?action=token.":                         "7 2 1",
		"?action=token.,start":                   "7 3 2 1",
		"?action=token.&action=stop":             "7 4 2 1",
		"?action=token":                          "",
		"?outcome=refused":                       "4",
		"?outcome=refused,failed":                "5 4 3",
		"?outcome=refused&outcome=failed":        "5 4 3",
		"?actor_kind=user":                       "6",
		"?actor_kind=token&outcome=ok":           "7 2 1",
		"?actor_kind=token&action=signin":        "",
		"?action=token.&actor=root&limit=2":      "7 2",
		"?application=my-api&outcome=failed":     "3",
		"?action=stop,start&since=2026-10-01":    "4 3",
		"?action=%20token.%20,%20,start&limit=9": "7 3 2 1",
	} {
		if got := ids(f.audit(query)); got != want {
			t.Errorf("GET /audit%s = %q, want %q", query, got, want)
		}
	}
	var twentyOne []string
	for c := 'a'; c < 'a'+21; c++ {
		twentyOne = append(twentyOne, string(c)+".")
	}
	for query, want := range map[string]string{
		"?action=Token.Create": `invalid action "Token.Create"`,
		"?action=token.*":      "invalid action",
		"?action=a.b.c":        "invalid action",
		"?action=%25":          "invalid action",
		"?action=" + strings.Join(twentyOne, ","): "at most 20 at once",
		"?outcome=denied":                         `invalid outcome "denied": use ok, refused or failed`,
		"?actor_kind=person":                      `actor_kind: invalid kind of actor "person": use token or user`,
		"?outcome=ok,successful":                  "invalid outcome",
	} {
		status, body := f.do("GET", "/api/v1/audit"+query, "")
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, want) {
			t.Errorf("GET /audit%s: status = %d, error = %+v; want %q", query, status, e, want)
		}
	}
}

func TestAPageOfTheTrailSaysWhetherThereIsMore(t *testing.T) {
	f := seedTrail(t)
	for query, want := range map[string]bool{
		"?limit=3":                 true,
		"?limit=6":                 true,
		"?limit=7":                 false, // exactly full, and nothing older
		"?limit=8":                 false,
		"?limit=3&before=4":        false, // exactly full again
		"?limit=2&before=4":        true,
		"?action=token.&limit=3":   false,
		"?action=token.&limit=2":   true,
		"?actor=nobody":            false,
		"?outcome=refused&limit=1": false,
	} {
		status, body := f.do("GET", "/api/v1/audit"+query, "")
		var page api.AuditPage
		if err := json.Unmarshal(body, &page); err != nil || status != http.StatusOK || page.More == nil || *page.More != want {
			t.Errorf("GET /audit%s: status = %d, more = %v, want %v (body %s)", query, status, page.More, want, body)
		}
		if page.Data == nil {
			t.Errorf("GET /audit%s: data must be a list, got %s", query, body)
		}
	}
}

// export fetches GET /audit/export and returns the response with its body
// read, so that the trailer is there.
func (f *fixture) export(query, auth string) (*http.Response, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/audit/export"+query, nil)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		f.t.Fatal(err)
	}
	return resp, body.Bytes()
}

func TestTheTrailIsExportedWholeAsCSVAndAsJSONLines(t *testing.T) {
	f := seedTrail(t)

	newest := f.audit("?limit=1")[0]
	resp, body := f.export("?format=csv", "Bearer "+testToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/csv; charset=utf-8" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), `attachment; filename="shipwick-audit-2026`) || resp.Trailer.Get(exportErrorTrailer) != "" {
		t.Fatalf("status = %d, headers = %v, trailer = %v, body = %s", resp.StatusCode, resp.Header, resp.Trailer, body)
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 8 {
		t.Fatalf("csv: %d rows, %v\n%s", len(rows), err, body)
	}
	if !reflect.DeepEqual(rows[0], api.AuditColumns) {
		t.Errorf("header = %q", rows[0])
	}
	if want := []string{"7", newest.At.Format(time.RFC3339), "token", "root", "127.0.0.1", "", "token.revoke", "", "viewer", "ok", "204", "", ""}; !reflect.DeepEqual(rows[1], want) {
		t.Errorf("first row = %q, want %q", rows[1], want)
	}
	if want := []string{"4", "token", "viewer", "stop", "my-api", "refused", "403", "FORBIDDEN"}; !reflect.DeepEqual([]string{rows[4][0], rows[4][2], rows[4][3], rows[4][6], rows[4][7], rows[4][9], rows[4][10], rows[4][11]}, want) {
		t.Errorf("the refused request = %q", rows[4])
	}

	resp, body = f.export("?format=json&action=token.&outcome=ok", "Bearer "+testToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("json: status = %d, headers = %v", resp.StatusCode, resp.Header)
	}
	var entries []api.AuditEntry
	lines := bufio.NewScanner(bytes.NewReader(body))
	for lines.Scan() {
		var e api.AuditEntry
		if err := json.Unmarshal(lines.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", lines.Text(), err)
		}
		entries = append(entries, e)
	}
	// The first export is in the trail by now, and is not a token action.
	if got := ids(entries); got != "7 2 1" {
		t.Errorf("json export of token actions = %q", got)
	}
	if !reflect.DeepEqual(entries, f.audit("?action=token.&outcome=ok")) {
		t.Errorf("a line of the export is not the entry GET /audit returns")
	}

	// Nothing matches: a header and no rows, no lines at all.
	if _, body := f.export("?format=csv&actor=nobody", "Bearer "+testToken); string(body) != strings.Join(api.AuditColumns, ",")+"\n" {
		t.Errorf("an empty csv export = %q", body)
	}
	if resp, body := f.export("?format=json&actor=nobody", "Bearer "+testToken); resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Errorf("an empty json export: status = %d, body = %q", resp.StatusCode, body)
	}

	// Each export is itself in the trail: who, which format, narrowed how,
	// how many entries.
	var got []string
	for _, e := range f.audit("?action=audit.export") {
		got = append(got, e.Actor.Name+" "+e.Outcome+": "+e.Detail)
	}
	want := []string{
		"root ok: json, actor nobody, 0 entries",
		"root ok: csv, actor nobody, 0 entries",
		"root ok: json, action token., outcome ok, 3 entries",
		"root ok: csv, 7 entries",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("exports in the trail = %q, want %q", got, want)
	}
}

func TestAnExportIsAdminsAndTakesNoPage(t *testing.T) {
	f := seedTrail(t)
	ci := f.tokenFor(`{"name": "ci2", "role": "deploy"}`)
	resp, body := f.export("?format=csv", ci)
	if e := decodeError(t, body); resp.StatusCode != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "admin" {
		t.Errorf("a deploy token exporting: status = %d, error = %+v", resp.StatusCode, e)
	}
	if entries := f.audit("?action=audit.export"); len(entries) != 1 || entries[0].Actor.Name != "ci2" || entries[0].Outcome != api.AuditRefused {
		t.Errorf("the refused export in the trail: %+v", entries)
	}
	for query, want := range map[string]string{
		"":                         "format is required: csv, or json",
		"?format=xlsx":             "format is required",
		"?format=csv&limit=10":     "an export is everything that matches, not a page",
		"?format=csv&before=3":     "an export is everything that matches, not a page",
		"?format=csv&action=Nope!": "invalid action",
		"?format=json&since=never": "since: invalid time",
	} {
		resp, body := f.export(query, "Bearer "+testToken)
		if e := decodeError(t, body); resp.StatusCode != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, want) {
			t.Errorf("GET /audit/export%s: status = %d, error = %+v; want %q", query, resp.StatusCode, e, want)
		}
	}
}

func TestAnExportLongerThanAPageArrivesWholeAndInOrder(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const total = 1203
	for i := range total {
		if _, err := f.store.AddAuditEntry(t.Context(), api.AuditEntry{At: at.Add(time.Duration(i) * time.Second), Actor: api.Actor{Kind: api.ActorToken, Name: "ci"},
			Address: "127.0.0.1", Action: "deploy", Application: "my-api", Outcome: api.AuditOK, Status: 202}); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := f.export("?format=csv&application=my-api", "Bearer "+testToken)
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || resp.StatusCode != http.StatusOK || len(rows) != total+1 || resp.Trailer.Get(exportErrorTrailer) != "" {
		t.Fatalf("status = %d, %d rows, %v, trailer %q", resp.StatusCode, len(rows), err, resp.Trailer.Get(exportErrorTrailer))
	}
	for i, row := range rows[1:] {
		if row[0] != strconv.Itoa(total-i) {
			t.Fatalf("row %d has id %s, want %d", i, row[0], total-i)
		}
	}
	if entries := f.audit("?action=audit.export"); len(entries) != 1 || entries[0].Detail != "csv, application my-api, 1203 entries" {
		t.Errorf("the export in the trail: %+v", entries)
	}
}

// The trail keeps names that callers chose: a path segment that is refused
// is recorded as the target it named. Opened in a spreadsheet, a cell that
// begins with =, +, - or @ is a formula.
func TestNoCellOfACSVExportIsAFormula(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/api/v1/tokens/=HYPERLINK(%22http:%2F%2Fevil.example%22)",
		"/api/v1/tokens/+1+1",
		"/api/v1/tokens/-2+3",
		"/api/v1/tokens/@SUM(A1:A9)",
		"/api/v1/secrets/=cmd%7C(%2FC)!A0",
	} {
		if status, body := f.do("DELETE", path, ""); status != http.StatusBadRequest {
			t.Fatalf("DELETE %s: status = %d, body = %s", path, status, body)
		}
	}
	// A person whose name begins with a dash, as a subject identifier may,
	// and a detail and an actor filter that would be formulas.
	if _, err := f.store.AddAuditEntry(t.Context(), api.AuditEntry{At: time.Now(), Actor: api.Actor{Kind: api.ActorUser, Name: "-AAAA_bbbb"},
		Address: "127.0.0.1", Action: "signin", Outcome: api.AuditOK, Status: 200, Detail: "=1+1", Code: "@X", Application: "+app"}); err != nil {
		t.Fatal(err)
	}

	// The targets are in the trail as they were sent: the export is where
	// they are made harmless, not the record.
	var targets []string
	for _, e := range f.audit("?outcome=failed") {
		targets = append(targets, e.Target)
	}
	if len(targets) != 5 || targets[4] != `=HYPERLINK("http://evil.example")` {
		t.Fatalf("recorded targets = %q", targets)
	}

	_, body := f.export("?format=csv", "Bearer "+testToken)
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 7 {
		t.Fatalf("csv: %d rows, %v\n%s", len(rows), err, body)
	}
	formulas := 0
	for _, row := range rows {
		for _, cell := range row {
			if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				t.Errorf("a cell that a spreadsheet reads as a formula: %q in %q", cell, row)
			}
			if strings.HasPrefix(cell, "'") {
				formulas++
			}
		}
	}
	// Five targets, and the person's name, application, code and detail.
	if formulas != 9 {
		t.Errorf("%d cells were neutralised, want 9:\n%s", formulas, body)
	}
	for _, want := range []string{`'=HYPERLINK(""http://evil.example"")`, "'+1+1", "'-2+3", "'@SUM(A1:A9)", "'-AAAA_bbbb", "'=1+1", "'@X", "'+app"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the export lacks %s:\n%s", want, body)
		}
	}

	// JSON is not read by a spreadsheet: the values are as recorded.
	_, body = f.export("?format=json&actor_kind=user", "Bearer "+testToken)
	var e api.AuditEntry
	if err := json.Unmarshal(body, &e); err != nil || e.Actor.Name != "-AAAA_bbbb" || e.Detail != "=1+1" {
		t.Errorf("json export = %s (%v)", body, err)
	}
}
