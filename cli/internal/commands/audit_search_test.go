package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// searchAgent serves GET /audit and GET /audit/export the way an agent that
// knows the filters does, and keeps the queries.
type searchAgent struct {
	*fakeAgent
	entries []api.AuditEntry
	more    bool
	query   string
	// export is what GET /audit/export sends; failAfter makes it stop there
	// and say so in the trailer.
	export    string
	failAfter bool
	exported  string
}

func newSearchAgent(t *testing.T) *searchAgent {
	t.Helper()
	a := &searchAgent{fakeAgent: &fakeAgent{t: t}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/audit":
			a.query = r.URL.RawQuery
			json.NewEncoder(w).Encode(api.AuditPage{Data: a.entries, More: &a.more})
		case r.Method == "GET" && r.URL.Path == "/api/v1/audit/export":
			a.exported = r.URL.RawQuery
			w.Header().Set("Trailer", "X-Shipwick-Export-Error")
			w.WriteHeader(200)
			w.Write([]byte(a.export))
			if a.failAfter {
				w.Header().Set("X-Shipwick-Export-Error", "the export stopped after 1 entries: it is not complete")
			}
		default:
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint"})
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestAuditSearchesByActionOutcomeAndKindOfActor(t *testing.T) {
	a := newSearchAgent(t)
	when := time.Date(2026, 2, 28, 9, 15, 30, 0, time.UTC)
	a.entries = []api.AuditEntry{
		{ID: 9, At: when, Actor: api.Actor{Kind: api.ActorUser, Name: "ada@example.com"}, Address: "172.18.0.3", Action: "token.update", Target: "ci", Outcome: api.AuditRefused, Status: 403, Code: api.CodeForbidden},
		{ID: 8, At: when, Actor: api.Actor{Kind: api.ActorUser, Name: "ada@example.com"}, Address: "172.18.0.3", Action: "backup.create", Application: "my-api", Outcome: api.AuditFailed, Status: 409},
	}
	out, _, err := a.run(t.TempDir(), "audit", "--action", "token.,deploy", "--action", "backup.", "--outcome", "refused,failed", "--actor-kind", "person", "-n", "2")
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	if a.query != "action=token.%2Cdeploy%2Cbackup.&actor_kind=user&limit=2&outcome=refused%2Cfailed" {
		t.Errorf("query = %q", a.query)
	}
	// The page is full, and the agent says nothing older matches: no hint.
	if strings.Contains(out, "Older entries") {
		t.Errorf("a full page with nothing after it offers older entries:\n%s", out)
	}

	// The agent says there is more: the hint repeats every filter.
	a.more = true
	out, _, _ = a.run(t.TempDir(), "audit", "--action", "token.,deploy", "--action", "backup.", "--outcome", "refused,failed", "--actor-kind", "person", "-n", "5")
	if want := "Older entries: shipwick audit --actor-kind person --action token. --action deploy --action backup. --outcome refused,failed -n 5 --before 8"; !strings.HasSuffix(strings.TrimSpace(out), want) {
		t.Errorf("the hint for the next page:\n%s\nwant it to end with:\n%s", out, want)
	}

	a.entries = []api.AuditEntry{}
	if out, _, _ = a.run(t.TempDir(), "audit", "--outcome", "failed"); strings.TrimSpace(out) != "Nothing recorded that matches." {
		t.Errorf("an empty answer to a filter:\n%s", out)
	}

	for want, args := range map[string][]string{
		`--action: invalid action "Token.Create"`:                 {"audit", "--action", "Token.Create"},
		`--outcome: invalid outcome "denied"`:                     {"audit", "--outcome", "ok,denied"},
		`--actor-kind: "robot" is neither token nor person`:       {"audit", "--actor-kind", "robot"},
		`--format: "xlsx" is neither csv nor json`:                {"audit", "--format", "xlsx"},
		"--output trail.txt does not say which format":            {"audit", "--output", "trail.txt"},
		"an export is everything that matches, not a page":        {"audit", "--format", "csv", "-n", "10"},
		"an export is everything that matches, not a page: leave": {"audit", "--output", "trail.csv", "--before", "9"},
	} {
		a.query, a.exported = "", ""
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.HasPrefix(err.Error(), want) || a.query != "" || a.exported != "" {
			t.Errorf("%v: err = %v, sent %q %q; want %q and nothing sent", args, err, a.query, a.exported, want)
		}
	}
}

func TestAuditSaysWhenTheAgentWouldIgnoreAFilter(t *testing.T) {
	// An agent from before the filters answers GET /audit without "more",
	// and with everything: it does not read what it does not know.
	a := newAuditAgent(t)
	a.entries = []api.AuditEntry{{ID: 1, At: fixedNow, Actor: api.Actor{Kind: api.ActorToken, Name: "ci"}, Action: "deploy", Outcome: api.AuditOK, Status: 202}}
	out, _, err := a.run(t.TempDir(), "audit", "--action", "token.")
	want := "Error: the agent is older than this shipwick: it filters the audit trail by application, actor and time only, and would have ignored --action, --outcome and --actor-kind\n\nCompare versions with: shipwick server status"
	if got := Render(err); got != want || strings.Contains(out, "deploy") {
		t.Errorf("got:\n%s\nwant:\n%s\nprinted:\n%s", got, want, out)
	}
	// The filters it knows keep working against it.
	if out, _, err := a.run(t.TempDir(), "audit", "--actor", "ci"); err != nil || !strings.Contains(out, "deploy") {
		t.Errorf("a filter the older agent knows: %v\n%s", err, out)
	}

	_, _, err = a.run(t.TempDir(), "audit", "--format", "csv")
	want = "Error: the agent is older than this shipwick and cannot export its audit trail\n\nCompare versions with: shipwick server status"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAuditExportWritesEverythingThatMatchesToStandardOutputOrAFile(t *testing.T) {
	a := newSearchAgent(t)
	a.export = "id,at,actor_kind,actor\n2,2026-02-28T09:15:30Z,token,ci\n1,2026-02-28T09:15:30Z,token,'=1+1\n"

	// To standard output: the export and nothing else, so that it can be piped.
	out, _, err := a.run(t.TempDir(), "audit", "--format", "csv", "--app", "my-api", "--action", "deploy", "--since", "7d")
	if err != nil || out != a.export {
		t.Fatalf("audit --format csv: %v\n%q", err, out)
	}
	if a.exported != "action=deploy&application=my-api&format=csv&since=2026-02-22T12%3A00%3A00Z" {
		t.Errorf("query = %q", a.exported)
	}

	// To a file: the format from its name, and for its owner alone.
	dir := t.TempDir()
	out, _, err = a.run(dir, "audit", "--output", "trail.ndjson", "--outcome", "refused")
	if err != nil || a.exported != "format=json&outcome=refused" {
		t.Fatalf("audit --output: %v, query %q\n%s", err, a.exported, out)
	}
	written, err := os.ReadFile(filepath.Join(dir, "trail.ndjson"))
	if err != nil || string(written) != a.export {
		t.Errorf("the file holds %q, %v", written, err)
	}
	assertInOrder(t, out, []string{"Wrote the audit trail to trail.ndjson", "recorded in the trail"})
	if strings.Contains(out, "=1+1") {
		t.Errorf("the export went to the terminal as well:\n%s", out)
	}

	// A file that is there is not written over.
	_, _, err = a.run(dir, "audit", "--output", "trail.ndjson")
	if err == nil || err.Error() != "trail.ndjson exists already; choose another name, or remove it first" {
		t.Errorf("writing over a file: %v", err)
	}

	// An export that stopped half way leaves no file that looks like one.
	a.failAfter = true
	_, _, err = a.run(dir, "audit", "--format", "csv", "--output", "half.csv")
	if err == nil || !strings.Contains(err.Error(), "interrupted by the server: the export stopped after 1 entries") {
		t.Errorf("an interrupted export: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "half.csv")); !os.IsNotExist(statErr) {
		t.Errorf("half an export was kept: %v", statErr)
	}
}
