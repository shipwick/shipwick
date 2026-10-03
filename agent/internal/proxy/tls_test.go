package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/cloudflare"
)

const testToken = "cfut_0123456789abcdefghijklmnopqrstuvwxyzABCD"

// tlsConfig mirrors what Caddy is told about certificates.
type tlsConfig struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []struct {
					Match []struct{ Host []string }
				}
				AutomaticHTTPS *struct {
					SkipCertificates []string `json:"skip_certificates"`
				} `json:"automatic_https"`
				TrustedProxies *struct {
					Source string
					Ranges []string
				} `json:"trusted_proxies"`
			}
		}
		TLS *struct {
			Automation *struct {
				Policies []struct {
					Subjects []string
					Issuers  []struct {
						Module     string
						Challenges struct {
							DNS struct {
								Provider struct {
									Name     string
									APIToken string `json:"api_token"`
								}
							}
						}
					}
				}
			}
			Certificates *struct {
				LoadPEM []struct {
					Certificate string
					Key         string
					Tags        []string
				} `json:"load_pem"`
			}
		}
	}
}

func buildTLS(t *testing.T, routes []Route, tls TLS) (tlsConfig, string, []byte) {
	t.Helper()
	raw, fingerprint, err := BuildWithTLS("unix//run/caddy/admin.sock", routes, tls)
	if err != nil {
		t.Fatalf("BuildWithTLS: %v", err)
	}
	var c tlsConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, raw)
	}
	return c, fingerprint, raw
}

func TestWithoutATokenOrCertificatesCaddyIsLeftToItsDefaults(t *testing.T) {
	c, fingerprint, raw := buildTLS(t, someRoutes, TLS{})
	server := c.Apps.HTTP.Servers["shipwick"]
	if c.Apps.TLS != nil || server.AutomaticHTTPS != nil || server.TrustedProxies != nil {
		t.Errorf("config says something about certificates nobody asked for:\n%s", raw)
	}
	if _, plain, _ := Build("unix//run/caddy/admin.sock", someRoutes); plain != fingerprint {
		t.Error("Build and BuildWithTLS disagree about a configuration without TLS settings; an upgrade would reload every proxy for nothing")
	}
}

func TestACloudflareTokenTurnsOnTheDNSChallengeForEveryHostname(t *testing.T) {
	c, _, _ := buildTLS(t, someRoutes, TLS{CloudflareToken: testToken})
	if c.Apps.TLS == nil || c.Apps.TLS.Automation == nil || len(c.Apps.TLS.Automation.Policies) != 1 {
		t.Fatalf("tls app = %+v, want one automation policy", c.Apps.TLS)
	}
	policy := c.Apps.TLS.Automation.Policies[0]
	if len(policy.Subjects) != 0 {
		t.Errorf("subjects = %v; the policy must apply to every hostname", policy.Subjects)
	}
	if len(policy.Issuers) != 1 || policy.Issuers[0].Module != "acme" {
		t.Fatalf("issuers = %+v", policy.Issuers)
	}
	if p := policy.Issuers[0].Challenges.DNS.Provider; p.Name != "cloudflare" || p.APIToken != testToken {
		t.Errorf("provider = %+v", p)
	}
}

func TestACloudflareTokenMakesCloudflareATrustedProxy(t *testing.T) {
	c, _, _ := buildTLS(t, someRoutes, TLS{CloudflareToken: testToken})
	trusted := c.Apps.HTTP.Servers["shipwick"].TrustedProxies
	if trusted == nil || trusted.Source != "static" {
		t.Fatalf("trusted_proxies = %+v", trusted)
	}
	if strings.Join(trusted.Ranges, ",") != strings.Join(cloudflare.Ranges(), ",") {
		t.Errorf("ranges = %v, want Cloudflare's and no others", trusted.Ranges)
	}
}

func TestTheFingerprintFollowsTheTokenWithoutCarryingIt(t *testing.T) {
	_, without, _ := buildTLS(t, someRoutes, TLS{})
	_, with, raw := buildTLS(t, someRoutes, TLS{CloudflareToken: testToken})
	_, other, _ := buildTLS(t, someRoutes, TLS{CloudflareToken: testToken + "x"})
	if with == without || with == other {
		t.Errorf("fingerprints %s, %s, %s: a token set or changed is a configuration to load", without, with, other)
	}
	if strings.Contains(with, testToken) || len(with) != 16 {
		t.Errorf("fingerprint = %q, want sixteen hex characters of a hash", with)
	}
	if n := strings.Count(string(raw), testToken); n != 1 {
		t.Errorf("the token appears %d times in the configuration, want once: in the provider", n)
	}
}

