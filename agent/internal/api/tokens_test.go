package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// createToken makes a token through the API, as root, and returns it.
func (f *fixture) createToken(name string, role api.Role) api.CreatedToken {
	f.t.Helper()
	status, body := f.do("POST", "/api/v1/tokens", `{"name": "`+name+`", "role": "`+string(role)+`"}`)
	if status != http.StatusCreated {
		f.t.Fatalf("create token: status = %d, body = %s", status, body)
	}
	return decode[api.CreatedToken](f.t, body)
}

func TestRootTokenIsAdminAndKnowsItself(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("GET", "/api/v1/server", "")
	if server := decode[api.Server](t, body); !reflect.DeepEqual(server.Token, api.TokenIdentity{Kind: api.ActorToken, Name: api.RootTokenName, Role: api.RoleAdmin, Applications: []string{}}) {
		t.Errorf("token = %+v, want root/admin", server.Token)
	}
}

func TestTokenLifecycle(t *testing.T) {
	f := newFixture(t)

	created := f.createToken("ci", api.RoleDeploy)
	if created.Name != "ci" || created.Role != api.RoleDeploy || created.ID == 0 {
		t.Errorf("unexpected token: %+v", created)
	}
	// 32 random bytes in unpadded base64url are 43 characters.
	if !strings.HasPrefix(created.Token, api.TokenPrefix) || len(created.Token) != len(api.TokenPrefix)+43 {
		t.Errorf("token value = %q: want swk_ and 43 characters", created.Token)
	}
	auth := "Bearer " + created.Token

	// It is a token: it authenticates, and it knows who it is.
	status, body := f.doWithAuth("GET", "/api/v1/server", "", auth)
	if server := decode[api.Server](t, body); status != http.StatusOK || !reflect.DeepEqual(server.Token, api.TokenIdentity{Kind: api.ActorToken, Name: "ci", Role: api.RoleDeploy, Applications: []string{}}) {
		t.Errorf("status = %d, token = %+v", status, server.Token)
	}

	// Its role lets it deploy, and the deployment records its name.
	status, body = f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, auth)
	if d := decode[api.Deployment](t, body); status != http.StatusAccepted || d.By != "ci" {
		t.Errorf("deploy: status = %d, by = %q, body = %s", status, d.By, body)
	}
	f.engine.Wait()
	_, body = f.do("GET", "/api/v1/deployments/1", "")
	if d := decode[api.DeploymentDetail](t, body); d.By != "ci" {
		t.Errorf("stored deployment by = %q, want ci", d.By)
	}

	// Its role does not let it delete, nor see the tokens.
	for _, tt := range []struct{ method, path string }{
		{"DELETE", "/api/v1/applications/my-api"},
		{"GET", "/api/v1/tokens"},
		{"POST", "/api/v1/tokens"},
		{"DELETE", "/api/v1/tokens/ci"},
	} {
		status, body := f.doWithAuth(tt.method, tt.path, "", auth)
		e := decodeError(t, body)
		if status != http.StatusForbidden || e.Code != api.CodeForbidden {
			t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
		}
		if e.Details["role"] != "deploy" || e.Details["required"] != "admin" || e.Message != "this token has the deploy role; this needs admin" {
			t.Errorf("%s %s: unexpected error: %+v", tt.method, tt.path, e)
		}
	}

	// The list shows the token without any secret.
	_, body = f.do("GET", "/api/v1/tokens", "")
	tokens := decode[[]api.Token](t, body)
	if len(tokens) != 1 || tokens[0].Name != "ci" || tokens[0].Role != api.RoleDeploy || tokens[0].LastUsedAt == nil {
		t.Errorf("unexpected list: %+v", tokens)
	}
	if strings.Contains(string(body), created.Token) || strings.Contains(strings.ToLower(string(body)), "hash") {
		t.Errorf("the list must not carry the token or its hash: %s", body)
	}

	// Revoked, it is a stranger; revoking it again finds nothing.
	if status, _ := f.do("DELETE", "/api/v1/tokens/ci", ""); status != http.StatusNoContent {
		t.Fatalf("revoke: status = %d", status)
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/applications", "", auth); status != http.StatusUnauthorized {
		t.Errorf("revoked token: status = %d, want 401", status)
	}
	if status, body := f.do("DELETE", "/api/v1/tokens/ci", ""); status != http.StatusNotFound || decodeError(t, body).Code != api.CodeNotFound {
		t.Errorf("revoke twice: status = %d, body = %s", status, body)
	}
	if _, body = f.do("GET", "/api/v1/tokens", ""); string(decode[json.RawMessage](t, body)) != "[]" {
		t.Errorf("an empty list must be [], got %s", body)
	}
}

