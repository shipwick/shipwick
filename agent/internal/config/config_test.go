package config

import (
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("ListenAddr = %q; the default must be loopback only", cfg.ListenAddr)
	}
	if cfg.Network != "shipwick" || cfg.LogFormat != "text" || cfg.LogLevel != slog.LevelInfo || cfg.DataDir == "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		EnvListenAddr: "0.0.0.0:9000",
		EnvDataDir:    "/data",
		EnvNetwork:    "custom",
		EnvLogLevel:   "debug",
		EnvLogFormat:  "JSON",
		EnvToken:      "a-sufficiently-long-token",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" || cfg.DataDir != "/data" || cfg.Network != "custom" ||
		cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "json" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.DatabasePath() != filepath.Join("/data", "shipwick.db") {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath())
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"short token": {EnvToken: "short"},
		"log level":   {EnvLogLevel: "chatty"},
		"log format":  {EnvLogFormat: "xml"},
	}
	for name, vars := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(vars))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "short") && name == "short token" {
				t.Errorf("error must not echo the token: %v", err)
			}
		})
	}
}

func TestResolveTokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	hash, generated, err := ResolveToken(Config{DataDir: dir, Token: "a-sufficiently-long-token"})
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if generated != "" {
		t.Error("no token should be generated when one is configured")
	}
	if hash != sha256.Sum256([]byte("a-sufficiently-long-token")) {
		t.Error("hash does not match the configured token")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("a configured token must not be persisted")
	}
}

func TestResolveTokenGeneratesOnceAndPersistsOnlyTheHash(t *testing.T) {
	cfg := Config{DataDir: filepath.Join(t.TempDir(), "data")}

	hash, generated, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if !strings.HasPrefix(generated, "shw_") || len(generated) != 4+64 {
		t.Errorf("unexpected generated token format (length %d)", len(generated))
	}
	if hash != sha256.Sum256([]byte(generated)) {
		t.Error("hash does not match the generated token")
	}

	onDisk, err := os.ReadFile(filepath.Join(cfg.DataDir, tokenHashFile))
	if err != nil {
		t.Fatalf("hash file: %v", err)
	}
	if strings.Contains(string(onDisk), generated) || strings.Contains(string(onDisk), strings.TrimPrefix(generated, "shw_")) {
		t.Error("the plaintext token must never be written to disk")
	}

	// Second start: same hash, and the token is not shown again.
	again, regenerated, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("second ResolveToken: %v", err)
	}
	if regenerated != "" || again != hash {
		t.Error("the persisted hash should be reused on later starts")
	}
}

func TestResolveTokenRejectsCorruptHashFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, tokenHashFile), []byte("not-hex"), 0o600)
	if _, _, err := ResolveToken(Config{DataDir: dir}); err == nil {
		t.Error("expected an error for a corrupt hash file")
	}
}

func TestLoadProxySettings(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		EnvCaddyAdmin:  "unix//run/caddy/admin.sock",
		EnvAgentDomain: " Agent.Example.com ",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CaddyAdmin != "unix//run/caddy/admin.sock" || cfg.AgentDomain != "agent.example.com" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	if cfg, _ := Load(env(nil)); cfg.CaddyAdmin != "" {
		t.Error("the proxy must be opt-in: a binary started with no configuration has no Caddy to talk to")
	}
	if _, err := Load(env(map[string]string{EnvAgentDomain: "agent.example.com"})); err == nil {
		t.Error("an agent domain without a proxy to serve it should be refused at startup")
	}
	for _, bad := range []string{"https://agent.example.com", "agent.example.com:443", "agent.example.com/api", "*.example.com"} {
		if _, err := Load(env(map[string]string{EnvCaddyAdmin: "unix//x/y.sock", EnvAgentDomain: bad})); err == nil {
			t.Errorf("agent domain %q should be rejected: it goes into the proxy configuration", bad)
		}
	}
}

func TestLoadDashboardSettings(t *testing.T) {
	base := map[string]string{EnvCaddyAdmin: "unix//run/caddy/admin.sock", EnvDashboardDomain: "Dash.Example.com"}
	cfg, err := Load(env(base))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DashboardDomain != "dash.example.com" || cfg.DashboardUpstream != "dashboard:3000" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	if cfg, err := Load(env(with(map[string]string{EnvDashboardUpstream: "10.0.0.5:8080"}))); err != nil || cfg.DashboardUpstream != "10.0.0.5:8080" {
		t.Errorf("an IP upstream should be accepted: %+v, %v", cfg, err)
	}

	bad := []map[string]string{
		{EnvDashboardDomain: "dash.example.com"}, // no proxy to serve it
		with(map[string]string{EnvAgentDomain: "dash.example.com"}),
		with(map[string]string{EnvDashboardUpstream: "dashboard"}),
		with(map[string]string{EnvDashboardUpstream: "dashboard:0"}),
		with(map[string]string{EnvDashboardUpstream: "http://dashboard:3000"}),
		with(map[string]string{EnvDashboardUpstream: `x:1"},{"dial":"evil:1`}),
		with(map[string]string{EnvDashboardDomain: "dash.example.com/admin"}),
	}
	for _, vars := range bad {
		if _, err := Load(env(vars)); err == nil {
			t.Errorf("expected an error for %v", vars)
		}
	}
}
