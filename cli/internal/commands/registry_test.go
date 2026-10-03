package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// registryAgent serves the registry endpoints and the key rotation, and
// records what it was sent.
type registryAgent struct {
	*fakeAgent
	registries []api.Registry
	setBodies  map[string]string // registry → the JSON body received
	setError   *api.Error
	removed    []string
	rotation   api.KeyRotation
	rotateErr  *api.Error
	old        bool // an agent from before these endpoints existed
}

func newRegistryAgent(t *testing.T) *registryAgent {
	t.Helper()
	a := &registryAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}, setBodies: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/registries", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, a.registries)
	})
	mux.HandleFunc("PUT /api/v1/registries/{registry}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.setBodies[r.PathValue("registry")] = strings.TrimSpace(string(body))
		if a.setError != nil {
			respondError(w, 400, *a.setError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/registries/{registry}", func(w http.ResponseWriter, r *http.Request) {
		for _, known := range a.registries {
			if known.Registry == r.PathValue("registry") {
				a.removed = append(a.removed, known.Registry)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "no credential is stored for " + r.PathValue("registry")})
	})
	mux.HandleFunc("POST /api/v1/server/rotate-key", func(w http.ResponseWriter, r *http.Request) {
		if a.rotateErr != nil {
			respondError(w, 409, *a.rotateErr)
			return
		}
		respond(w, 200, a.rotation)
	})
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		if a.old {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestRegistryLoginReadsThePasswordFromStdinAndNeverEchoesIt(t *testing.T) {
	a := newRegistryAgent(t)

	a.stdin = "ghp_token\n"
	out, errOut, err := a.run(t.TempDir(), "registry", "login", "ghcr.io", "--username", "octocat")
	if err != nil {
		t.Fatalf("registry login: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Logged in to ghcr.io as octocat", "pulls images from ghcr.io with this credential"})
	if a.setBodies["ghcr.io"] != `{"username":"octocat","password":"ghp_token"}` {
		t.Errorf("unexpected request body: %s", a.setBodies["ghcr.io"])
	}
	if strings.Contains(out+errOut, "ghp_token") {
		t.Errorf("the password was echoed:\n%s%s", out, errOut)
	}

	// Docker's flag and Docker's names for its own registry are understood.
	a.stdin = "hub_token"
	if _, _, err := a.run(t.TempDir(), "registry", "login", "Index.Docker.IO", "-u", "company", "--password-stdin"); err != nil {
		t.Fatalf("--password-stdin: %v", err)
	}
	if a.setBodies["docker.io"] != `{"username":"company","password":"hub_token"}` {
		t.Errorf("Docker Hub must be sent as docker.io: %v", a.setBodies)
	}

	a.stdin = "token"
	if _, _, err := a.run(t.TempDir(), "registry", "login", "registry.example.com:5000", "-u", "ci"); err != nil || a.setBodies["registry.example.com:5000"] == "" {
		t.Errorf("a registry with a port: %v, bodies = %v", err, a.setBodies)
	}
}

func TestRegistryLoginRefusesBadInputBeforeContactingTheAgent(t *testing.T) {
	a := newRegistryAgent(t)
	for _, tt := range []struct {
		args  []string
		stdin string
		want  string
	}{
		{[]string{"registry", "login", "https://ghcr.io", "-u", "octocat"}, "x", "use its hostname"},
		{[]string{"registry", "login", "ghcr.io/company", "-u", "octocat"}, "x", "use its hostname"},
		{[]string{"registry", "login", "ghcr.io"}, "x", "--username"},
		{[]string{"registry", "login", "ghcr.io", "-u", "octocat"}, "", "no password on standard input"},
		{[]string{"registry", "login", "ghcr.io", "-u", "octocat"}, "\n", "the password is empty"},
		{[]string{"registry", "login", "ghcr.io", "-u", "a:b"}, "x", "colon"},
		{[]string{"registry", "login", "ghcr.io", "-u", "octocat"}, strings.Repeat("x", api.MaxRegistryPasswordBytes+1), "too large"},
		{[]string{"registry", "login", "-u", "octocat"}, "x", "arg"},
		{[]string{"registry", "login", "ghcr.io", "-u", "octocat", "ghp_token"}, "x", "arg"},
		{[]string{"registry", "logout", "user@ghcr.io"}, "", "use its hostname"},
	} {
		a.stdin = tt.stdin
		_, _, err := a.run(t.TempDir(), tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
	}
	if len(a.requests) != 0 {
		t.Errorf("nothing may be sent for bad input, saw %v", a.requests)
	}
}

func TestARefusedRegistryLoginSaysThatNothingWasStoredAndWhatToCheck(t *testing.T) {
	a := newRegistryAgent(t)
	a.stdin = "typo"
	a.setError = &api.Error{Code: api.CodeRegistryLoginFailed, Message: "ghcr.io refused the login: denied: denied",
		Details: map[string]any{"registry": "ghcr.io", "refused": true}}
	out, _, err := a.run(t.TempDir(), "registry", "login", "ghcr.io", "-u", "octocat")
	if err == nil || strings.Contains(out, "Logged in") {
		t.Fatalf("a refused login must fail: %v\n%s", err, out)
	}
	if got := Render(err); !strings.HasPrefix(got, "Error: ghcr.io refused the login: denied: denied\n\nNothing was stored. Check the username and the token") {
		t.Errorf("unexpected rendering: %s", got)
	}

	a.setError = &api.Error{Code: api.CodeRegistryLoginFailed, Message: "could not log in to registry.example.com: no such host",
		Details: map[string]any{"registry": "registry.example.com", "refused": false}}
	_, _, err = a.run(t.TempDir(), "registry", "login", "registry.example.com", "-u", "ci")
	if got := Render(err); !strings.Contains(got, "Check the registry's name") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestRegistryListShowsRegistriesUsernamesAndAges(t *testing.T) {
	a := newRegistryAgent(t)
	out, _, err := a.run(t.TempDir(), "registry", "ls")
	if err != nil || !strings.Contains(out, "No registry credentials on the server") || !strings.Contains(out, "shipwick registry login ghcr.io --username") {
		t.Errorf("empty list: %v\n%s", err, out)
	}

	a.registries = []api.Registry{
		{Registry: "docker.io", Username: "company", CreatedAt: fixedNow.Add(-30 * 24 * time.Hour), UpdatedAt: fixedNow.Add(-2 * time.Hour)},
		{Registry: "ghcr.io", Username: "octocat", CreatedAt: fixedNow.Add(-time.Minute), UpdatedAt: fixedNow.Add(-time.Minute)},
	}
	out, _, err = a.run(t.TempDir(), "registry", "list")
	if err != nil {
		t.Fatalf("registry list: %v", err)
	}
	assertInOrder(t, out, []string{"REGISTRY", "USERNAME", "UPDATED", "docker.io", "company", "2h ago", "ghcr.io", "octocat", "1m ago"})
}

func TestRegistryLogout(t *testing.T) {
	a := newRegistryAgent(t)
	a.registries = []api.Registry{{Registry: "ghcr.io", Username: "octocat"}}

	out, _, err := a.run(t.TempDir(), "registry", "logout", "GHCR.io")
	if err != nil || !strings.Contains(out, "Logged out of ghcr.io") {
		t.Errorf("logout: %v\n%s", err, out)
	}
	if len(a.removed) != 1 || a.removed[0] != "ghcr.io" {
		t.Errorf("removed = %v", a.removed)
	}

	_, _, err = a.run(t.TempDir(), "registry", "logout", "quay.io")
	if got := Render(err); !strings.Contains(got, "no credential is stored for quay.io") || !strings.Contains(got, "shipwick registry ls") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestServerRotateKeyWithTheKeyInItsFileSaysWhereItIsAndToBackItUp(t *testing.T) {
	a := newRegistryAgent(t)
	a.rotation = api.KeyRotation{Values: 3, Deployments: 1, KeySource: api.KeySourceFile, KeyFile: "/var/lib/shipwick/encryption.key"}
	out, _, err := a.run(t.TempDir(), "server", "rotate-key")
	if err != nil {
		t.Fatalf("rotate-key: %v", err)
	}
	assertInOrder(t, out, []string{
		"Rotated the encryption key: 3 stored values and 1 deployment re-encrypted",
		"/var/lib/shipwick/encryption.key", "Back it up",
	})
	if strings.Contains(out, "SHIPWICK_ENCRYPTION_KEY") {
		t.Errorf("nothing is to be done to the environment here:\n%s", out)
	}
}

func TestServerRotateKeyWithTheKeyInTheEnvironmentShowsTheNewKeyAndWhatToDoWithIt(t *testing.T) {
	a := newRegistryAgent(t)
	key := strings.Repeat("ab", 32)
	a.rotation = api.KeyRotation{Values: 1, Deployments: 12, KeySource: api.KeySourceEnvironment, Key: key, KeyFile: "/var/lib/shipwick/encryption.key.new"}
	out, _, err := a.run(t.TempDir(), "server", "rotate-key")
	if err != nil {
		t.Fatalf("rotate-key: %v", err)
	}
	assertInOrder(t, out, []string{
		"Rotated the encryption key: 1 stored value and 12 deployments re-encrypted",
		key,
		"/opt/shipwick/.env",
		"SHIPWICK_ENCRYPTION_KEY=" + key,
		"refuses to start",
		"/var/lib/shipwick/encryption.key.new",
	})

	a.rotateErr = &api.Error{Code: api.CodeKeyRotationPending, Message: "the key was already rotated since the agent started",
		Details: map[string]any{"key_file": "/var/lib/shipwick/encryption.key.new"}}
	_, _, err = a.run(t.TempDir(), "server", "rotate-key")
	got := Render(err)
	for _, want := range []string{"already rotated", "/var/lib/shipwick/encryption.key.new", "/opt/shipwick/.env", "SHIPWICK_ENCRYPTION_KEY", "restart the agent"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendering must mention %q:\n%s", want, got)
		}
	}
}

func TestAnOlderAgentIsNamedAsTheReasonInWords(t *testing.T) {
	a := newRegistryAgent(t)
	a.old = true

	_, _, err := a.run(t.TempDir(), "server", "rotate-key")
	if got := Render(err); !strings.Contains(got, "the agent is older than this shipwick and cannot rotate its key") {
		t.Errorf("rotate-key: %s", got)
	}
	a.stdin = "token"
	_, _, err = a.run(t.TempDir(), "registry", "login", "ghcr.io", "-u", "octocat")
	if got := Render(err); !strings.Contains(got, "the agent is older than this shipwick and keeps no registry credentials") {
		t.Errorf("registry login: %s", got)
	}
	_, _, err = a.run(t.TempDir(), "registry", "ls")
	if got := Render(err); !strings.Contains(got, "older than this shipwick") {
		t.Errorf("registry ls: %s", got)
	}
}
