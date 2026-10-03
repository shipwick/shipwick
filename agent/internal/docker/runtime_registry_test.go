package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

func TestImageRegistry(t *testing.T) {
	for _, tt := range []struct{ image, registry, name string }{
		{"nginx", "docker.io", "nginx"},
		{"nginx:1.27", "docker.io", "nginx"},
		{"company/api:1", "docker.io", "company/api"},
		{"docker.io/company/api:1", "docker.io", "company/api"},
		{"index.docker.io/company/api:1", "docker.io", "company/api"},
		{"ghcr.io/company/api:1.4.2", "ghcr.io", "ghcr.io/company/api"},
		{"ghcr.io/company/api@sha256:0000000000000000000000000000000000000000000000000000000000000000", "ghcr.io", "ghcr.io/company/api"},
		{"registry.example.com:5000/team/job:4", "registry.example.com:5000", "registry.example.com:5000/team/job"},
		{"localhost:5000/job", "localhost:5000", "localhost:5000/job"},
	} {
		registry, name, err := ImageRegistry(tt.image)
		if err != nil || registry != tt.registry || name != tt.name {
			t.Errorf("ImageRegistry(%q) = %q, %q, %v; want %q, %q", tt.image, registry, name, err, tt.registry, tt.name)
		}
	}
	if _, _, err := ImageRegistry("Not A Reference"); err == nil {
		t.Error("an invalid reference must be refused")
	}
}

func TestRegistryAuthEncodesWhatTheDaemonDecodes(t *testing.T) {
	encoded, err := RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"}.encode()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("not base64url: %v", err)
	}
	var got registry.AuthConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != (registry.AuthConfig{Username: "octocat", Password: "ghp_token", ServerAddress: "ghcr.io"}) {
		t.Errorf("decoded = %+v", got)
	}
}

// The messages are what Docker 29 with the containerd image store answered
// for a private or missing image, per registry; the daemon's status code for
// them ranged from 401 to 500.
func TestPullDeniedRecognisesTheRegistriesRefusals(t *testing.T) {
	for _, msg := range []string{
		"Error response from daemon: error from registry: denied\ndenied",
		"Error response from daemon: pull access denied for company/api, repository does not exist or may require 'docker login'",
		"Error response from daemon: authentication required - incorrect username or password",
		"Error response from daemon: error from registry: access forbidden",
		`Error response from daemon: unknown: failed to resolve reference "quay.io/company/api:1": unexpected status from HEAD request to https://quay.io/v2/company/api/manifests/1: 401 UNAUTHORIZED`,
		`Error response from daemon: Head "https://ghcr.io/v2/company/api/manifests/1": unauthorized`,
	} {
		if !pullDenied(errors.New(msg)) {
			t.Errorf("not recognised as a refusal: %s", msg)
		}
	}
	for _, msg := range []string{
		`Error response from daemon: failed to resolve reference "registry.example.invalid/a/b:1": failed to do request: Head "https://registry.example.invalid/v2/a/b/manifests/1": dial tcp: lookup registry.example.invalid: no such host`,
		"Error response from daemon: manifest for nginx:nope not found: manifest unknown: manifest unknown",
		"Error response from daemon: toomanyrequests: You have reached your pull rate limit",
		"write /var/lib/docker/tmp/GetImageBlob123: no space left on device",
	} {
		if pullDenied(errors.New(msg)) {
			t.Errorf("taken for a refusal: %s", msg)
		}
	}
}

func TestLoginNoiseLeavesTheRegistrysOwnMessage(t *testing.T) {
	for in, want := range map[string]string{
		`Error response from daemon: Get "https://ghcr.io/v2/": denied: denied`:                                                           "denied: denied",
		`Error response from daemon: Get "https://registry-1.docker.io/v2/": unauthorized: incorrect username or password`:                "unauthorized: incorrect username or password",
		`Error response from daemon: Get "https://registry.example.invalid/v2/": dial tcp: lookup registry.example.invalid: no such host`: "dial tcp: lookup registry.example.invalid: no such host",
		"login attempt failed": "login attempt failed",
	} {
		if got := loginNoise.ReplaceAllString(in, ""); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestLoginErrorTellsARefusalFromARegistryThatCouldNotBeAsked(t *testing.T) {
	for msg, refused := range map[string]bool{
		`Error response from daemon: Get "https://ghcr.io/v2/": denied: denied`:                                                           true,
		`Error response from daemon: Get "https://registry-1.docker.io/v2/": unauthorized: incorrect username or password`:                true,
		`Error response from daemon: login attempt to http://localhost:5055/v2/ failed with status: 401 Unauthorized`:                     true,
		`Error response from daemon: Get "https://registry.example.invalid/v2/": dial tcp: lookup registry.example.invalid: no such host`: false,
		`Error response from daemon: Get "https://localhost:5999/v2/": net/http: request canceled while waiting for connection`:           false,
	} {
		if got := loginError(errors.New(msg)); got.Refused != refused {
			t.Errorf("Refused = %v, want %v: %s", got.Refused, refused, msg)
		}
	}
}
