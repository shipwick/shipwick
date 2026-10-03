package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/certs/certstest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// cloudflareAddress is one of Cloudflare's: what a proxied record resolves to.
const cloudflareAddress = "104.21.5.6"

// supply stores a certificate for hosts under the first of them.
func (h *harness) supply(hosts ...string) certstest.Pair {
	h.t.Helper()
	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), hosts...)
	if _, err := h.engine.SetCertificate(context.Background(), hosts[0], pair.Cert, pair.Key); err != nil {
		h.t.Fatalf("SetCertificate: %v", err)
	}
	return pair
}

// supplied is what the proxy was last told about supplied certificates:
// the hostnames they are stored under, joined.
func (p *fakeProxy) supplied() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.certs))
	for _, c := range p.certs {
		names = append(names, c.Hostname)
	}
	return strings.Join(names, ",")
}

func TestWithTheDNSChallengeAHostnameBehindCloudflareIsServed(t *testing.T) {
	s, p := newGated(t)
	s.engine.opts.DNSChallenge = true
	s.dns.resolve("web.example.com", cloudflareAddress, "2606:4700:3030::6815:506")

	d := s.deploy(web("web:1.0", 1))
	if !p.hasRoute("web.example.com") {
		t.Fatalf("a hostname behind Cloudflare's proxy was held back although certificates come through DNS:\n%s", s.steps(d.ID))
	}
	if steps := s.steps(d.ID); !strings.Contains(steps, "info: Routed https://web.example.com to 1 replica") {
		t.Errorf("steps lack the routing step:\n%s", steps)
	}
}

func TestTheDNSChallengeDoesNotExcuseARecordThatPointsElsewhere(t *testing.T) {
	s, p := newGated(t)
	s.engine.opts.DNSChallenge = true
	s.dns.resolve("web.example.com", "198.51.100.7")

	d := s.deploy(web("web:1.0", 1))
	if p.hasRoute("web.example.com") {
		t.Error("a hostname that points at another server reached the proxy")
	}
	// Proxied or not is the operator's choice once the challenge is set up,
	// so the advice no longer insists on "DNS only".
	want := "resolves to 198.51.100.7, not to this server; change the A record: web.example.com → " + serverAddress + "."
	if steps := s.steps(d.ID); !strings.Contains(steps, want) {
		t.Errorf("steps lack %q:\n%s", want, steps)
	}
}

func TestAWildcardIsRefusedWithoutAWayToItsCertificate(t *testing.T) {
	s, _ := newRouted(t)
	for field, a := range map[string]spec.App{
		"domain": func() spec.App { a := web("web:1.0", 1); a.Domain = "*.example.com"; return a }(),
		"aliases[1]": func() spec.App {
			a := web("web:1.0", 1)
			a.Aliases = []string{"a.example.com", "*.example.com"}
			return a
		}(),
	} {
		_, err := s.engine.Deploy(context.Background(), a)
		var wildcard *WildcardError
		if !errors.As(err, &wildcard) {
			t.Fatalf("%s: Deploy = %v, want a WildcardError", field, err)
		}
		fields := wildcard.Fields()
		if len(fields) != 1 || fields[0].Field != field || !strings.Contains(fields[0].Expected, "SHIPWICK_CLOUDFLARE_API_TOKEN") ||
			!strings.Contains(fields[0].Expected, "shipwick cert set '*.example.com'") {
			t.Errorf("%s: fields = %+v, want the field and both ways out", field, fields)
		}
	}
	if all, _ := s.store.ListDeployments(context.Background(), store.DeploymentFilter{}); len(all) != 0 {
		t.Errorf("a refused deployment was recorded: %d records", len(all))
	}
}

func TestAWildcardIsServedWithTheDNSChallenge(t *testing.T) {
	s, p := newGated(t)
	s.engine.opts.DNSChallenge = true
	a := web("web:1.0", 1)
	a.Aliases = []string{"*.example.com"}

	d := s.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if r := p.route(t, "web.example.com"); strings.Join(r.Aliases, ",") != "*.example.com" {
		t.Errorf("route = %+v, want the wildcard as an alias", r)
	}
	if n := s.dns.lookups("*.example.com"); n != 0 {
		t.Errorf("a wildcard was looked up %d times; there is no such record to find", n)
	}
}

