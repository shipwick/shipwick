package config

import (
	"strings"
	"testing"
)

func TestLoadWebhookSettings(t *testing.T) {
	cfg, err := Load(env(map[string]string{EnvWebhookURL: " https://hooks.slack.com/services/T0/B0/secret ", EnvWebhookSecret: "s3cret"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WebhookURL != "https://hooks.slack.com/services/T0/B0/secret" || cfg.WebhookSecret != "s3cret" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg, _ := Load(env(nil)); cfg.WebhookURL != "" || cfg.WebhookSecret != "" {
		t.Error("notifications must be opt-in")
	}
	if cfg, err := Load(env(map[string]string{EnvWebhookURL: "http://127.0.0.1:8080/hook"})); err != nil || cfg.WebhookURL == "" {
		t.Errorf("plain http to the machine itself should be accepted: %v", err)
	}

	bad := map[string]string{
		"http to the internet": "http://hooks.example.com/services/T0/B0/hunter2",
		"no scheme":            "hooks.example.com/T0/B0/hunter2",
		"not a URL":            "://",
	}
	for name, u := range bad {
		_, err := Load(env(map[string]string{EnvWebhookURL: u}))
		if err == nil {
			t.Errorf("%s: %q should be rejected", name, u)
			continue
		}
		if strings.Contains(err.Error(), "T0/B0") || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error repeats the URL, which is a credential: %v", name, err)
		}
		if !strings.Contains(err.Error(), EnvWebhookURL) {
			t.Errorf("%s: the error should name the variable: %v", name, err)
		}
	}
	if _, err := Load(env(map[string]string{EnvWebhookSecret: "s3cret"})); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("a secret without a URL is a mistake worth reporting, without echoing the secret: %v", err)
	}
}
