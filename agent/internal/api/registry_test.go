package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestRegistryLifecycle(t *testing.T) {
	f := newFixture(t)

	status, body := f.do("GET", "/api/v1/registries", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Fatalf("empty list: status = %d, body = %s", status, body)
	}

	status, body = f.do("PUT", "/api/v1/registries/ghcr.io", `{"username": "octocat", "password": "ghp_first"}`)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("login: status = %d, body = %s", status, body)
	}
	// Docker Hub's other names are stored as docker.io; case does not matter.
	if status, body = f.do("PUT", "/api/v1/registries/Index.Docker.IO", `{"username": "company", "password": "hub_token"}`); status != http.StatusNoContent {
		t.Fatalf("login to Docker Hub: status = %d, body = %s", status, body)
	}
	if status, _ = f.do("PUT", "/api/v1/registries/ghcr.io", `{"username": "ci-bot", "password": "ghp_second"}`); status != http.StatusNoContent {
		t.Fatalf("replace: status = %d", status)
	}
	if logins := f.rt.Logins(); len(logins) != 3 || logins[2] != (docker.RegistryAuth{Registry: "ghcr.io", Username: "ci-bot", Password: "ghp_second"}) {
		t.Errorf("every credential must be checked against its registry: %+v", logins)
	}

	_, body = f.do("GET", "/api/v1/registries", "")
	list := decode[[]api.Registry](t, body)
	if len(list) != 2 || list[0].Registry != "docker.io" || list[1].Registry != "ghcr.io" || list[1].Username != "ci-bot" ||
		list[1].CreatedAt.IsZero() || list[1].UpdatedAt.Before(list[1].CreatedAt) {
		t.Errorf("list = %+v", list)
	}
	for _, secret := range []string{"ghp_", "hub_token", "password"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the list carries %q: %s", secret, body)
		}
		if secret != "password" && strings.Contains(f.logs.String(), secret) {
			t.Errorf("%q reached the log:\n%s", secret, f.logs.String())
		}
	}
	if !strings.Contains(f.logs.String(), `msg="registry credential stored" registry=ghcr.io by=root`) {
		t.Errorf("the log must say who stored the credential:\n%s", f.logs.String())
	}

	// The stored credential is what a deployment pulls with.
	deployAndWait(t, f, "name: my-api\nimage: ghcr.io/company/api:1\n")
	pulls := f.rt.Pulls()
	if last := pulls[len(pulls)-1]; last.Auth == nil || *last.Auth != (docker.RegistryAuth{Registry: "ghcr.io", Username: "ci-bot", Password: "ghp_second"}) {
		t.Errorf("pulled with %+v", last.Auth)
	}

	if status, body = f.do("DELETE", "/api/v1/registries/GHCR.io", ""); status != http.StatusNoContent {
		t.Fatalf("logout: status = %d, body = %s", status, body)
	}
	status, body = f.do("DELETE", "/api/v1/registries/ghcr.io", "")
	if e := decodeError(t, body); status != http.StatusNotFound || e.Code != api.CodeNotFound || e.Message != "no credential is stored for ghcr.io" {
		t.Errorf("logout again: status = %d, error = %+v", status, e)
	}
}

// deployAndWait deploys a deploy.yaml for my-api and returns the finished
// deployment.
func deployAndWait(t *testing.T, f *fixture, config string) api.Deployment {
	t.Helper()
	status, resp := f.do("POST", "/api/v1/applications/my-api/deploy", config)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, resp)
	}
	f.engine.Wait()
	_, resp = f.do("GET", "/api/v1/deployments/"+strconv.FormatInt(decode[api.Deployment](t, resp).ID, 10), "")
	return decode[api.Deployment](t, resp)
}

func TestARefusedRegistryLoginIsAnswered400WithTheRegistrysMessageAndStoresNothing(t *testing.T) {
	f := newFixture(t)
	f.rt.RefuseLogin("ghcr.io", &docker.LoginError{Refused: true, Message: "denied: denied"})
	f.rt.RefuseLogin("registry.example.com", &docker.LoginError{Message: "dial tcp: lookup registry.example.com: no such host"})

	status, body := f.do("PUT", "/api/v1/registries/ghcr.io", `{"username": "octocat", "password": "ghp_typo"}`)
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeRegistryLoginFailed || e.Message != "ghcr.io refused the login: denied: denied" {
		t.Errorf("refused: status = %d, error = %+v", status, e)
	}
	if e.Details["registry"] != "ghcr.io" || e.Details["refused"] != true {
		t.Errorf("details = %v", e.Details)
	}

	status, body = f.do("PUT", "/api/v1/registries/registry.example.com", `{"username": "ci", "password": "token"}`)
	e = decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeRegistryLoginFailed || e.Details["refused"] != false ||
		!strings.HasPrefix(e.Message, "could not log in to registry.example.com: ") {
		t.Errorf("unreachable: status = %d, error = %+v", status, e)
	}

	_, body = f.do("GET", "/api/v1/registries", "")
	if strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Errorf("a refused credential was stored: %s", body)
	}
	if strings.Contains(f.logs.String(), "ghp_typo") {
		t.Errorf("the password reached the log:\n%s", f.logs.String())
	}
}

