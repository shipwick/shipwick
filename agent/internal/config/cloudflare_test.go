package config

import (
	"strings"
	"testing"
)

func TestLoadCloudflareToken(t *testing.T) {
	const token = "0123456789abcdefghijklmnopqrstuvwxyz_-AB"
	withProxy := func(value string) map[string]string {
		return map[string]string{EnvCloudflareToken: value, EnvCaddyAdmin: "unix//run/caddy/admin.sock"}
	}

	cfg, err := Load(env(withProxy(" " + token + "\n")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CloudflareToken != token {
		t.Errorf("CloudflareToken = %q, want the value without the whitespace around it", cfg.CloudflareToken)
	}
	if cfg, err := Load(env(withProxy("cfut_" + token))); err != nil || cfg.CloudflareToken == "" {
		t.Errorf("a token in Cloudflare's prefixed form should be accepted: %v", err)
	}
	if cfg, _ := Load(env(nil)); cfg.CloudflareToken != "" {
		t.Error("the DNS challenge must be opt-in")
	}

	bad := map[string]string{
		"too short":       "hunter2hunter2",
		"a placeholder":   "{env.CF_TOKEN_hunter2_0123456789abcdefghij}",
		"quoted":          `"` + token + `hunter2"`,
		"with whitespace": token[:20] + " hunter2 " + token[20:],
	}
	for name, value := range bad {
		_, err := Load(env(withProxy(value)))
		if err == nil {
			t.Errorf("%s: should be rejected", name)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error repeats the value, which is a credential: %v", name, err)
		}
		if !strings.Contains(err.Error(), EnvCloudflareToken) {
			t.Errorf("%s: the error should name the variable: %v", name, err)
		}
	}

	_, err = Load(env(map[string]string{EnvCloudflareToken: token}))
	if err == nil || !strings.Contains(err.Error(), EnvCaddyAdmin) || strings.Contains(err.Error(), token) {
		t.Errorf("a token without a proxy to use it is a mistake worth reporting, without echoing it: %v", err)
	}
}
