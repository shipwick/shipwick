package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/pkg/api"
)

// withContexts saves prod (pointing nowhere) and staging (the fake agent) and
// makes the CLI under test use that file and nothing from the environment.
func withContexts(t *testing.T, f *fakeAgent, current string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	var cfg cliconfig.Config
	cfg.Set("prod", cliconfig.Context{URL: "http://127.0.0.1:1", Token: "prod-token-0123456789"})
	cfg.Set("staging", cliconfig.Context{URL: f.srv.URL, Token: testToken})
	cfg.Current = current
	if err := cliconfig.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{cliconfig.EnvConfig: path, cliconfig.EnvURL: "", cliconfig.EnvToken: ""}
	return path
}

func TestContextLs(t *testing.T) {
	f := newFakeAgent(t)
	withContexts(t, f, "prod")

	out, _, err := f.run(t.TempDir(), "context", "ls")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"NAME", "URL", "* prod", "http://127.0.0.1:1", "  staging", f.srv.URL})
	if strings.Contains(out, "token") || strings.Contains(out, testToken) {
		t.Errorf("tokens must never be listed:\n%s", out)
	}
	if len(f.requests) != 0 {
		t.Errorf("listing contexts is offline, saw %v", f.requests)
	}

	f.env[cliconfig.EnvConfig] = filepath.Join(t.TempDir(), "absent.yaml")
	out, _, err = f.run(t.TempDir(), "context", "ls")
	if err != nil || !strings.Contains(out, "No saved servers") || !strings.Contains(out, "shipwick login") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
}

func TestContextUseAndCurrent(t *testing.T) {
	f := newFakeAgent(t)
	path := withContexts(t, f, "prod")

	out, _, err := f.run(t.TempDir(), "context", "current")
	if err != nil || strings.TrimSpace(out) != "prod" {
		t.Errorf("current = %q, %v", out, err)
	}

	out, _, err = f.run(t.TempDir(), "context", "use", "staging")
	if err != nil || !strings.Contains(out, "Switched to staging ("+f.srv.URL+")") {
		t.Errorf("use: %v\n%s", err, out)
	}
	saved, _ := cliconfig.Load(path)
	if saved.Current != "staging" || len(saved.Contexts) != 2 {
		t.Errorf("use should only change the current context: %+v", saved)
	}

	_, _, err = f.run(t.TempDir(), "context", "use", "ghost")
	if !errors.Is(err, cliconfig.ErrUnknownContext) || !strings.Contains(Render(err), "shipwick context ls") {
		t.Errorf("unknown context: %v", err)
	}
}

func TestContextRm(t *testing.T) {
	f := newFakeAgent(t)
	path := withContexts(t, f, "prod")

	if _, _, err := f.run(t.TempDir(), "context", "rm", "prod"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want a refusal pointing at --yes", err)
	}
	if saved, _ := cliconfig.Load(path); len(saved.Contexts) != 2 {
		t.Error("nothing may be removed without confirmation")
	}

	out, _, err := f.run(t.TempDir(), "context", "rm", "prod", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"Removed context prod", "shipwick context use"})
	saved, _ := cliconfig.Load(path)
	if _, ok := saved.Contexts["prod"]; ok || saved.Current != "" || len(saved.Contexts) != 1 {
		t.Errorf("rm should drop the context and leave none current: %+v", saved)
	}

	_, _, err = f.run(t.TempDir(), "context", "current")
	if err == nil || !strings.Contains(err.Error(), "shipwick context use") {
		t.Errorf("with no current context, `current` says how to pick one: %v", err)
	}
}