func TestRegistryEndpointsValidateBeforeAskingTheRegistry(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/registries/ghcr.io:port", `{"username": "u", "password": "p"}`},
		{"PUT", "/api/v1/registries/user@ghcr.io", `{"username": "u", "password": "p"}`},
		{"PUT", "/api/v1/registries/" + strings.Repeat("a", 256), `{"username": "u", "password": "p"}`},
		{"PUT", "/api/v1/registries/ghcr.io", `{"username": "", "password": "p"}`},
		{"PUT", "/api/v1/registries/ghcr.io", `{"username": "u"}`},
		{"PUT", "/api/v1/registries/ghcr.io", `{"username": "a:b", "password": "p"}`},
		{"PUT", "/api/v1/registries/ghcr.io", `{"username": "u", "password": "p", "registry": "ghcr.io"}`},
		{"PUT", "/api/v1/registries/ghcr.io", `not json`},
		{"PUT", "/api/v1/registries/ghcr.io", `{"username": "u", "password": "` + strings.Repeat("p", api.MaxRegistryPasswordBytes+1) + `"}`},
		{"DELETE", "/api/v1/registries/user@ghcr.io", ""},
	} {
		status, body := f.do(tt.method, tt.path, tt.body)
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("%s %.60s %.40s: status = %d, error = %+v", tt.method, tt.path, tt.body, status, e)
		}
	}
	if logins := f.rt.Logins(); len(logins) != 0 {
		t.Errorf("an invalid request reached the registry: %+v", logins)
	}
}

func TestRegistryLimit(t *testing.T) {
	f := newFixture(t)
	for i := range api.MaxRegistries {
		if status, body := f.do("PUT", "/api/v1/registries/r"+strconv.Itoa(i)+".example.com", `{"username": "u", "password": "p"}`); status != http.StatusNoContent {
			t.Fatalf("registry %d: status = %d, body = %s", i, status, body)
		}
	}
	status, body := f.do("PUT", "/api/v1/registries/one-more.example.com", `{"username": "u", "password": "p"}`)
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "at most 50") {
		t.Errorf("over the limit: status = %d, error = %+v", status, e)
	}
}

func TestRegistriesAndKeyRotationByRole(t *testing.T) {
	f := newFixture(t)
	read := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token
	admin := "Bearer " + f.createToken("ops", api.RoleAdmin).Token

	for _, auth := range []string{read, deployer} {
		if status, _ := f.doWithAuth("GET", "/api/v1/registries", "", auth); status != http.StatusOK {
			t.Errorf("listing registries needs read only: status = %d", status)
		}
		for _, tt := range []struct{ method, path, body string }{
			{"PUT", "/api/v1/registries/ghcr.io", `{"username": "u", "password": "p"}`},
			{"DELETE", "/api/v1/registries/ghcr.io", ""},
			{"POST", "/api/v1/server/rotate-key", ""},
		} {
			status, body := f.doWithAuth(tt.method, tt.path, tt.body, auth)
			if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden {
				t.Errorf("%s %s: status = %d, error = %+v; want 403", tt.method, tt.path, status, e)
			}
		}
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/registries", "", ""); status != http.StatusUnauthorized {
		t.Errorf("without a token: status = %d", status)
	}
	if status, body := f.doWithAuth("PUT", "/api/v1/registries/ghcr.io", `{"username": "u", "password": "p"}`, admin); status != http.StatusNoContent {
		t.Errorf("admin login: status = %d, body = %s", status, body)
	}
	if status, body := f.doWithAuth("POST", "/api/v1/server/rotate-key", "", admin); status != http.StatusOK {
		t.Errorf("admin rotation: status = %d, body = %s", status, body)
	}
	if len(f.rt.Logins()) != 1 {
		t.Errorf("a forbidden login reached the registry: %+v", f.rt.Logins())
	}
}

