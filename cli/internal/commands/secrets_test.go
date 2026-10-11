package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// secretAgent serves the secret endpoints and records what it was sent.
type secretAgent struct {
	*fakeAgent
	secrets     []api.Secret
	setBodies   map[string]string // name → the JSON body received
	removed     []string          // name, with "?application=…" when the request carried it
	removeError *api.Error
	// older: the agent is one before 0.9, which refuses a body with a key it
	// does not know and lists secrets without a scope.
	older bool
}

func newSecretAgent(t *testing.T) *secretAgent {
	t.Helper()
	a := &secretAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}, setBodies: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		if a.older {
			type old struct {
				Name      string    `json:"name"`
				CreatedAt time.Time `json:"created_at"`
				UpdatedAt time.Time `json:"updated_at"`
			}
			list := make([]old, 0, len(a.secrets))
			for _, s := range a.secrets {
				list = append(list, old{s.Name, s.CreatedAt, s.UpdatedAt})
			}
			respond(w, 200, list)
			return
		}
		respond(w, 200, a.secrets)
	})
	mux.HandleFunc("PUT /api/v1/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if a.older && strings.Contains(string(body), `"application"`) {
			respondError(w, 400, api.Error{Code: api.CodeInvalidRequest, Message: `invalid JSON body: expected {"value": "..."}`})
			return
		}
		a.setBodies[r.PathValue("name")] = strings.TrimSpace(string(body))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		removed := r.PathValue("name")
		if r.URL.RawQuery != "" {
			removed += "?" + r.URL.RawQuery
		}
		a.removed = append(a.removed, removed)
		if a.removeError != nil {
			respondError(w, 404, *a.removeError)
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

func TestSecretSetReadsTheValueFromStdinOrAFileAndNeverEchoesIt(t *testing.T) {
	a := newSecretAgent(t)

	// Piped in, as `printf '%s' "$VALUE" |` or `echo` would: one trailing
	// newline is dropped, nothing else is.
	a.stdin = " hunter2 \n"
	out, errOut, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD")
	if err != nil {
		t.Fatalf("secret set: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Stored secret DB_PASSWORD", "${DB_PASSWORD}", "next deployment"})
	if a.setBodies["DB_PASSWORD"] != `{"value":" hunter2 "}` {
		t.Errorf("unexpected request body: %s", a.setBodies["DB_PASSWORD"])
	}
	if strings.Contains(out+errOut, "hunter2") {
		t.Errorf("the value was echoed:\n%s%s", out, errOut)
	}

	a.stdin = "line one\r\n"
	if _, _, err := a.run(t.TempDir(), "secret", "set", "CRLF"); err != nil || a.setBodies["CRLF"] != `{"value":"line one"}` {
		t.Errorf("CRLF: %v, body = %s", err, a.setBodies["CRLF"])
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "key.pem"), []byte("-----BEGIN KEY-----\nabc\n-----END KEY-----\n"), 0o600)
	a.stdin = "ignored when a file is given"
	if _, _, err := a.run(dir, "secret", "set", "TLS_KEY", "--from-file", "key.pem"); err != nil {
		t.Fatalf("--from-file: %v", err)
	}
	if a.setBodies["TLS_KEY"] != `{"value":"-----BEGIN KEY-----\nabc\n-----END KEY-----"}` {
		t.Errorf("unexpected request body: %s", a.setBodies["TLS_KEY"])
	}
}

func TestSecretSetWithAppStoresTheSecretForThatApplication(t *testing.T) {
	a := newSecretAgent(t)
	a.stdin = "api-s-own"
	out, errOut, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD", "--app", "api")
	if err != nil {
		t.Fatalf("secret set --app: %v\n%s", err, out)
	}
	// The application travels in the body, where an agent that does not know
	// it refuses it instead of storing the secret for the server.
	if a.setBodies["DB_PASSWORD"] != `{"value":"api-s-own","application":"api"}` {
		t.Errorf("unexpected request body: %s", a.setBodies["DB_PASSWORD"])
	}
	assertInOrder(t, out, []string{"Stored secret DB_PASSWORD for api", "${DB_PASSWORD} in api's deploy.yaml", "before a server-wide DB_PASSWORD", "other applications do not see it"})
	if strings.Contains(out+errOut, "api-s-own") {
		t.Errorf("the value was echoed:\n%s%s", out, errOut)
	}

	// The name is checked as an application name before anything is sent.
	a.requests = nil
	a.stdin = "x"
	if _, _, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD", "--app", "Not An App"); err == nil || !strings.Contains(err.Error(), "invalid application name") || len(a.requests) != 0 {
		t.Errorf("err = %v, requests = %v", err, a.requests)
	}
}

