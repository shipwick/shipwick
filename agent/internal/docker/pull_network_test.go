package docker

import (
	"errors"
	"strings"
	"testing"
)

func TestPullAdviceIsAboutTheDaemonNotTheAgent(t *testing.T) {
	unreachable := []string{
		// Docker 29 with no route out.
		`failed to resolve reference "ghcr.io/acme/api:1": failed to do request: Head "https://ghcr.io/v2/acme/api/manifests/1": dial tcp: lookup ghcr.io on 192.168.65.7:53: dial udp 192.168.65.7:53: connect: network is unreachable`,
		`Get "https://ghcr.io/v2/": dial tcp 140.82.121.34:443: i/o timeout`,
		`Get "https://ghcr.io/v2/": proxyconnect tcp: dial tcp 10.0.0.9:3128: connect: connection refused`,
		`Get "https://ghcr.io/v2/": net/http: TLS handshake timeout`,
		`Get "https://ghcr.io/v2/": Proxy Authentication Required`,
	}
	for _, msg := range unreachable {
		advice := pullAdvice(errors.New(msg), "ghcr.io", false)
		if !strings.Contains(advice, "could not reach ghcr.io") || !strings.Contains(advice, `"proxies" in /etc/docker/daemon.json`) || !strings.Contains(advice, "build:") {
			t.Errorf("%s\n  advice = %q", msg, advice)
		}
		if withProxy := pullAdvice(errors.New(msg), "ghcr.io", true); !strings.Contains(withProxy, "through the proxy it is configured with") {
			t.Errorf("with a daemon proxy: %q", withProxy)
		}
	}

	untrusted := `Get "https://registry.example.internal/v2/": tls: failed to verify certificate: x509: certificate signed by unknown authority`
	if advice := pullAdvice(errors.New(untrusted), "registry.example.internal", true); !strings.Contains(advice, "/etc/docker/certs.d/registry.example.internal/ca.crt") {
		t.Errorf("advice = %q", advice)
	}

	for _, msg := range []string{
		`manifest for ghcr.io/acme/api:2 not found: manifest unknown`,
		`pull access denied for acme/api, repository does not exist or may require 'docker login'`,
		`no matching manifest for linux/arm64 in the manifest list entries`,
	} {
		if advice := pullAdvice(errors.New(msg), "ghcr.io", false); advice != "" {
			t.Errorf("%s\n  got advice about the network: %q", msg, advice)
		}
	}
}
