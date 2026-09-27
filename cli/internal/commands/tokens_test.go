package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

const fakeTokenValue = "swk_0123456789abcdefghijklmnopqrstuvwxyzABCDEF"

// tokenAgent serves the token endpoints. It keeps the tokens it is given and
// records what it was asked to create.
type tokenAgent struct {
	*fakeAgent
	tokens      []api.Token
	createBody  string
	revoked     []string
	revokeError *api.Error
}

func newTokenAgent(t *testing.T) *tokenAgent {
	t.Helper()
	a := &tokenAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, a.tokens)
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.createBody = strings.TrimSpace(string(body))
		var req api.CreateTokenRequest
		json.Unmarshal(body, &req)
		respond(w, 201, api.CreatedToken{ID: 7, Name: req.Name, Role: req.Role, CreatedAt: fixedNow, Token: fakeTokenValue})
	})
	mux.HandleFunc("DELETE /api/v1/tokens/{name}", func(w http.ResponseWriter, r *http.Request) {
		a.revoked = append(a.revoked, r.PathValue("name"))
		if a.revokeError != nil {
			respondError(w, 404, *a.revokeError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestTokenCreateShowsTheValueOnce(t *testing.T) {
	a := newTokenAgent(t)
	out, _, err := a.run(t.TempDir(), "token", "create", "ci", "--role", "deploy")
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Created token ci with the deploy role",
		fakeTokenValue,
		"will not be shown again",
		"SHIPWICK_AGENT_TOKEN",
		"shipwick login",
	})
	if strings.Count(out, fakeTokenValue) != 1 {
		t.Errorf("the token should appear exactly once:\n%s", out)
	}
	if a.createBody != `{"name":"ci","role":"deploy"}` {
		t.Errorf("unexpected request body: %s", a.createBody)
	}

	// Everything the agent would reject is rejected here first.
	for _, args := range [][]string{
		{"token", "create", "ci"},
		{"token", "create", "ci", "--role", "owner"},
		{"token", "create", "Not_Valid", "--role", "read"},
		{"token", "create", "root", "--role", "admin"},
	} {
		a.requests = nil
		if _, _, err := a.run(t.TempDir(), args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", args, a.requests)
		}
	}
	_, _, err = a.run(t.TempDir(), "token", "create", "ci")
	if err == nil || !strings.Contains(err.Error(), "--role") {
		t.Errorf("without a role the error should point at --role, got %v", err)
	}
}

func TestTokenList(t *testing.T) {
	a := newTokenAgent(t)
	used := fixedNow.Add(-5 * time.Minute)
	a.tokens = []api.Token{
		{ID: 1, Name: "ci", Role: api.RoleDeploy, CreatedAt: fixedNow.Add(-2 * time.Hour), LastUsedAt: &used},
		{ID: 2, Name: "viewer", Role: api.RoleRead, CreatedAt: fixedNow.Add(-3 * 24 * time.Hour)},
	}
	out, _, err := a.run(t.TempDir(), "token", "ls")
	if err != nil {
		t.Fatalf("token ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"NAME", "ROLE", "CREATED", "LAST USED"})
	assertInOrder(t, lines[1], []string{"ci", "deploy", "2h ago", "5m ago"})
	assertInOrder(t, lines[2], []string{"viewer", "read", "3d ago", "never"})
	if strings.Index(lines[0], "ROLE") != strings.Index(lines[1], "deploy") {
		t.Errorf("misaligned columns:\n%s", out)
	}

	a.tokens = nil
	out, _, _ = a.run(t.TempDir(), "token", "list")
	if !strings.Contains(out, "No tokens besides") || !strings.Contains(out, "shipwick token create") {
		t.Errorf("an empty list should say what to do next:\n%s", out)
	}
}

func TestTokenRevokeRequiresConfirmation(t *testing.T) {
	a := newTokenAgent(t)
	if _, _, err := a.run(t.TempDir(), "token", "revoke", "ci"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want a refusal pointing at --yes", err)
	}
	if len(a.requests) != 0 {
		t.Errorf("nothing may be sent without confirmation, saw %v", a.requests)
	}

	out, _, err := a.run(t.TempDir(), "token", "revoke", "ci", "--yes")
	if err != nil || !strings.Contains(out, "Revoked token ci") {
		t.Errorf("revoke --yes: %v\n%s", err, out)
	}
	if len(a.revoked) != 1 || a.revoked[0] != "ci" {
		t.Errorf("revoked = %v", a.revoked)
	}

	a.revokeError = &api.Error{Code: api.CodeNotFound, Message: "not found"}
	_, _, err = a.run(t.TempDir(), "token", "revoke", "ghost", "--yes")
	if got := Render(err); !strings.Contains(got, "no token named ghost") || !strings.Contains(got, "shipwick token ls") {
		t.Errorf("unexpected rendering: %s", got)
	}

	a.requests = nil
	if _, _, err := a.run(t.TempDir(), "token", "revoke", "root", "--yes"); err == nil || !strings.Contains(err.Error(), "SHIPWICK_AGENT_TOKEN") {
		t.Errorf("revoking root should explain where that token lives, got %v", err)
	}
	if len(a.requests) != 0 {
		t.Errorf("revoking root must not reach the agent, saw %v", a.requests)
	}
}

func TestRenderForbidden(t *testing.T) {
	err := &client.APIError{Status: 403, Code: api.CodeForbidden,
		Message: "this token has the read role; deploying needs deploy or admin",
		Details: map[string]any{"role": "read", "required": "deploy"}}
	want := "This token may not do that: it has the read role.\n\n" +
		"Use a token with the deploy role, or create one with: shipwick token create <name> --role deploy"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	bare := &client.APIError{Status: 403, Code: api.CodeForbidden, Message: "not allowed", Details: map[string]any{}}
	if got := Render(bare); got != "This token may not do that: not allowed" {
		t.Errorf("without details: %q", got)
	}
}

func TestServerStatusShowsTheToken(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1", Token: api.TokenIdentity{Name: "ci", Role: api.RoleDeploy}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v", err)
	}
	assertInOrder(t, out, []string{"Proxy", "Token", "ci (deploy)"})

	// An older agent says nothing about tokens, and neither do we.
	f.server.Token = api.TokenIdentity{}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	if strings.Contains(out, "Token") {
		t.Errorf("no token line without a token identity:\n%s", out)
	}
}
