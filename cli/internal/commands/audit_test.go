package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestTokenCreateSendsTheLimitAndTheExpiry(t *testing.T) {
	a := newTokenAgent(t)
	out, _, err := a.run(t.TempDir(), "token", "create", "ci", "--role", "deploy", "--app", "web", "--app", "my-api", "--expires", "90d")
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	// fixedNow is 2026-03-01 12:00 UTC; ninety days later is 2026-05-30.
	if a.createBody != `{"name":"ci","role":"deploy","applications":["my-api","web"],"expires_at":"2026-05-30T12:00:00Z"}` {
		t.Errorf("request body: %s", a.createBody)
	}
	expires := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	assertInOrder(t, out, []string{
		"Created token ci with the deploy role, limited to my-api, web",
		"It expires on " + expires.Local().Format("2006-01-02 at 15:04") + ", in 90 days.",
		fakeTokenValue,
		"will not be shown again",
	})

	a.run(t.TempDir(), "token", "create", "auditor", "--role", "read", "--expires", "2026-03-31")
	if a.createBody != `{"name":"auditor","role":"read","expires_at":"2026-04-01T00:00:00Z"}` {
		t.Errorf("a date lasts through that day: %s", a.createBody)
	}

	for want, args := range map[string][]string{
		"--app: only a deploy token can be limited to applications": {"token", "create", "t", "--role", "read", "--app", "web"},
		"--app: only a deploy token can be limited":                 {"token", "create", "t", "--role", "admin", "--app", "web"},
		"--app: applications: ":                                     {"token", "create", "t", "--role", "deploy", "--app", "Not_Valid"},
		`--expires: invalid expiry "soon": use days or hours from now, or a date, e.g. 90d, 12h or 2026-06-01`: {"token", "create", "t", "--role", "deploy", "--expires", "soon"},
		"--expires: the expiry is in the past": {"token", "create", "t", "--role", "deploy", "--expires", "2026-02-28"},
	} {
		a.requests = nil
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", args, a.requests)
		}
	}
}

func TestTokenCreateSaysWhenTheAgentKnowsNeitherLimitsNorExpiry(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, 400, api.Error{Code: api.CodeInvalidRequest, Message: `invalid JSON body: json: unknown field "expires_at"`})
	}))
	t.Cleanup(old.Close)
	a := &fakeAgent{t: t, srv: old}

	_, _, err := a.run(t.TempDir(), "token", "create", "ci", "--role", "deploy", "--expires", "90d")
	want := "Error: the agent is older than this shipwick: it knows neither --app nor --expires, and created nothing\n\nCompare versions with: shipwick server status"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Without the new flags the agent's own words stand.
	_, _, err = a.run(t.TempDir(), "token", "create", "ci", "--role", "deploy")
	if got := Render(err); !strings.Contains(got, "unknown field") {
		t.Errorf("got: %s", got)
	}
}