func TestSecretSetWithAppAgainstAnOlderAgentSaysSoAndStoresNothing(t *testing.T) {
	a := newSecretAgent(t)
	a.older = true
	a.stdin = "api-s-own"
	_, _, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD", "--app", "api")
	if err == nil || !strings.Contains(err.Error(), "older than this shipwick") || !strings.Contains(err.Error(), "nothing was stored") || !strings.Contains(err.Error(), "without --app") {
		t.Errorf("err = %v, want the older agent named and the way out", err)
	}
	if _, stored := a.setBodies["DB_PASSWORD"]; stored {
		t.Error("the older agent stored the secret as the server's")
	}
	// Without --app the same agent is served as before.
	a.stdin = "shared"
	if _, _, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD"); err != nil || a.setBodies["DB_PASSWORD"] != `{"value":"shared"}` {
		t.Errorf("server-wide against an older agent: %v, body = %s", err, a.setBodies["DB_PASSWORD"])
	}
}

func TestSecretSetWarnsAboutCharactersAURLCannotCarry(t *testing.T) {
	a := newSecretAgent(t)
	for _, tt := range []struct{ value, want string }{
		{"p@ss:word/with?every#one%and a space", "(@, :, /, ?, #, %, whitespace)"},
		{"pa:ss@word", "(@, :)"},
		{"tab\there", "(whitespace)"},
		{"c4f1d9e0a7b2", ""},
	} {
		a.stdin = tt.value
		out, errOut, err := a.run(t.TempDir(), "secret", "set", "DB_PASSWORD")
		if err != nil {
			t.Fatalf("%q: %v", tt.value, err)
		}
		if tt.want == "" {
			if strings.Contains(errOut, "URL") {
				t.Errorf("%q: warned for nothing:\n%s", tt.value, errOut)
			}
			continue
		}
		// Stored all the same: a warning, not a refusal.
		if !strings.Contains(a.setBodies["DB_PASSWORD"], `"value":"`) {
			t.Errorf("%q: not stored", tt.value)
		}
		if !strings.Contains(errOut, "characters a URL cannot carry unescaped "+tt.want) || !strings.Contains(errOut, "connection string") {
			t.Errorf("%q: warning = %q, want the characters %s", tt.value, errOut, tt.want)
		}
		for _, fragment := range []string{"p@ss", "every#one", "pa:ss", "tab\there"} {
			if strings.Contains(out+errOut, fragment) {
				t.Errorf("%q: the warning repeats the value:\n%s%s", tt.value, out, errOut)
			}
		}
	}
}

func TestSecretSetRefusesBadInputBeforeContactingTheAgent(t *testing.T) {
	a := newSecretAgent(t)
	for _, tt := range []struct {
		args  []string
		stdin string
		want  string
	}{
		{[]string{"secret", "set", "db-password"}, "x", "invalid secret name"},
		{[]string{"secret", "set", "1ABC"}, "x", "invalid secret name"},
		{[]string{"secret", "set", strings.Repeat("A", 65)}, "x", "max 64"},
		{[]string{"secret", "set", "EMPTY"}, "", "no value on standard input"},
		{[]string{"secret", "set", "EMPTY"}, "\n", "the value is empty"},
		{[]string{"secret", "set", "BIG"}, strings.Repeat("x", api.MaxSecretValueBytes+1), "too large"},
		{[]string{"secret", "set", "NUL"}, "a\x00b", "NUL"},
		{[]string{"secret", "set", "MISSING", "--from-file", "nope.txt"}, "", "nope.txt"},
		{[]string{"secret", "set"}, "x", "arg"},
	} {
		a.requests = nil
		a.stdin = tt.stdin
		_, _, err := a.run(t.TempDir(), tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", tt.args, a.requests)
		}
		if err != nil && strings.Contains(err.Error(), "xxxx") {
			t.Errorf("%v: the error echoes the value", tt.args)
		}
	}
}