func TestSuppliedCertificatesAreLoadedAndTheirHostnamesNotManaged(t *testing.T) {
	routes := []Route{
		{Domain: "example.com", Aliases: []string{"api.example.com"}, Redirects: []string{"www.example.com"}, Upstreams: []string{"a:80"}},
		{Domain: "example.org", Upstreams: []string{"b:80"}},
	}
	tls := TLS{Certificates: []Certificate{
		{Hostname: "example.com", Subjects: []string{"example.com"}, CertPEM: "CHAIN-1", KeyPEM: "KEY-1"},
		{Hostname: "*.example.com", Subjects: []string{"*.example.com"}, CertPEM: "CHAIN-2", KeyPEM: "KEY-2"},
	}}
	c, _, _ := buildTLS(t, routes, tls)

	if c.Apps.TLS == nil || c.Apps.TLS.Certificates == nil || c.Apps.TLS.Automation != nil {
		t.Fatalf("tls app = %+v, want loaded certificates and no automation policy", c.Apps.TLS)
	}
	loaded := c.Apps.TLS.Certificates.LoadPEM
	if len(loaded) != 2 || loaded[0].Certificate != "CHAIN-2" || loaded[0].Key != "KEY-2" || strings.Join(loaded[0].Tags, ",") != "*.example.com" ||
		loaded[1].Certificate != "CHAIN-1" || loaded[1].Key != "KEY-1" {
		t.Errorf("load_pem = %+v, want both pairs in hostname order", loaded)
	}

	skip := c.Apps.HTTP.Servers["shipwick"].AutomaticHTTPS
	if skip == nil || strings.Join(skip.SkipCertificates, ",") != "api.example.com,example.com,www.example.com" {
		t.Errorf("automatic_https = %+v, want the three covered hostnames and not example.org", skip)
	}
}

func TestCertificateOrderDoesNotChangeTheFingerprint(t *testing.T) {
	a := Certificate{Hostname: "a.example.com", Subjects: []string{"a.example.com"}, CertPEM: "A", KeyPEM: "KA"}
	b := Certificate{Hostname: "b.example.com", Subjects: []string{"b.example.com"}, CertPEM: "B", KeyPEM: "KB"}
	_, one, _ := buildTLS(t, someRoutes, TLS{Certificates: []Certificate{a, b}})
	_, two, _ := buildTLS(t, someRoutes, TLS{Certificates: []Certificate{b, a}})
	if one != two {
		t.Error("the same certificates in another order produced another configuration")
	}
	b.KeyPEM = "KB2"
	if _, replaced, _ := buildTLS(t, someRoutes, TLS{Certificates: []Certificate{a, b}}); replaced == one {
		t.Error("a replaced key did not change the fingerprint; the proxy would keep the old one")
	}
}

func TestAnExactHostnameWinsOverAWildcard(t *testing.T) {
	// "*" sorts before every letter: by name alone the wildcard would come
	// first and answer for api.example.com too.
	routes := []Route{
		{Domain: "*.example.com", Upstreams: []string{"wild:80"}},
		{Domain: "api.example.com", Aliases: []string{"*.example.org"}, Upstreams: []string{"api:80"}},
		{Domain: "www.example.org", Upstreams: []string{"www:80"}},
	}
	c, _, _ := buildTLS(t, routes, TLS{})
	var order []string
	for _, r := range c.Apps.HTTP.Servers["shipwick"].Routes {
		if len(r.Match) > 0 {
			order = append(order, strings.Join(r.Match[0].Host, "+"))
		}
	}
	if got := strings.Join(order, " "); got != "api.example.com www.example.org *.example.com *.example.org" {
		t.Errorf("route order = %s; every exact hostname must come before every wildcard", got)
	}
}

func TestARejectedConfigurationDoesNotLeakTheToken(t *testing.T) {
	c, fake := newTestCaddy(t)
	c.UseCloudflare(testToken)
	// What Caddy's Cloudflare module answers to a token it does not like.
	fake.reject = `{"error":"loading module 'cloudflare': API token '` + testToken + `' appears invalid"}`

	err := c.Sync(context.Background(), someRoutes)
	if err == nil || !strings.Contains(err.Error(), "appears invalid") {
		t.Fatalf("err = %v, want Caddy's explanation", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("the error carries the token: %v", err)
	}
	if s := c.Status(); strings.Contains(s.Error, testToken) || !s.DNSChallenge {
		t.Errorf("Status = %+v; the error is shown by GET /server and must not carry the token", s)
	}
}

func TestSetCertificatesTakesEffectAtTheNextSync(t *testing.T) {
	c, fake := newTestCaddy(t)
	ctx := context.Background()
	if err := c.Sync(ctx, someRoutes); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if c.Status().DNSChallenge {
		t.Error("the DNS challenge is reported without a token")
	}

	c.SetCertificates([]Certificate{{Hostname: "web.example.com", Subjects: []string{"web.example.com"}, CertPEM: "CHAIN", KeyPEM: "KEY"}})
	if err := c.Sync(ctx, someRoutes); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fake.loadCount() != 2 {
		t.Fatalf("loads = %d, want 2: a new certificate is a new configuration", fake.loadCount())
	}
	fake.mu.Lock()
	last := string(fake.loads[len(fake.loads)-1])
	fake.mu.Unlock()
	if !strings.Contains(last, `"load_pem"`) || !strings.Contains(last, `"skip_certificates":["web.example.com"]`) {
		t.Errorf("the loaded configuration lacks the certificate:\n%s", last)
	}

	c.SetCertificates(nil)
	if err := c.Sync(ctx, someRoutes); err != nil || fake.loadCount() != 3 {
		t.Fatalf("after removing it: err = %v, loads = %d, want a third load", err, fake.loadCount())
	}
	fake.mu.Lock()
	last = string(fake.loads[len(fake.loads)-1])
	fake.mu.Unlock()
	if strings.Contains(last, "load_pem") || strings.Contains(last, "skip_certificates") {
		t.Errorf("the hostname is not back under automatic management:\n%s", last)
	}
}