func TestAWildcardAndANameUnderItBelongToDifferentApplications(t *testing.T) {
	s, p := newRouted(t)
	s.engine.opts.DNSChallenge = true
	wild := app("tenants", "tenants:1.0", 1)
	wild.Domain = "*.example.com"
	s.deploy(wild)

	named := app("api", "api:1.0", 1)
	named.Domain = "api.example.com"
	if d := s.deploy(named); d.Status != api.StatusActive {
		t.Fatalf("api.example.com was refused next to *.example.com: %s (%s)", d.Status, d.Error)
	}
	if len(p.upstreams("*.example.com")) != 1 || len(p.upstreams("api.example.com")) != 1 {
		t.Error("both hostnames should have a route of their own")
	}

	second := app("other", "other:1.0", 1)
	second.Domain = "*.example.com"
	var conflict *DomainConflictError
	if _, err := s.engine.Deploy(context.Background(), second); !errors.As(err, &conflict) {
		t.Errorf("a second application claiming *.example.com: err = %v, want a conflict", err)
	}
}

func TestASuppliedCertificateLetsAWildcardBeDeployed(t *testing.T) {
	s, p := newRouted(t)
	s.supply("*.example.com", "example.com")
	a := web("web:1.0", 1)
	a.Domain = "*.example.com"

	d := s.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if !p.hasRoute("*.example.com") {
		t.Error("the wildcard did not reach the proxy")
	}
	if steps := s.steps(d.ID); !strings.Contains(steps, "info: Routed https://*.example.com to 1 replica, with the certificate you supplied for it") {
		t.Errorf("the routing step does not mention the certificate:\n%s", steps)
	}
}

func TestAHostnameWithASuppliedCertificateDoesNotWaitForDNS(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("web.example.com")
	s.supply("*.example.com")

	d := s.deploy(web("web:1.0", 1))
	if !p.hasRoute("web.example.com") {
		t.Fatalf("the hostname waits for DNS although no authority will be asked:\n%s", s.steps(d.ID))
	}
	if steps := s.steps(d.ID); !strings.Contains(steps, "info: Routed https://web.example.com to 1 replica, with the certificate you supplied for *.example.com") {
		t.Errorf("the routing step does not name the certificate:\n%s", steps)
	}
	if n := s.dns.lookups("web.example.com"); n != 0 {
		t.Errorf("the hostname was looked up %d times", n)
	}
	if got := p.supplied(); got != "*.example.com" {
		t.Errorf("the proxy was given the certificates %q", got)
	}
}

func TestSupplyingACertificateServesAHostnameThatWasWaiting(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("web.example.com")
	s.deploy(web("web:1.0", 1))
	if p.hasRoute("web.example.com") {
		t.Fatal("the hostname should be waiting for DNS")
	}

	s.supply("web.example.com")
	if !p.hasRoute("web.example.com") {
		t.Fatal("supplying the certificate should put the hostname into the proxy at once, not at the next deployment")
	}
	const announced = "web.example.com is now served, with the certificate you supplied for it"
	if events := strings.Join(s.appEvents(t, "web"), "\n"); !strings.Contains(events, announced) {
		t.Errorf("app events lack %q:\n%s", announced, events)
	}
}

func TestRemovingACertificatePutsItsHostnamesBackBehindTheGate(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("web.example.com")
	s.supply("web.example.com")
	s.deploy(web("web:1.0", 1))
	if !p.hasRoute("web.example.com") || p.supplied() != "web.example.com" {
		t.Fatal("setup: the hostname should be served with the supplied certificate")
	}

	if err := s.engine.DeleteCertificate(context.Background(), "web.example.com"); err != nil {
		t.Fatalf("DeleteCertificate: %v", err)
	}
	if p.supplied() != "" {
		t.Errorf("the proxy still holds %q", p.supplied())
	}
	if p.hasRoute("web.example.com") {
		t.Error("the hostname stayed in the proxy: Caddy would ask an authority for a name that does not resolve")
	}
	if err := s.engine.DeleteCertificate(context.Background(), "web.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting again = %v, want ErrNotFound", err)
	}

	// With a record that points here it is an ordinary hostname again.
	s.dns.resolve("web.example.com")
	s.advance(hostnameRetryAfter)
	if !p.hasRoute("web.example.com") {
		t.Error("the hostname should be served once its DNS points here")
	}
}

