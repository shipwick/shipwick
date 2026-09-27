package cliconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func oneServer(url, token string) Config {
	var cfg Config
	cfg.Set(DefaultContext, Context{URL: url, Token: token})
	return cfg
}

func TestResolvePrecedence(t *testing.T) {
	file := oneServer("https://saved.example.com", "saved-token")
	saved := Target{URL: "https://saved.example.com", Token: "saved-token", Context: DefaultContext}
	tests := []struct {
		name    string
		flagURL string
		env     map[string]string
		file    Config
		want    Target
	}{
		{"nothing configured", "", nil, Config{}, Target{URL: DefaultURL}},
		{"config file", "", nil, file, saved},
		{"file token with default url", "", nil, oneServer("", "t"), Target{URL: DefaultURL, Token: "t", Context: DefaultContext}},
		{"env beats file", "", map[string]string{EnvURL: "https://env.example.com", EnvToken: "env-token"}, file,
			Target{URL: "https://env.example.com", Token: "env-token"}},
		{"flag beats env", "https://flag.example.com", map[string]string{EnvURL: "https://env.example.com", EnvToken: "env-token"}, file,
			Target{URL: "https://flag.example.com", Token: "env-token"}},
		{"env token with saved url", "", map[string]string{EnvToken: "env-token"}, file,
			Target{URL: "https://saved.example.com", Token: "env-token", Context: DefaultContext}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.flagURL, "", env(tt.env), tt.file)
			if err != nil || got != tt.want {
				t.Errorf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestResolveNeverSendsSavedTokenToAnotherAgent(t *testing.T) {
	file := oneServer("https://saved.example.com", "saved-token")

	if got, _ := Resolve("https://other.example.com", "", env(nil), file); got.Token != "" || got.Context != "" {
		t.Errorf("--url to another agent must not carry the saved token, got %+v", got)
	}
	if got, _ := Resolve("", "", env(map[string]string{EnvURL: "https://other.example.com"}), file); got.Token != "" {
		t.Errorf("%s to another agent must not carry the saved token, got %q", EnvURL, got.Token)
	}
	if got, _ := Resolve("https://saved.example.com", "", env(nil), file); got.Token != "saved-token" {
		t.Error("naming the saved agent explicitly should still use its token")
	}
}

func TestResolveSelectsAContext(t *testing.T) {
	var file Config
	file.Set("prod", Context{URL: "https://prod.example.com", Token: "prod-token"})
	file.Set("staging", Context{URL: "https://staging.example.com", Token: "staging-token"})

	got, err := Resolve("", "", env(nil), file)
	if err != nil || got != (Target{URL: "https://staging.example.com", Token: "staging-token", Context: "staging"}) {
		t.Errorf("the current context should win by default, got %+v, %v", got, err)
	}
	got, _ = Resolve("", "prod", env(nil), file)
	if got.URL != "https://prod.example.com" || got.Token != "prod-token" || got.Context != "prod" {
		t.Errorf("--context prod should select prod, got %+v", got)
	}
	got, _ = Resolve("", "", env(map[string]string{EnvContext: "prod"}), file)
	if got.Context != "prod" {
		t.Errorf("%s should select the context, got %+v", EnvContext, got)
	}
	got, _ = Resolve("", "prod", env(map[string]string{EnvContext: "staging"}), file)
	if got.Context != "prod" {
		t.Errorf("--context should beat %s, got %+v", EnvContext, got)
	}
	got, _ = Resolve("", "prod", env(map[string]string{EnvURL: "https://other.example.com"}), file)
	if got.Token != "" || got.Context != "" {
		t.Errorf("%s pointing elsewhere must not carry prod's token, got %+v", EnvURL, got)
	}

	_, err = Resolve("", "ghost", env(nil), file)
	if !errors.Is(err, ErrUnknownContext) || !strings.Contains(err.Error(), "shipwick context ls") {
		t.Errorf("an unknown context must be refused with a hint, got %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	var want Config
	want.Set("prod", Context{URL: "https://agent.example.com", Token: "swk_secret"})
	want.Set("staging", Context{URL: "http://127.0.0.1:9000", Token: "swk_other"})

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil || got.Current != "staging" || len(got.Contexts) != 2 || got.Contexts["prod"] != want.Contexts["prod"] {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, want)
	}
	data, _ := os.ReadFile(path)
	for _, frag := range []string{"current: staging", "contexts:", "prod:", "url: https://agent.example.com"} {
		if !strings.Contains(string(data), frag) {
			t.Errorf("the file should hold %q:\n%s", frag, data)
		}
	}

	if runtime.GOOS != "windows" { // Windows has ACLs, not mode bits
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config file mode = %o, want 600", perm)
		}
	}

	// Overwriting must work and leave no temp files behind.
	if err := Save(path, oneServer("https://new.example.com", "t2")); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("expected only config.yaml in the directory, got %d entries", len(entries))
	}
}

func TestLoadMigratesASingleServerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("url: https://agent.example.com\ntoken: swk_secret\n"), 0o600)

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := oneServer("https://agent.example.com", "swk_secret")
	if got.Current != DefaultContext || got.Contexts[DefaultContext] != want.Contexts[DefaultContext] {
		t.Errorf("Load = %+v, want %+v", got, want)
	}

	if err := Save(path, got); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "contexts:") || strings.HasPrefix(string(data), "url:") {
		t.Errorf("Save should write the current format:\n%s", data)
	}
	again, err := Load(path)
	if err != nil || again.Contexts[DefaultContext] != want.Contexts[DefaultContext] {
		t.Errorf("the migrated file should load back, got %+v, %v", again, err)
	}
}

func TestRemoveClearsCurrent(t *testing.T) {
	var cfg Config
	cfg.Set("prod", Context{URL: "https://prod.example.com"})
	cfg.Set("staging", Context{URL: "https://staging.example.com"})
	cfg.Remove("prod")
	if cfg.Current != "staging" || len(cfg.Contexts) != 1 {
		t.Errorf("removing another context must leave the current one alone: %+v", cfg)
	}
	cfg.Remove("staging")
	if cfg.Current != "" {
		t.Errorf("removing the current context should leave none current: %+v", cfg)
	}
}

func TestLoadMissingFileIsEmptyConfig(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || got.Current != "" || len(got.Contexts) != 0 {
		t.Errorf("Load = %+v, %v", got, err)
	}
}

func TestLoadMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("url: [unclosed"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("expected an error naming the file, got %v", err)
	}
}

func TestPath(t *testing.T) {
	if p, _ := Path(env(map[string]string{EnvConfig: "/custom/cfg.yaml"})); p != "/custom/cfg.yaml" {
		t.Errorf("Path = %q", p)
	}
	p, err := Path(env(nil))
	if err != nil || !strings.HasSuffix(filepath.ToSlash(p), "shipwick/config.yaml") {
		t.Errorf("Path = %q, %v", p, err)
	}
}