func TestContextFlagSelectsTheServer(t *testing.T) {
	f := newFakeAgent(t)
	withContexts(t, f, "prod")
	f.app.Application = api.Application{Name: "my-api", Status: api.AppHealthy}

	_, _, err := f.run(t.TempDir(), "ps")
	if got := Render(err); !strings.Contains(got, "cannot reach the Shipwick agent at http://127.0.0.1:1") || !strings.Contains(got, "--context") {
		t.Errorf("the current context (prod) points nowhere and the hint should mention --context:\n%s", got)
	}

	out, _, err := f.run(t.TempDir(), "ps", "--context", "staging")
	if err != nil || !strings.Contains(out, "my-api") {
		t.Errorf("--context staging should reach the fake agent: %v\n%s", err, out)
	}

	f.env[cliconfig.EnvContext] = "staging"
	if out, _, err := f.run(t.TempDir(), "ps"); err != nil || !strings.Contains(out, "my-api") {
		t.Errorf("%s should select the context: %v\n%s", cliconfig.EnvContext, err, out)
	}
	delete(f.env, cliconfig.EnvContext)

	_, _, err = f.run(t.TempDir(), "ps", "--context", "ghost")
	if !errors.Is(err, cliconfig.ErrUnknownContext) || len(f.requests) != 2 {
		t.Errorf("an unknown context is refused before any request: %v, %v", err, f.requests)
	}
}

func TestServerStatusNamesTheContextWhenThereAreSeveral(t *testing.T) {
	f := newFakeAgent(t)
	withContexts(t, f, "staging")
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1"}

	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{f.srv.URL, "context staging", "reachable", "vps-1"})

	// With a single saved server the name is noise.
	path := filepath.Join(t.TempDir(), "config.yaml")
	var one cliconfig.Config
	one.Set("staging", cliconfig.Context{URL: f.srv.URL, Token: testToken})
	cliconfig.Save(path, one)
	f.env[cliconfig.EnvConfig] = path
	out, _, _ = f.run(t.TempDir(), "server", "status")
	if strings.Contains(out, "context") {
		t.Errorf("one server needs no context name:\n%s", out)
	}
}

func TestLoginWithContext(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1"}
	path := filepath.Join(t.TempDir(), "config.yaml")
	f.env = map[string]string{cliconfig.EnvConfig: path, cliconfig.EnvURL: "", cliconfig.EnvToken: ""}
	f.stdin = testToken + "\n"

	out, _, err := f.run(t.TempDir(), "login", "--context", "staging", "--url", f.srv.URL, "--token-stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Logged in to " + f.srv.URL, "vps-1", "saved as context staging"})
	saved, _ := cliconfig.Load(path)
	if saved.Current != "staging" || saved.Contexts["staging"] != (cliconfig.Context{URL: f.srv.URL, Token: testToken}) {
		t.Errorf("unexpected config: %+v", saved)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "contexts:") {
		t.Errorf("login writes the current file format:\n%s", data)
	}

	// A second server is added; the first stays.
	out, _, err = f.run(t.TempDir(), "login", "--context", "prod", "--url", f.srv.URL, "--token-stdin")
	if err != nil {
		t.Fatalf("second login: %v\n%s", err, out)
	}
	saved, _ = cliconfig.Load(path)
	if saved.Current != "prod" || len(saved.Contexts) != 2 {
		t.Errorf("login --context prod should add a context and make it current: %+v", saved)
	}

	// Without --context, login refreshes the current one.
	f.env[cliconfig.EnvContext] = ""
	if _, _, err := f.run(t.TempDir(), "login", "--url", f.srv.URL, "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	saved, _ = cliconfig.Load(path)
	if len(saved.Contexts) != 2 || saved.Current != "prod" {
		t.Errorf("login without --context updates the current context: %+v", saved)
	}
}

func TestLoginWithoutContextsCreatesDefault(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1"}
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("url: "+f.srv.URL+"\ntoken: stale-token-0123456789\n"), 0o600)
	f.env = map[string]string{cliconfig.EnvConfig: path, cliconfig.EnvURL: "", cliconfig.EnvToken: ""}
	f.stdin = testToken

	out, _, err := f.run(t.TempDir(), "login", "--token-stdin")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	saved, _ := cliconfig.Load(path)
	if saved.Current != cliconfig.DefaultContext || saved.Contexts[cliconfig.DefaultContext].Token != testToken {
		t.Errorf("a single-server file is refreshed as the default context: %+v", saved)
	}
	if !strings.Contains(out, "saved as context default") {
		t.Errorf("unexpected output:\n%s", out)
	}
}