func TestTokenListShowsLimitsAndMarksWhatIsAboutToExpire(t *testing.T) {
	a := newTokenAgent(t)
	at := func(d time.Duration) *time.Time { t := fixedNow.Add(d); return &t }
	a.tokens = []api.Token{
		{ID: 1, Name: "laptop", Role: api.RoleAdmin, CreatedAt: fixedNow.Add(-time.Hour), Applications: []string{}},
		{ID: 2, Name: "ci", Role: api.RoleDeploy, CreatedAt: fixedNow.Add(-time.Hour), Applications: []string{"my-api", "web"}, ExpiresAt: at(89*24*time.Hour - 5*time.Second)},
		{ID: 3, Name: "soon", Role: api.RoleDeploy, CreatedAt: fixedNow.Add(-time.Hour), ExpiresAt: at(14 * 24 * time.Hour)},
		{ID: 4, Name: "sooner", Role: api.RoleRead, CreatedAt: fixedNow.Add(-time.Hour), ExpiresAt: at(90 * time.Minute)},
		{ID: 5, Name: "gone", Role: api.RoleRead, CreatedAt: fixedNow.Add(-time.Hour), ExpiresAt: at(-3 * 24 * time.Hour)},
		{ID: 6, Name: "later", Role: api.RoleRead, CreatedAt: fixedNow.Add(-time.Hour), ExpiresAt: at(14*24*time.Hour + time.Minute)},
	}
	out, _, err := a.run(t.TempDir(), "token", "ls")
	if err != nil {
		t.Fatalf("token ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 9 {
		t.Fatalf("unexpected output:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"NAME", "ROLE", "APPLICATIONS", "EXPIRES", "CREATED", "LAST USED"})
	assertInOrder(t, lines[1], []string{"laptop", "admin", "all", "never", "1h ago", "never"})
	assertInOrder(t, lines[2], []string{"ci", "deploy", "my-api, web", "in 89 days ", "1h ago"})
	assertInOrder(t, lines[3], []string{"soon", "deploy", "all", "in 14 days (soon)"})
	assertInOrder(t, lines[4], []string{"sooner", "read", "all", "in 90 minutes (soon)"})
	assertInOrder(t, lines[5], []string{"gone", "read", "all", "expired 3d ago"})
	assertInOrder(t, lines[6], []string{"later", "read", "all", "in 14 days "})
	if lines[8] != "Expired, or expiring within 14 days: soon, sooner, gone. A token cannot be extended: create a new one, hand it over, then revoke the old one." {
		t.Errorf("closing line: %q", lines[8])
	}

	// An agent from before sends neither field, and nothing is marked.
	a.tokens = []api.Token{{ID: 1, Name: "ci", Role: api.RoleDeploy, CreatedAt: fixedNow}}
	out, _, _ = a.run(t.TempDir(), "token", "ls")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 2 || !strings.Contains(lines[1], "never") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestRenderAnExpiredAndALimitedToken(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	expired := &client.APIError{Status: 401, Code: api.CodeTokenExpired, Message: "token ci expired on 2026-10-02 at 12:00 UTC; an admin creates a new one with: shipwick token create",
		Details: map[string]any{"name": "ci", "expired_at": "2026-10-02T12:00:00Z"}}
	want := "The token ci expired on " + at.Local().Format("2006-01-02 at 15:04") + ".\n\n" +
		"An admin creates a new one with: shipwick token create\nThen set SHIPWICK_AGENT_TOKEN to it, or save it with: shipwick login"
	if got := Render(expired); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := Render(&client.APIError{Status: 401, Code: api.CodeTokenExpired, Details: map[string]any{}}); !strings.HasPrefix(got, "The API token has expired.\n\n") {
		t.Errorf("without details: %q", got)
	}

	limited := &client.APIError{Status: 403, Code: api.CodeTokenLimited, Message: "this token is limited to my-api and web; it can read worker and not change it",
		Details: map[string]any{"applications": []any{"my-api", "web"}, "application": "worker"}}
	want = "This token may not do that: it is limited to my-api, web.\n\n" +
		"It can read worker, not change it. Use a token that covers it, or create one with: shipwick token create <name> --role deploy --app worker"
	if got := Render(limited); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	delete(limited.Details, "application")
	want = "This token may not do that: it is limited to my-api, web.\n\nThis operation is not about one application. Use a deploy token without --app, or an admin token."
	if got := Render(limited); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := Render(&client.APIError{Status: 403, Code: api.CodeTokenLimited, Message: "no", Details: map[string]any{}}); got != "This token may not do that: no" {
		t.Errorf("without details: %q", got)
	}
}

func TestServerStatusAndDoctorSayWhenTheTokenExpires(t *testing.T) {
	f := newFakeAgent(t)
	expires := fixedNow.Add(40 * 24 * time.Hour)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1",
		Token: api.TokenIdentity{Kind: api.ActorToken, Name: "ci", Role: api.RoleDeploy, Applications: []string{"my-api"}, ExpiresAt: &expires}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil || !strings.Contains(out, "ci (deploy, limited to my-api, expires in 40 days)") {
		t.Errorf("server status: %v\n%s", err, out)
	}

	d := doctorAgent(t, func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	d.server.Token = f.server.Token
	out, _, _ = d.run(t.TempDir(), "doctor")
	if !strings.Contains(out, "✓ Token ci (deploy, limited to my-api, expires in 40 days)") {
		t.Errorf("doctor, forty days ahead:\n%s", out)
	}

	expires = fixedNow.Add(9 * 24 * time.Hour)
	out, _, _ = d.run(t.TempDir(), "doctor")
	want := "! Token ci (deploy, limited to my-api, expires in 9 days). From " + expires.Local().Format("2006-01-02 15:04") +
		" it is refused; create its replacement before then: shipwick token create <name> --role deploy --app my-api --expires 90d"
	if !strings.Contains(out, want) {
		t.Errorf("doctor, nine days ahead:\n%s\nwant a line:\n%s", out, want)
	}
}

// auditAgent serves GET /audit and keeps the query it was asked with.
type auditAgent struct {
	*fakeAgent
	entries []api.AuditEntry
	query   string
}

func newAuditAgent(t *testing.T) *auditAgent {
	t.Helper()
	a := &auditAgent{fakeAgent: &fakeAgent{t: t}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/audit" {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint"})
			return
		}
		a.query = r.URL.RawQuery
		respond(w, 200, a.entries)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestAuditListsWhoDidWhat(t *testing.T) {
	a := newAuditAgent(t)
	when := time.Date(2026, 2, 28, 9, 15, 30, 0, time.UTC)
	ci := api.Actor{Kind: api.ActorToken, Name: "ci"}
	a.entries = []api.AuditEntry{
		{ID: 9, At: when, Actor: ci, Address: "172.18.0.3", ForwardedFor: "203.0.113.9", Action: "deploy", Application: "my-api", Outcome: api.AuditOK, Status: 202, Detail: "deployment 12"},
		{ID: 8, At: when, Actor: ci, Address: "172.18.0.3", Action: "deploy", Application: "web", Outcome: api.AuditRefused, Status: 403, Code: api.CodeTokenLimited},
		{ID: 7, At: when, Actor: api.Actor{Kind: api.ActorToken, Name: "root"}, Address: "127.0.0.1", Action: "job.run", Application: "my-api", Target: "nightly", Outcome: api.AuditFailed, Status: 409, Code: api.CodeJobAlreadyRunning},
		{ID: 6, At: when, Actor: api.Actor{Kind: api.ActorToken, Name: "root"}, Address: "127.0.0.1", Action: "secret.set", Target: "DATABASE_URL", Outcome: api.AuditOK, Status: 204},
		{ID: 5, At: when, Actor: api.Actor{Kind: api.ActorToken, Name: "root"}, Address: "127.0.0.1", Action: "key.rotate", Outcome: api.AuditFailed, Status: 500},
	}
	out, _, err := a.run(t.TempDir(), "audit")
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	if a.query != "limit=50" {
		t.Errorf("query = %q", a.query)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 {
		t.Fatalf("unexpected output:\n%s", out)
	}
	stamp := when.Local().Format("2006-01-02 15:04:05")
	assertInOrder(t, lines[0], []string{"WHEN", "WHO", "ACTION", "ON", "RESULT", "FROM", "DETAIL"})
	assertInOrder(t, lines[1], []string{stamp, "ci", "deploy", "my-api", "ok", "203.0.113.9", "deployment 12"})
	assertInOrder(t, lines[2], []string{stamp, "ci", "deploy", "web", "refused", "172.18.0.3"})
	assertInOrder(t, lines[3], []string{"root", "job.run", "my-api nightly", "failed: JOB_ALREADY_RUNNING", "127.0.0.1"})
	assertInOrder(t, lines[4], []string{"root", "secret.set", "DATABASE_URL", "ok"})
	assertInOrder(t, lines[5], []string{"root", "key.rotate", "server", "failed", "127.0.0.1"})

	// A full page says how to go on from its last entry.
	out, _, _ = a.run(t.TempDir(), "audit", "--app", "my-api", "--actor", "ci", "--since", "7d", "-n", "5")
	if a.query != "actor=ci&application=my-api&limit=5&since=2026-02-22T12%3A00%3A00Z" {
		t.Errorf("query = %q", a.query)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "Older entries: shipwick audit --app my-api --actor ci --since 7d -n 5 --before 5") {
		t.Errorf("a full page should end with the command for the next:\n%s", out)
	}
	a.run(t.TempDir(), "audit", "--before", "5", "--since", "2026-02-01")
	if a.query != "before=5&limit=50&since=2026-02-01T00%3A00%3A00Z" {
		t.Errorf("query = %q", a.query)
	}

	a.entries = []api.AuditEntry{}
	if out, _, _ = a.run(t.TempDir(), "audit"); !strings.Contains(out, "Nothing recorded yet") {
		t.Errorf("an empty trail:\n%s", out)
	}
	if out, _, _ = a.run(t.TempDir(), "audit", "--actor", "ci"); strings.TrimSpace(out) != "Nothing recorded that matches." {
		t.Errorf("an empty answer to a filter:\n%s", out)
	}

	for want, args := range map[string][]string{
		"-n must be between 1 and 500": {"audit", "-n", "0"},
		"--since: invalid time":        {"audit", "--since", "lately"},
		"invalid":                      {"audit", "--app", "Not_Valid"},
	} {
		a.query = ""
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.Contains(err.Error(), want) || a.query != "" {
			t.Errorf("%v: err = %v, query = %q; want %q and nothing sent", args, err, a.query, want)
		}
	}
}

func TestAuditSaysWhenTheAgentKeepsNoTrail(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: GET /api/v1/audit"})
	}))
	t.Cleanup(old.Close)
	a := &fakeAgent{t: t, srv: old}
	_, _, err := a.run(t.TempDir(), "audit")
	want := "Error: the agent is older than this shipwick and keeps no audit trail\n\nCompare versions with: shipwick server status"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