// keyedFixture is an agent whose database is on disk, with the key in a file
// next to it or, with fromEnv, given the way the environment gives it.
func keyedFixture(t *testing.T, fromEnv bool) (f *fixture, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	key, _, err := config.ResolveEncryptionKey(config.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	keyFile = filepath.Join(dir, config.EncryptionKeyFile)
	if fromEnv {
		os.Remove(keyFile)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "shipwick.db"), store.Options{EncryptionKey: key, KeyFile: keyFile, KeyFromEnvironment: fromEnv})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet})
	logs := &bytes.Buffer{}
	apiServer := New(engine, st, sha256.Sum256([]byte(testToken)), slog.New(slog.NewTextHandler(logs, nil)))
	srv := httptest.NewServer(apiServer.Handler())
	t.Cleanup(func() {
		apiServer.Close()
		srv.Close()
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &fixture{t: t, srv: srv, engine: engine, rt: rt, api: apiServer, logs: logs, store: st}, keyFile
}

func TestRotateKeyReencryptsWhileTheAgentRunsAndLogsWhoAsked(t *testing.T) {
	f, keyFile := keyedFixture(t, false)
	before, _ := os.ReadFile(keyFile)

	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	f.do("PUT", "/api/v1/registries/ghcr.io", `{"username": "octocat", "password": "ghp_token"}`)
	first := deployAndWait(t, f, "name: my-api\nimage: ghcr.io/company/api:1\nenv:\n  TOKEN: t0ken\n  DB: ${DB_PASSWORD}\n")
	if first.Status != api.StatusActive {
		t.Fatalf("deploy: %+v", first)
	}

	ops := f.createToken("ops", api.RoleAdmin).Token
	status, body := f.doWithAuth("POST", "/api/v1/server/rotate-key", "", "Bearer "+ops)
	if status != http.StatusOK {
		t.Fatalf("rotate: status = %d, body = %s", status, body)
	}
	rotation := decode[api.KeyRotation](t, body)
	if rotation.Values != 2 || rotation.Deployments != 1 || rotation.KeySource != api.KeySourceFile || rotation.KeyFile != keyFile {
		t.Errorf("rotation = %+v", rotation)
	}
	after, _ := os.ReadFile(keyFile)
	if bytes.Equal(before, after) {
		t.Error("the key file was not replaced")
	}
	if rotation.Key != "" || strings.Contains(string(body), strings.TrimSpace(string(after))) {
		t.Errorf("a key the agent keeps in its file must not be in the response: %s", body)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, `msg="encryption key rotated" by=ops values=2 deployments=1`) {
		t.Errorf("the log must record the rotation and the token that asked:\n%s", logs)
	}
	if strings.Contains(logs, strings.TrimSpace(string(after))) || strings.Contains(logs, strings.TrimSpace(string(before))) {
		t.Error("a key reached the log")
	}

	// Nothing restarted, and everything sealed is still usable: the
	// redeploy reads the stored spec, the secret and the credential.
	if n := len(f.rt.Containers()); n != 1 {
		t.Fatalf("%d containers after the rotation, want the one that was running", n)
	}
	status, body = f.do("POST", "/api/v1/applications/my-api/redeploy", "")
	if status != http.StatusAccepted {
		t.Fatalf("redeploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	containers := f.rt.Containers()
	if len(containers) != 1 {
		t.Fatalf("containers = %+v", containers)
	}
	if env := f.rt.Spec(containers[0].ID).Env; env["TOKEN"] != "t0ken" || env["DB"] != "hunter2" {
		t.Errorf("env after rotation = %v", env)
	}
	pulls := f.rt.Pulls()
	if last := pulls[len(pulls)-1]; last.Auth == nil || last.Auth.Password != "ghp_token" {
		t.Errorf("the registry password did not survive the rotation: %+v", last.Auth)
	}
}

func TestRotateKeyWithTheKeyInTheEnvironmentShowsItOnceAndRefusesASecondRotation(t *testing.T) {
	f, keyFile := keyedFixture(t, true)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)

	status, body := f.do("POST", "/api/v1/server/rotate-key", "")
	if status != http.StatusOK {
		t.Fatalf("rotate: status = %d, body = %s", status, body)
	}
	rotation := decode[api.KeyRotation](t, body)
	pending := keyFile + ".new"
	if rotation.KeySource != api.KeySourceEnvironment || len(rotation.Key) != 2*config.EncryptionKeySize || rotation.KeyFile != pending || rotation.Values != 1 {
		t.Fatalf("rotation = %+v", rotation)
	}
	if kept, _ := os.ReadFile(pending); strings.TrimSpace(string(kept)) != rotation.Key {
		t.Error("the agent must keep the key it returned until it has been started with it")
	}
	if strings.Contains(f.logs.String(), rotation.Key) {
		t.Error("the key reached the log")
	}

	status, body = f.do("POST", "/api/v1/server/rotate-key", "")
	e := decodeError(t, body)
	if status != http.StatusConflict || e.Code != api.CodeKeyRotationPending || e.Details["key_file"] != pending {
		t.Errorf("second rotation: status = %d, error = %+v", status, e)
	}
	if strings.Contains(string(body), rotation.Key) {
		t.Error("the key is shown once")
	}
}
