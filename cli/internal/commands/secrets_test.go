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
	removed     []string
	removeError *api.Error
}

func newSecretAgent(t *testing.T) *secretAgent {
	t.Helper()
	a := &secretAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}, setBodies: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, a.secrets)
	})
	mux.HandleFunc("PUT /api/v1/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.setBodies[r.PathValue("name")] = strings.TrimSpace(string(body))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		a.removed = append(a.removed, r.PathValue("name"))
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
	}
	out, _, err := a.run(t.TempDir(), "secret", "ls")
	if err != nil {
		t.Fatalf("secret ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"NAME", "CREATED", "UPDATED"})
	assertInOrder(t, lines[1], []string{"API_KEY", "3d ago", "3d ago"})
	assertInOrder(t, lines[2], []string{"DB_PASSWORD", "2h ago", "5m ago"})

	a.secrets = nil
	out, _, _ = a.run(t.TempDir(), "secret", "list")
	if !strings.Contains(out, "No secrets on the server") || !strings.Contains(out, "shipwick secret set") {
		t.Errorf("an empty list should say what to do next:\n%s", out)
	}
}

func TestSecretRemoveRequiresConfirmation(t *testing.T) {
	a := newSecretAgent(t)
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

	a.removeError = &api.Error{Code: api.CodeNotFound, Message: "not found"}
	_, _, err = a.run(t.TempDir(), "secret", "remove", "GHOST", "--yes")
	if got := Render(err); !strings.Contains(got, "no secret named GHOST") || !strings.Contains(got, "shipwick secret ls") {
		t.Errorf("unexpected rendering: %s", got)
	}

	a.requests = nil
	if _, _, err := a.run(t.TempDir(), "secret", "rm", "not-a-name", "--yes"); err == nil || len(a.requests) != 0 {
		t.Errorf("an invalid name must be refused here: err = %v, requests = %v", err, a.requests)
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
