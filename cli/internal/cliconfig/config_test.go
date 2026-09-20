package cliconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestResolvePrecedence(t *testing.T) {
	file := Config{URL: "https://saved.example.com", Token: "saved-token"}
	tests := []struct {
		name    string
		flagURL string
		env     map[string]string
		file    Config
		want    Config
	}{
		{"nothing configured", "", nil, Config{}, Config{URL: DefaultURL}},
		{"config file", "", nil, file, file},
		{"file token with default url", "", nil, Config{Token: "t"}, Config{URL: DefaultURL, Token: "t"}},
		{"env beats file", "", map[string]string{EnvURL: "https://env.example.com", EnvToken: "env-token"}, file,
			Config{URL: "https://env.example.com", Token: "env-token"}},
		{"flag beats env", "https://flag.example.com", map[string]string{EnvURL: "https://env.example.com", EnvToken: "env-token"}, file,
			Config{URL: "https://flag.example.com", Token: "env-token"}},
		{"env token with saved url", "", map[string]string{EnvToken: "env-token"}, file,
			Config{URL: "https://saved.example.com", Token: "env-token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.flagURL, env(tt.env), tt.file); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveNeverSendsSavedTokenToAnotherAgent(t *testing.T) {
	file := Config{URL: "https://saved.example.com", Token: "saved-token"}

	if got := Resolve("https://other.example.com", env(nil), file); got.Token != "" {
		t.Errorf("--url to another agent must not carry the saved token, got %q", got.Token)
	}
	if got := Resolve("", env(map[string]string{EnvURL: "https://other.example.com"}), file); got.Token != "" {
		t.Errorf("%s to another agent must not carry the saved token, got %q", EnvURL, got.Token)
	}
	if got := Resolve("https://saved.example.com", env(nil), file); got.Token != "saved-token" {
		t.Error("naming the saved agent explicitly should still use its token")
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	want := Config{URL: "https://agent.example.com", Token: "shw_secret"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil || got != want {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, want)
	}

	if runtime.GOOS != "windows" { // Windows has ACLs, not mode bits
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config file mode = %o, want 600", perm)
		}
	}

	// Overwriting must work and leave no temp files behind.
	if err := Save(path, Config{URL: "https://new.example.com", Token: "t2"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("expected only config.yaml in the directory, got %d entries", len(entries))
	}
}

func TestLoadMissingFileIsEmptyConfig(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || got != (Config{}) {
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
