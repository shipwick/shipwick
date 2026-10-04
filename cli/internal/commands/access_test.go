package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

// accessAgent answers the access endpoints the way the agent does.
type accessAgent struct {
	*fakeAgent
	rules      []api.AccessRule
	sessions   []api.Session
	configured bool
	// nameClaim is what GET /server reports people are named by; "" as an
	// agent from before 0.7.
	nameClaim string
	grantBody string
	revoked   []string
	signedOut []string
}

func newAccessAgent(t *testing.T) *accessAgent {
	t.Helper()
	a := &accessAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}, configured: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/access/rules", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.rules) })
	mux.HandleFunc("POST /api/v1/access/rules", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.grantBody = strings.TrimSpace(string(body))
		respond(w, 201, api.AccessRule{ID: 9, Kind: api.AccessGroup, Subject: "backend", Role: api.RoleDeploy, Applications: []string{"my-api", "worker"}})
	})
	mux.HandleFunc("DELETE /api/v1/access/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.revoked = append(a.revoked, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/access/sessions", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.sessions) })
	mux.HandleFunc("DELETE /api/v1/access/sessions/{email}", func(w http.ResponseWriter, r *http.Request) {
		a.signedOut = append(a.signedOut, r.PathValue("email"))
		respond(w, 200, api.SignedOut{Sessions: len(a.sessions)})
	})
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, api.Health{Status: "ok", Version: "1.2.3"})
	})
	mux.HandleFunc("GET /api/v1/server", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, api.Server{AgentVersion: "1.2.3", Hostname: "server-1", Token: api.TokenIdentity{Name: "root", Role: api.RoleAdmin},
			SignIn: api.SignInStatus{Configured: a.configured, Issuer: "https://accounts.example.com", NameClaim: a.nameClaim}})
	})
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestAccessGrantSendsWhoAndWhatAndRefusesTheRestLocally(t *testing.T) {
	a := newAccessAgent(t)
	out, _, err := a.run(t.TempDir(), "access", "grant", "group:backend", "--role", "deploy", "--app", "worker", "--app", "my-api")
	if err != nil {
		t.Fatalf("access grant: %v\n%s", err, out)
	}
	if a.grantBody != `{"kind":"group","subject":"backend","role":"deploy","applications":["my-api","worker"]}` {
		t.Errorf("request body: %s", a.grantBody)
	}
	assertInOrder(t, out, []string{"group:backend has the deploy role, limited to my-api, worker", "signed out with their next request"})

	a.run(t.TempDir(), "access", "grant", "Ada@Example.com", "--role", "admin")
	if a.grantBody != `{"kind":"email","subject":"ada@example.com","role":"admin"}` {
		t.Errorf("an address: %s", a.grantBody)
	}
	a.run(t.TempDir(), "access", "grant", "*@example.com", "--role", "read")
	if a.grantBody != `{"kind":"domain","subject":"example.com","role":"read"}` {
		t.Errorf("a domain: %s", a.grantBody)
	}

	for want, args := range map[string][]string{
		`"ada" is neither an address, a domain, a group nor a name`: {"access", "grant", "ada", "--role", "read"},
		"not an e-mail address":                      {"access", "grant", "a b@example.com", "--role", "read"},
		"choose what they may do: --role":            {"access", "grant", "ada@example.com"},
		"invalid role":                               {"access", "grant", "ada@example.com", "--role", "owner"},
		"--app: only the deploy role can be limited": {"access", "grant", "ada@example.com", "--role", "admin", "--app", "web"},
		"--app: applications: ":                      {"access", "grant", "ada@example.com", "--role", "deploy", "--app", "Not_Valid"},
	} {
		a.requests = nil
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", args, a.requests)
		}
	}
}

