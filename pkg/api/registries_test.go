package api

import (
	"strings"
	"testing"
)

func TestNormalizeRegistry(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io":                   "ghcr.io",
		"GHCR.IO":                   "ghcr.io",
		"docker.io":                 "docker.io",
		"index.docker.io":           "docker.io",
		"registry-1.docker.io":      "docker.io",
		"Index.Docker.IO":           "docker.io",
		"registry.example.com:5000": "registry.example.com:5000",
		"localhost:5000":            "localhost:5000",
		"10.0.0.5:5000":             "10.0.0.5:5000",
		"registry":                  "registry",
	} {
		if got, err := NormalizeRegistry(in); err != nil || got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"",
		"https://ghcr.io",
		"ghcr.io/",
		"ghcr.io/company",
		"ghcr.io:",
		"ghcr.io:0",
		"ghcr.io:65536",
		"ghcr.io:05000",
		"ghcr.io:port",
		"-ghcr.io",
		"ghcr..io",
		"ghcr.io:5000:1",
		"user@ghcr.io",
		"ghcr io",
		strings.Repeat("a", 250) + ".io:5000",
	} {
		if got, err := NormalizeRegistry(in); err == nil {
			t.Errorf("NormalizeRegistry(%q) = %q, want an error", in, got)
		} else if in != "" && !strings.Contains(err.Error(), "ghcr.io") {
			t.Errorf("the error must show what a registry looks like: %v", err)
		}
	}
}

func TestValidateRegistryCredential(t *testing.T) {
	if err := ValidateRegistryCredential("octocat", "ghp_token"); err != nil {
		t.Errorf("a plain credential: %v", err)
	}
	// A service-account key is JSON over several lines.
	if err := ValidateRegistryCredential("_json_key", "{\n  \"type\": \"service_account\"\n}"); err != nil {
		t.Errorf("a JSON key: %v", err)
	}
	for _, tt := range []struct{ username, password, want string }{
		{"", "p", "username is empty"},
		{"a:b", "p", "colon"},
		{"a\nb", "p", "control"},
		{strings.Repeat("u", MaxRegistryUsernameLength+1), "p", "too long"},
		{"u", "", "password is empty"},
		{"u", "a\x00b", "NUL"},
		{"u", strings.Repeat("p", MaxRegistryPasswordBytes+1), "too large"},
	} {
		err := ValidateRegistryCredential(tt.username, tt.password)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidateRegistryCredential(%.10q, %.10q) = %v, want %q", tt.username, tt.password, err, tt.want)
		}
		if err != nil && tt.password != "" && len(tt.password) > 3 && strings.Contains(err.Error(), tt.password) {
			t.Error("the password is in the error")
		}
	}
}
