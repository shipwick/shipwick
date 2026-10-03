package config

import (
	"strings"
	"testing"
)

func TestLoadProxyTLSAddr(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxyTLSAddr != "caddy:443" {
		t.Errorf("default = %q, want the compose setup's caddy:443", cfg.ProxyTLSAddr)
	}
	cfg, err = Load(env(map[string]string{EnvProxyTLSAddr: " 127.0.0.1:8443 "}))
	if err != nil || cfg.ProxyTLSAddr != "127.0.0.1:8443" {
		t.Errorf("override = %q, %v", cfg.ProxyTLSAddr, err)
	}
	if _, err := Load(env(map[string]string{EnvProxyTLSAddr: "caddy"})); err == nil || !strings.Contains(err.Error(), EnvProxyTLSAddr) || !strings.Contains(err.Error(), "host:port") {
		t.Errorf("without a port: err = %v, want one that names the variable and the format", err)
	}
}