func TestSecretList(t *testing.T) {
	a := newSecretAgent(t)
	a.secrets = []api.Secret{
		{Name: "API_KEY", CreatedAt: fixedNow.Add(-3 * 24 * time.Hour), UpdatedAt: fixedNow.Add(-3 * 24 * time.Hour)},
		{Name: "DB_PASSWORD", CreatedAt: fixedNow.Add(-2 * time.Hour), UpdatedAt: fixedNow.Add(-5 * time.Minute)},
		{Name: "DB_PASSWORD", Application: "api", CreatedAt: fixedNow.Add(-time.Hour), UpdatedAt: fixedNow.Add(-time.Hour)},
	}
	out, _, err := a.run(t.TempDir(), "secret", "ls")
	if err != nil {
		t.Fatalf("secret ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"NAME", "SCOPE", "CREATED", "UPDATED"})
	assertInOrder(t, lines[1], []string{"API_KEY", "server", "3d ago", "3d ago"})
	assertInOrder(t, lines[2], []string{"DB_PASSWORD", "server", "2h ago", "5m ago"})
	assertInOrder(t, lines[3], []string{"DB_PASSWORD", "api", "1h ago", "1h ago"})

	// An older agent says no scope: everything it has is the server's.
	a.older = true
	out, _, _ = a.run(t.TempDir(), "secret", "ls")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 4 || strings.Count(out, "server") != 3 {
		t.Errorf("against an older agent:\n%s", out)
	}

	a.older = false
	a.secrets = nil
	out, _, _ = a.run(t.TempDir(), "secret", "list")
	if !strings.Contains(out, "No secrets on the server") || !strings.Contains(out, "shipwick secret set") {
		t.Errorf("an empty list should say what to do next:\n%s", out)
	}
}

func TestSecretRemoveRequiresConfirmation(t *testing.T) {
	a := newSecretAgent(t)
	a.secrets = []api.Secret{{Name: "DB_PASSWORD"}}
	if _, _, err := a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want a refusal pointing at --yes", err)
	}
	if len(a.requests) != 0 {
		t.Errorf("nothing may be sent without confirmation, saw %v", a.requests)
	}

	out, _, err := a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD", "--yes")
	if err != nil || !strings.Contains(out, "Removed secret DB_PASSWORD") {
		t.Errorf("rm --yes: %v\n%s", err, out)
	}
	if len(a.removed) != 1 || a.removed[0] != "DB_PASSWORD" {
		t.Errorf("removed = %v", a.removed)
	}

	// A name the listing does not have is refused before anything is sent.
	a.removed = nil
	_, _, err = a.run(t.TempDir(), "secret", "remove", "GHOST", "--yes")
	if got := Render(err); !strings.Contains(got, "no secret named GHOST") || !strings.Contains(got, "shipwick secret ls") || len(a.removed) != 0 {
		t.Errorf("unexpected rendering: %s (removed %v)", got, a.removed)
	}
	// And one that went between the listing and the request is reported
	// the same way.
	a.removeError = &api.Error{Code: api.CodeNotFound, Message: "not found"}
	_, _, err = a.run(t.TempDir(), "secret", "remove", "DB_PASSWORD", "--yes")
	if got := Render(err); !strings.Contains(got, "no secret named DB_PASSWORD") {
		t.Errorf("unexpected rendering: %s", got)
	}

	a.requests = nil
	if _, _, err := a.run(t.TempDir(), "secret", "rm", "not-a-name", "--yes"); err == nil || len(a.requests) != 0 {
		t.Errorf("an invalid name must be refused here: err = %v, requests = %v", err, a.requests)
	}
}

func TestSecretRemoveWithAppRemovesTheApplicationsOwnAndNothingElse(t *testing.T) {
	a := newSecretAgent(t)
	a.secrets = []api.Secret{
		{Name: "DB_PASSWORD"},
		{Name: "DB_PASSWORD", Application: "api"},
		{Name: "DB_PASSWORD", Application: "web"},
		{Name: "ONLY_API", Application: "api"},
	}
	out, _, err := a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD", "--app", "api", "--yes")
	if err != nil || !strings.Contains(out, "Removed secret DB_PASSWORD of api") {
		t.Errorf("rm --app: %v\n%s", err, out)
	}
	if len(a.removed) != 1 || a.removed[0] != "DB_PASSWORD?application=api" {
		t.Errorf("removed = %v, want the request to carry the application", a.removed)
	}

	// Removing the server's is allowed while applications keep their own,
	// and says so.
	a.removed = nil
	out, _, err = a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD", "--yes")
	if err != nil {
		t.Fatalf("rm the server's: %v", err)
	}
	assertInOrder(t, out, []string{"Removed secret DB_PASSWORD", "The DB_PASSWORD of api and web stays", "${DB_PASSWORD} is unchanged there"})
	if len(a.removed) != 1 || a.removed[0] != "DB_PASSWORD" {
		t.Errorf("removed = %v", a.removed)
	}

	// An application without one of its own: the server's is pointed at,
	// and nothing is sent.
	a.removed = nil
	_, _, err = a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD", "--app", "worker", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no secret DB_PASSWORD for application worker") || !strings.Contains(err.Error(), "shipwick secret rm DB_PASSWORD") || strings.Contains(err.Error(), "0.9") || len(a.removed) != 0 {
		t.Errorf("err = %v, removed = %v", err, a.removed)
	}
	// A name only applications have, asked for without --app.
	_, _, err = a.run(t.TempDir(), "secret", "rm", "ONLY_API", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no server-wide secret ONLY_API") || !strings.Contains(err.Error(), "shipwick secret rm ONLY_API --app api") || len(a.removed) != 0 {
		t.Errorf("err = %v, removed = %v", err, a.removed)
	}
}