func TestAccessListShowsTheRulesAndSaysWhenNoneApplyYet(t *testing.T) {
	a := newAccessAgent(t)
	a.rules = []api.AccessRule{
		{ID: 1, Kind: api.AccessEmail, Subject: "ada@example.com", Role: api.RoleAdmin, Applications: []string{}, CreatedAt: fixedNow.Add(-2 * time.Hour), CreatedBy: "root"},
		{ID: 2, Kind: api.AccessGroup, Subject: "backend", Role: api.RoleDeploy, Applications: []string{"my-api", "worker"}, CreatedAt: fixedNow.Add(-3 * 24 * time.Hour), CreatedBy: "ada@example.com"},
		{ID: 3, Kind: api.AccessDomain, Subject: "example.com", Role: api.RoleRead, CreatedAt: fixedNow.Add(-time.Minute), CreatedBy: "root"},
	}
	out, _, err := a.run(t.TempDir(), "access", "ls")
	if err != nil {
		t.Fatalf("access ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"WHO", "ROLE", "APPLICATIONS", "GRANTED", "BY"})
	assertInOrder(t, lines[1], []string{"ada@example.com", "admin", "all", "2h ago", "root"})
	assertInOrder(t, lines[2], []string{"group:backend", "deploy", "my-api, worker", "3d ago", "ada@example.com"})
	assertInOrder(t, lines[3], []string{"*@example.com", "read", "all"})

	a.configured = false
	if out, _, _ = a.run(t.TempDir(), "access", "ls"); !strings.Contains(out, "Signing in is not configured on this agent, so no rule applies yet: set SHIPWICK_OIDC_ISSUER") {
		t.Errorf("without a provider:\n%s", out)
	}
	a.rules, a.configured = nil, true
	if out, _, _ = a.run(t.TempDir(), "access", "ls"); !strings.Contains(out, "No rules: nobody can sign in. Grant access with: shipwick access grant") {
		t.Errorf("without rules:\n%s", out)
	}
}

func TestAccessRevokeFindsTheRuleByWhoItIsFor(t *testing.T) {
	a := newAccessAgent(t)
	a.rules = []api.AccessRule{
		{ID: 4, Kind: api.AccessGroup, Subject: "ada@example.com", Role: api.RoleRead},
		{ID: 7, Kind: api.AccessEmail, Subject: "ada@example.com", Role: api.RoleAdmin},
	}
	out, _, err := a.run(t.TempDir(), "access", "revoke", "ADA@example.com")
	if err != nil || len(a.revoked) != 1 || a.revoked[0] != "7" {
		t.Fatalf("access revoke: %v, revoked %v\n%s", err, a.revoked, out)
	}
	assertInOrder(t, out, []string{"Revoked the rule for ada@example.com", "signed out with their next request"})

	_, _, err = a.run(t.TempDir(), "access", "revoke", "group:nobody")
	if got := Render(err); got != "Error: there is no rule for group:nobody\n\nList the rules with: shipwick access ls" || len(a.revoked) != 1 {
		t.Errorf("a rule that is not there: %q, revoked %v", got, a.revoked)
	}
}

func TestAccessSessionsAndSignout(t *testing.T) {
	a := newAccessAgent(t)
	if out, _, _ := a.run(t.TempDir(), "access", "sessions"); strings.TrimSpace(out) != "Nobody is signed in." {
		t.Errorf("no sessions:\n%s", out)
	}
	if out, _, _ := a.run(t.TempDir(), "access", "signout", "ada@example.com"); strings.TrimSpace(out) != "ada@example.com was not signed in." {
		t.Errorf("signing out nobody:\n%s", out)
	}

	used := fixedNow.Add(-5 * time.Minute)
	a.sessions = []api.Session{
		{ID: 1, Email: "ada@example.com", Role: api.RoleDeploy, Applications: []string{"my-api"}, CreatedAt: fixedNow.Add(-2 * time.Hour), ExpiresAt: fixedNow.Add(8 * time.Hour), LastUsedAt: &used},
		{ID: 2, Email: "ada@example.com", Role: api.RoleDeploy, Applications: []string{"my-api"}, CreatedAt: fixedNow.Add(-9*time.Hour - 30*time.Minute), ExpiresAt: fixedNow.Add(30 * time.Minute)},
	}
	out, _, err := a.run(t.TempDir(), "access", "sessions")
	if err != nil {
		t.Fatalf("access sessions: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"WHO", "ROLE", "APPLICATIONS", "SIGNED IN", "ENDS", "LAST USED"})
	assertInOrder(t, lines[1], []string{"ada@example.com", "deploy", "my-api", "2h ago", "in 8 hours", "5m ago"})
	assertInOrder(t, lines[2], []string{"ada@example.com", "deploy", "my-api", "9h ago", "in 30 minutes", "never"})

	out, _, err = a.run(t.TempDir(), "access", "signout", "Ada@Example.com")
	if err != nil || len(a.signedOut) != 2 || a.signedOut[1] != "ada@example.com" || !strings.Contains(out, "Signed ada@example.com out of 2 sessions") {
		t.Errorf("access signout: %v, sent %v\n%s", err, a.signedOut, out)
	}
	if _, _, err := a.run(t.TempDir(), "access", "signout", "group:backend"); err == nil || !strings.Contains(err.Error(), "group:backend is not a person") {
		t.Errorf("signing out a group: %v", err)
	}
}

func TestAccessSaysWhenTheAgentHasNoSignIn(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
	}))
	t.Cleanup(old.Close)
	a := &fakeAgent{t: t, srv: old}
	want := "Error: the agent is older than this shipwick and has no sign-in: it accepts tokens only\n\nCompare versions with: shipwick server status"
	for _, args := range [][]string{{"access", "ls"}, {"access", "grant", "ada@example.com", "--role", "read"}, {"access", "revoke", "ada@example.com"},
		{"access", "sessions"}, {"access", "signout", "ada@example.com"}} {
		if _, _, err := a.run(t.TempDir(), args...); Render(err) != want {
			t.Errorf("%v:\n got %q\nwant %q", args, Render(err), want)
		}
	}
}

func TestServerStatusNamesTheSignInProviderAndASessionThatIsOverSaysSo(t *testing.T) {
	a := newAccessAgent(t)
	out, _, err := a.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Sign-in", "https://accounts.example.com"})
	a.configured = false
	if out, _, _ = a.run(t.TempDir(), "server", "status"); strings.Contains(out, "Sign-in") {
		t.Errorf("an agent without a provider:\n%s", out)
	}

	got := Render(&client.APIError{Status: 401, Code: api.CodeSessionEnded, Message: "what ada@example.com may do on this server was changed. Sign in again"})
	want := "The session this command ran with no longer works: what ada@example.com may do on this server was changed. Sign in again.\n\n" +
		"The CLI and CI are meant to be given a token; an admin creates one with: shipwick token create"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