func TestReadTokenMayLookButNotTouch(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	auth := "Bearer " + f.createToken("viewer", api.RoleRead).Token

	for _, path := range []string{"/api/v1/applications", "/api/v1/applications/my-api", "/api/v1/deployments", "/api/v1/applications/my-api/logs"} {
		if status, _ := f.doWithAuth("GET", path, "", auth); status != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, status)
		}
	}
	for _, tt := range []struct{ method, path, body string }{
		{"POST", "/api/v1/applications/my-api/deploy", validConfig},
		{"POST", "/api/v1/applications/my-api/redeploy", ""},
		{"POST", "/api/v1/applications/my-api/stop", ""},
		{"DELETE", "/api/v1/applications/my-api", ""},
	} {
		status, body := f.doWithAuth(tt.method, tt.path, tt.body, auth)
		e := decodeError(t, body)
		if status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["role"] != "read" {
			t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
		}
		if tt.method == "POST" && e.Message != "this token has the read role; deploying needs deploy or admin" {
			t.Errorf("%s %s: message = %q", tt.method, tt.path, e.Message)
		}
	}
	if all := decodeList(t, f); len(all) != 1 {
		t.Errorf("refused requests must not create deployments, got %d", len(all))
	}
}

func TestCreateTokenRules(t *testing.T) {
	f := newFixture(t)
	f.createToken("ci", api.RoleDeploy)

	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"duplicate name", `{"name": "ci", "role": "read"}`, http.StatusConflict, api.CodeTokenExists},
		{"root is taken", `{"name": "root", "role": "admin"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"uppercase", `{"name": "CI", "role": "deploy"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"too long", `{"name": "` + strings.Repeat("a", 41) + `", "role": "deploy"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"unknown role", `{"name": "ops", "role": "owner"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"missing role", `{"name": "ops"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"missing name", `{"role": "read"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"empty body", ``, http.StatusBadRequest, api.CodeInvalidRequest},
		{"unknown field", `{"name": "ops", "role": "read", "hash": "x"}`, http.StatusBadRequest, api.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := f.do("POST", "/api/v1/tokens", tt.body)
			if e := decodeError(t, body); status != tt.status || e.Code != tt.code {
				t.Errorf("status = %d, error = %+v", status, e)
			}
		})
	}

	// The root token is configured, not stored: nothing here can revoke it.
	if status, body := f.do("DELETE", "/api/v1/tokens/root", ""); status != http.StatusBadRequest {
		t.Errorf("revoke root: status = %d, body = %s", status, body)
	}
	if status, _ := f.do("DELETE", "/api/v1/tokens/Not_Valid", ""); status != http.StatusBadRequest {
		t.Errorf("revoke an invalid name: status = %d, want 400", status)
	}
	if _, body := f.do("GET", "/api/v1/tokens", ""); len(decode[[]api.Token](t, body)) != 1 {
		t.Errorf("rejected requests must not create tokens: %s", body)
	}
}

func TestTokenUseIsWrittenDownOnceAMinute(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }

	auth := "Bearer " + f.createToken("ci", api.RoleRead).Token
	lastUsed := func() time.Time {
		t.Helper()
		_, body := f.do("GET", "/api/v1/tokens", "")
		tokens := decode[[]api.Token](t, body)
		if len(tokens) != 1 || tokens[0].LastUsedAt == nil {
			t.Fatalf("unexpected list: %+v", tokens)
		}
		return *tokens[0].LastUsedAt
	}

	for range 3 {
		f.doWithAuth("GET", "/api/v1/applications", "", auth)
	}
	first := lastUsed()
	if !first.Equal(clock) {
		t.Fatalf("last used = %s, want %s", first, clock)
	}

	clock = clock.Add(30 * time.Second)
	f.doWithAuth("GET", "/api/v1/applications", "", auth)
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("a use 30s later was written down (%s); it should wait for the minute", got)
	}

	clock = clock.Add(31 * time.Second)
	f.doWithAuth("GET", "/api/v1/applications", "", auth)
	if got := lastUsed(); !got.Equal(clock) {
		t.Errorf("last used = %s, want %s after a minute", got, clock)
	}
}

func TestStopAndStartEventsSayWho(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	ci := "Bearer " + f.createToken("ci", api.RoleDeploy).Token

	if status, _ := f.doWithAuth("POST", "/api/v1/applications/my-api/stop", "", ci); status != http.StatusOK {
		t.Fatalf("stop: status = %d", status)
	}
	if status, _ := f.do("POST", "/api/v1/applications/my-api/start", ""); status != http.StatusOK {
		t.Fatalf("start: status = %d", status)
	}
	_, body := f.do("GET", "/api/v1/applications/my-api/events", "")
	events := decode[[]api.Event](t, body)
	if len(events) != 2 || events[0].Message != "Application started" || events[1].Message != "Application stopped by ci" {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestStoredTokenValueIsNeverLogged(t *testing.T) {
	f := newFixture(t)
	created := f.createToken("ci", api.RoleDeploy)
	f.doWithAuth("GET", "/api/v1/applications", "", "Bearer "+created.Token)
	f.doWithAuth("GET", "/api/v1/applications", "", "Bearer swk_not-a-real-token-value")
	f.do("DELETE", "/api/v1/tokens/ci", "")

	logged := f.logs.String()
	if !strings.Contains(logged, "token created") || !strings.Contains(logged, "token revoked") {
		t.Error("creating and revoking a token are worth a log line")
	}
	for _, secret := range []string{created.Token, "swk_not-a-real-token-value"} {
		if strings.Contains(logged, secret) {
			t.Errorf("logs contain the token %q", secret)
		}
	}
}