func TestSecretRemoveWithAppAgainstAnOlderAgentSendsNothing(t *testing.T) {
	a := newSecretAgent(t)
	a.older = true
	a.secrets = []api.Secret{{Name: "DB_PASSWORD"}}
	// An older agent would ignore the parameter and remove the server's
	// secret; the listing, which has no scope, keeps the request from going.
	_, _, err := a.run(t.TempDir(), "secret", "rm", "DB_PASSWORD", "--app", "api", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no secret DB_PASSWORD for application api") || !strings.Contains(err.Error(), "0.9 or newer") {
		t.Errorf("err = %v", err)
	}
	if len(a.removed) != 0 {
		t.Errorf("a request reached the older agent: %v", a.removed)
	}
}

func TestDeployLeavesUnsetEnvPlaceholdersToTheServer(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done}}}
	f.env = map[string]string{"TAG": "1.4.2"}

	dir := writeConfig(t, secretConfig)
	out, _, err := f.run(dir, "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ Validated deploy.yaml (1 variable substituted, 1 left to the server)") {
		t.Errorf("unexpected note:\n%s", out)
	}
	sent, err := spec.Parse([]byte(f.deployBodies[0]))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Image != "ghcr.io/company/my-api:1.4.2" || sent.Env["DATABASE_URL"] != "postgres://app:${DB_PASSWORD}@postgres:5432/app" {
		t.Errorf("sent image=%s env=%v", sent.Image, sent.Env)
	}

	out, _, err = f.run(dir, "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"deploy.yaml is valid (1 variable substituted, 1 left to the server)",
		"Environment",
		"${DB_PASSWORD} is not set here; the server fills it in from its secrets",
	})

	// Only env values may be left to the server.
	f.env = nil
	if _, _, err := f.run(dir, "validate"); err == nil || !strings.Contains(err.Error(), "${TAG}") || !strings.Contains(err.Error(), "Only values under env") {
		t.Errorf("err = %v", err)
	}

	// Nothing substituted here, all left to the server.
	f.env = map[string]string{"TAG": "1.4.2"}
	dir = writeConfig(t, "name: my-api\nimage: nginx\nenv:\n  A: ${A}\n  B: ${B}\n")
	out, _, err = f.run(dir, "validate")
	if err != nil || !strings.Contains(out, "is valid (2 variables left to the server)") {
		t.Errorf("validate: %v\n%s", err, out)
	}
}

func TestAMissingSecretRendersAsAValidationErrorWithTheCommandToRun(t *testing.T) {
	f := newFakeAgent(t)
	f.deployStatus = 400
	f.deployError = api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{
		"fields": []map[string]string{{
			"field":    "env.DATABASE_URL",
			"message":  "refers to ${DB_PASSWORD}, which is not set where shipwick runs and not stored on the server",
			"expected": "shipwick secret set DB_PASSWORD",
		}},
	}}
	_, _, err := f.run(writeConfig(t, "name: my-api\nimage: nginx\nenv:\n  DATABASE_URL: ${DB_PASSWORD}\n"), "deploy")
	want := "invalid deploy.yaml\n\nenv.DATABASE_URL:\n  refers to ${DB_PASSWORD}, which is not set where shipwick runs and not stored on the server\n  expected: shipwick secret set DB_PASSWORD\n"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