func TestAWildcardWhoseCertificateWasRemovedIsHeldBackWithTheReason(t *testing.T) {
	s, p := newGated(t)
	s.supply("*.example.com")
	a := web("web:1.0", 1)
	a.Aliases = []string{"*.example.com"}
	s.deploy(a)

	if err := s.engine.DeleteCertificate(context.Background(), "*.example.com"); err != nil {
		t.Fatalf("DeleteCertificate: %v", err)
	}
	if p.hasRoute("*.example.com") {
		t.Error("the wildcard stayed in the proxy, which cannot obtain a certificate for it")
	}
	if !p.hasRoute("web.example.com") {
		t.Error("the domain itself points here and must stay served")
	}
	if _, why := s.engine.hostnameReady("*.example.com"); !strings.Contains(why, "SHIPWICK_CLOUDFLARE_API_TOKEN") || !strings.Contains(why, "shipwick cert set '*.example.com'") {
		t.Errorf("why = %q, want both ways out", why)
	}
}

func TestSetCertificateRefusesWhatCannotServeTheHostname(t *testing.T) {
	s, p := newRouted(t)
	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), "example.com")
	other := certstest.Issue(time.Now().AddDate(0, 3, 0), "example.com")
	expired := certstest.Issue(time.Now().AddDate(0, 0, -1), "example.com")

	for name, tt := range map[string]struct{ hostname, cert, key, want string }{
		"another hostname": {"example.org", pair.Cert, pair.Key, "does not cover example.org"},
		"a foreign key":    {"example.com", pair.Cert, other.Key, "does not belong to"},
		"expired":          {"example.com", expired.Cert, expired.Key, "expired on"},
	} {
		_, err := s.engine.SetCertificate(context.Background(), tt.hostname, tt.cert, tt.key)
		var invalid *InvalidCertificateError
		if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, tt.want) {
			t.Errorf("%s: err = %v, want a refusal containing %q", name, err, tt.want)
		}
	}
	if list, _ := s.engine.Certificates(context.Background()); len(list) != 0 || p.supplied() != "" {
		t.Errorf("a refused certificate was stored: %+v", list)
	}
}

func TestCertificatesAreListedByWhatTheirChainSays(t *testing.T) {
	s, _ := newRouted(t)
	notAfter := time.Now().AddDate(0, 3, 0)
	pair := certstest.Issue(notAfter, "example.com", "*.example.com")
	stored, err := s.engine.SetCertificate(context.Background(), "example.com", pair.Cert, pair.Key)
	if err != nil {
		t.Fatalf("SetCertificate: %v", err)
	}
	list, err := s.engine.Certificates(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("Certificates = %+v, %v", list, err)
	}
	c := list[0]
	if c.Hostname != "example.com" || strings.Join(c.Subjects, ",") != "example.com,*.example.com" || c.Issuer != "Shipwick Test Authority" ||
		c.NotAfter.Unix() != notAfter.Unix() || c.CreatedAt.IsZero() {
		t.Errorf("certificate = %+v", c)
	}
	if stored.Hostname != c.Hostname || !stored.NotAfter.Equal(c.NotAfter) || !stored.CreatedAt.Equal(c.CreatedAt) {
		t.Errorf("SetCertificate answered %+v, the list says %+v", stored, c)
	}
}

func TestTheServerViewReportsTheDNSChallenge(t *testing.T) {
	s, _ := newRouted(t)
	if server, err := s.engine.Server(context.Background()); err != nil || server.Proxy.DNSChallenge {
		t.Fatalf("Server = %+v, %v; the challenge is off unless configured", server.Proxy, err)
	}
	s.engine.opts.DNSChallenge = true
	if server, _ := s.engine.Server(context.Background()); !server.Proxy.DNSChallenge || !server.Proxy.Enabled {
		t.Errorf("proxy = %+v, want dns_challenge reported", server.Proxy)
	}
}
