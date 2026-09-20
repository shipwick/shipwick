package docker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

func TestContainerName(t *testing.T) {
	if got := ContainerName("my-api", 7, 2); got != "shipwick_my-api_7_2" {
		t.Errorf("ContainerName = %q", got)
	}
}

func TestContainerLabels(t *testing.T) {
	labels := containerLabels("my-api", 42, 2)
	want := map[string]string{
		LabelManaged:    "true",
		LabelApp:        "my-api",
		LabelDeployment: "42",
		LabelReplica:    "2",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}

	var c Container
	c.applyLabels(labels)
	if c.App != "my-api" || c.DeploymentID != 42 || c.Replica != 2 {
		t.Errorf("applyLabels round trip failed: %+v", c)
	}
}

func writeDockerConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeAuth(t *testing.T, encoded string) registry.AuthConfig {
	t.Helper()
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("auth is not base64url: %v", err)
	}
	var cfg registry.AuthConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("auth is not JSON: %v", err)
	}
	return cfg
}

func TestRegistryAuth(t *testing.T) {
	ghcr := base64.StdEncoding.EncodeToString([]byte("octocat:ghp_token:with:colons"))
	hub := base64.StdEncoding.EncodeToString([]byte("moby:dock"))
	path := writeDockerConfig(t, `{
		"auths": {
			"ghcr.io": {"auth": "`+ghcr+`"},
			"https://index.docker.io/v1/": {"auth": "`+hub+`"},
			"helper.example.com": {}
		},
		"credsStore": "desktop"
	}`)

	t.Run("private registry", func(t *testing.T) {
		auth, err := registryAuth(path, "ghcr.io/company/my-api:1.4.2")
		if err != nil {
			t.Fatal(err)
		}
		cfg := decodeAuth(t, auth)
		if cfg.Username != "octocat" || cfg.Password != "ghp_token:with:colons" || cfg.ServerAddress != "ghcr.io" {
			t.Errorf("unexpected auth: %+v", cfg)
		}
	})

	t.Run("docker hub short name", func(t *testing.T) {
		auth, err := registryAuth(path, "nginx:1.27")
		if err != nil {
			t.Fatal(err)
		}
		if cfg := decodeAuth(t, auth); cfg.Username != "moby" {
			t.Errorf("unexpected auth: %+v", cfg)
		}
	})

	t.Run("no entry means anonymous", func(t *testing.T) {
		for _, image := range []string{"quay.io/org/app:1", "helper.example.com/app:1"} {
			auth, err := registryAuth(path, image)
			if err != nil || auth != "" {
				t.Errorf("registryAuth(%q) = %q, %v; want anonymous", image, auth, err)
			}
		}
	})

	t.Run("missing config file means anonymous", func(t *testing.T) {
		auth, err := registryAuth(filepath.Join(t.TempDir(), "absent.json"), "ghcr.io/a/b:1")
		if err != nil || auth != "" {
			t.Errorf("got %q, %v; want anonymous", auth, err)
		}
	})

	t.Run("malformed config is an error", func(t *testing.T) {
		if _, err := registryAuth(writeDockerConfig(t, "{not json"), "ghcr.io/a/b:1"); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestRegistryHost(t *testing.T) {
	tests := map[string]string{
		"ghcr.io":                     "ghcr.io",
		"https://ghcr.io":             "ghcr.io",
		"https://ghcr.io/":            "ghcr.io",
		"https://index.docker.io/v1/": "docker.io",
		"registry-1.docker.io":        "docker.io",
		"localhost:5000":              "localhost:5000",
		"registry.example.com/v2/":    "registry.example.com",
	}
	for key, want := range tests {
		if got := registryHost(key); got != want {
			t.Errorf("registryHost(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestParseLogLines(t *testing.T) {
	buf := bytes.NewBufferString(
		"2026-03-01T10:00:00.123456789Z listening on :8080\n" +
			"2026-03-01T10:00:01Z \n" +
			"no timestamp here\n")
	entries := parseLogLines("stdout", buf)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Message != "listening on :8080" || entries[0].Time.IsZero() || entries[0].Stream != "stdout" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
	if entries[1].Message != "" {
		t.Errorf("blank line message = %q", entries[1].Message)
	}
	if entries[2].Message != "no timestamp here" || !entries[2].Time.IsZero() {
		t.Errorf("line without timestamp should be kept verbatim: %+v", entries[2])
	}
}
