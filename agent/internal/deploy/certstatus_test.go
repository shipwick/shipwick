package deploy

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/pkg/api"
)

// fakeCerts stands in for the proxy's TLS port: what it presents for each
// server name, and how often it was asked.
type fakeCerts struct {
	mu    sync.Mutex
	certs map[string]ProbedCertificate
	down  error // when set, the proxy cannot be reached
	asked map[string]int
	addrs []string
}

func (f *fakeCerts) probe(_ context.Context, addr, name string) (ProbedCertificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked[name]++
	f.addrs = append(f.addrs, addr)
	if f.down != nil {
		return ProbedCertificate{}, f.down
	}
	c, ok := f.certs[name]
	if !ok {
		return ProbedCertificate{}, ErrNoCertificate
	}
	return c, nil
}

// issue gives name a ninety-day certificate with `left` to go at `now`.
func (f *fakeCerts) issue(name string, now time.Time, left time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	notAfter := now.Add(left)
	f.certs[name] = ProbedCertificate{Issuer: "Let's Encrypt E7", NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
}

func (f *fakeCerts) times(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[name]
}

// certified is a routed harness whose certificates are checked by hand on
// the synthetic clock.
type certified struct {
	*supervised
	certs *fakeCerts
}

func newCertified(t *testing.T) *certified {
	s, _ := newGated(t)
	f := &fakeCerts{certs: map[string]ProbedCertificate{}, asked: map[string]int{}}
	s.engine.opts.CertificateProbe = f.probe
	return &certified{supervised: s, certs: f}
}

// check moves the clock and takes one look at the certificates.
func (c *certified) check(d time.Duration) {
	c.now = c.now.Add(d)
	c.engine.checkCertificates(context.Background(), c.now)
}

func (c *certified) status(t *testing.T, name string) []api.HostnameCertificate {
	t.Helper()
	detail, err := c.engine.Application(context.Background(), name)
	if err != nil {
		t.Fatalf("Application: %v", err)
	}
	return detail.Certificates
}

const day = 24 * time.Hour

func TestCertificateStatusFollowsWhatTheProxyPresents(t *testing.T) {
	c := newCertified(t)
	a := web("web:1.0", 1)
	a.Aliases, a.Redirects = []string{"web2.example.com"}, []string{"www.example.com"}
	c.deploy(a)

	// Routed, never looked at.
	got := c.status(t, "web")
	if len(got) != 3 || got[0].Hostname != "web.example.com" || got[1].Hostname != "web2.example.com" || got[2].Hostname != "www.example.com" {
		t.Fatalf("certificates = %+v, want the domain, the alias and the redirect", got)
	}
	if got[0].Status != api.CertUnknown || got[0].Message != "not checked yet" {
		t.Errorf("before the first look: %+v", got[0])
	}

	// The proxy has a certificate for the domain only.
	c.certs.issue("web.example.com", c.now, 80*day)
	c.check(certTick)
	got = c.status(t, "web")
	if got[0].Status != api.CertOK || got[0].Issuer != "Let's Encrypt E7" || got[0].NotAfter == nil || got[0].Message != "" {
		t.Errorf("domain = %+v, want ok with issuer and expiry", got[0])
	}
	if want := c.now.Add(-certTick).Add(80 * day); !got[0].NotAfter.Equal(want) {
		t.Errorf("not_after = %s, want %s", got[0].NotAfter, want)
	}
	if got[1].Status != api.CertObtaining || got[1].NotAfter != nil || !strings.Contains(got[1].Message, "no certificate for it yet") {
		t.Errorf("alias = %+v, want obtaining", got[1])
	}
	if got[2].Status != api.CertObtaining {
		t.Errorf("redirect = %+v, want obtaining", got[2])
	}
	if c.certs.addrs[0] != DefaultProxyTLSAddr {
		t.Errorf("asked %q, want the compose setup's %q", c.certs.addrs[0], DefaultProxyTLSAddr)
	}
}

func TestCertificateIsLookedAtOnceAMinuteAndSoonerWhileItIsMissing(t *testing.T) {
	c := newCertified(t)
	c.deploy(web("web:1.0", 1))
	c.certs.issue("web.example.com", c.now, 80*day)

	c.check(certTick)
	for i := 0; i < 10; i++ { // 50 seconds of ticks
		c.check(certTick)
	}
	if n := c.certs.times("web.example.com"); n != 1 {
		t.Errorf("asked %d times within a minute, want 1", n)
	}
	c.check(10 * time.Second)
	if n := c.certs.times("web.example.com"); n != 2 {
		t.Errorf("asked %d times after a minute, want 2", n)
	}

	// A hostname the proxy has no certificate for is somebody's wait.
	c.deploy(app("other", "other:1.0", 1))
	b := app("blog", "blog:1.0", 1)
	b.Domain = "blog.example.com"
	c.deploy(b)
	c.check(certTick)
	c.check(certTick)
	if n := c.certs.times("blog.example.com"); n != 1 {
		t.Errorf("asked %d times within ten seconds, want 1", n)
	}
	c.check(certTick)
	if n := c.certs.times("blog.example.com"); n != 2 {
		t.Errorf("asked %d times after ten seconds without a certificate, want 2", n)
	}
}

func TestHostnameHeldBackByTheDNSGateIsWaitingForDNS(t *testing.T) {
	c := newCertified(t)
	c.dns.unresolved("web.example.com")
	a := web("web:1.0", 1)
	a.Aliases = []string{"web2.example.com"}
	c.deploy(a)
	c.check(certTick)

	got := c.status(t, "web")
	if got[0].Status != api.CertWaitingForDNS || !strings.Contains(got[0].Message, "does not resolve yet") {
		t.Errorf("domain = %+v, want waiting_for_dns with the gate's reason", got[0])
	}
	// The alias points here, but is served like a domain that is not served.
	if got[1].Status != api.CertWaitingForDNS || !strings.Contains(got[1].Message, "waits for web.example.com") {
		t.Errorf("alias = %+v, want waiting for its domain", got[1])
	}
	if n := c.certs.times("web.example.com"); n != 0 {
		t.Errorf("the proxy was asked %d times about a hostname it does not serve", n)
	}

	// The record appears; the certificate follows a little later, and its
	// arrival is an event.
	c.dns.resolve("web.example.com")
	c.advance(hostnameRetryAfter)
	c.check(certTick)
	if got := c.status(t, "web"); got[0].Status != api.CertObtaining {
		t.Fatalf("domain = %+v, want obtaining once it is routed", got[0])
	}
	c.certs.issue("web.example.com", c.now, 90*day)
	c.check(certRetryPending)
	if got := c.status(t, "web"); got[0].Status != api.CertOK {
		t.Fatalf("domain = %+v, want ok", got[0])
	}
	events := strings.Join(c.appEvents(t, "web"), "\n")
	want := "Certificate for web.example.com obtained from Let's Encrypt E7, valid until " + c.now.Add(90*day).UTC().Format("2006-01-02")
	if !strings.Contains(events, want) {
		t.Errorf("missing event %q in:\n%s", want, events)
	}
}

func TestACertificateThatWasAlreadyThereIsNotAnnounced(t *testing.T) {
	c := newCertified(t)
	c.certs.issue("web.example.com", c.now, 80*day)
	c.deploy(web("web:1.0", 1))
	c.check(certTick)
	c.check(time.Minute)
	for _, e := range c.appEvents(t, "web") {
		if strings.Contains(e, "Certificate") {
			t.Errorf("event %q for a certificate first seen in order: after an agent restart that is every certificate", e)
		}
	}
}

func TestExpiringCertificateIsReportedAt14DaysAndAgainAt3(t *testing.T) {
	c := newCertified(t)
	rec := notified(c.harness)
	c.deploy(web("web:1.0", 1))
	issued := c.now
	c.certs.issue("web.example.com", issued, 20*day)
	expires := issued.Add(20 * day).UTC().Format("2006-01-02")

	certNotices := func() []notify.Event {
		var out []notify.Event
		for _, e := range rec.Events() {
			if e.Kind == notify.CertificateExpiring {
				out = append(out, e)
			}
		}
		return out
	}

	c.check(certTick)
	if got := c.status(t, "web")[0]; got.Status != api.CertOK {
		t.Fatalf("with 20 days left: %+v", got)
	}
	if n := len(certNotices()); n != 0 {
		t.Fatalf("%d notifications with 20 days left", n)
	}

	// Day 7: 13 days left.
	c.check(7*day - certTick)
	got := c.status(t, "web")[0]
	if got.Status != api.CertExpiring || got.Message != "expires in 13 days, on "+expires || got.NotAfter == nil {
		t.Errorf("with 13 days left: %+v", got)
	}
	notices := certNotices()
	if len(notices) != 1 {
		t.Fatalf("%d notifications on entering expiring, want 1", len(notices))
	}
	if n := notices[0]; n.Application != "web" || !strings.Contains(n.Message, "web: the certificate for web.example.com expires in 13 days, on "+expires) ||
		!strings.Contains(n.Message, "docker logs shipwick-caddy-1") {
		t.Errorf("notification = %+v; it should name the hostname, the days and where to look", n)
	}
	events := strings.Join(c.appEvents(t, "web"), "\n")
	if !strings.Contains(events, "The certificate for web.example.com expires in 13 days") {
		t.Errorf("missing the event in:\n%s", events)
	}

	// The days in between say nothing new.
	for i := 0; i < 5; i++ {
		c.check(day)
	}
	if n := len(certNotices()); n != 1 {
		t.Fatalf("%d notifications by day 12, want still 1", n)
	}

	// Day 17: 3 days left.
	c.check(5 * day)
	if got := c.status(t, "web")[0]; got.Message != "expires in 3 days, on "+expires {
		t.Errorf("with 3 days left: %+v", got)
	}
	if n := len(certNotices()); n != 2 {
		t.Fatalf("%d notifications with 3 days left, want a second one", n)
	}
	c.check(day)
	c.check(day)
	if n := len(certNotices()); n != 2 {
		t.Fatalf("%d notifications by day 19, want still 2", n)
	}

	// Past the end it says so; and a renewal starts over.
	c.check(2 * day)
	if got := c.status(t, "web")[0]; got.Status != api.CertExpiring || got.Message != "expired on "+expires {
		t.Errorf("after the end: %+v", got)
	}
	c.certs.issue("web.example.com", c.now, 90*day)
	c.check(time.Minute)
	if got := c.status(t, "web")[0]; got.Status != api.CertOK || got.Message != "" {
		t.Errorf("after renewal: %+v", got)
	}
	c.certs.issue("web.example.com", c.now, 10*day)
	c.check(time.Minute)
	if n := len(certNotices()); n != 3 {
		t.Errorf("%d notifications after a renewed certificate ran low again, want 3", n)
	}
}

func TestShortLivedCertificateIsNotExpiringFromTheStart(t *testing.T) {
	c := newCertified(t)
	rec := notified(c.harness)
	c.deploy(web("web:1.0", 1))
	// What Caddy's own authority issues for *.localhost: twelve hours.
	c.certs.certs["web.example.com"] = ProbedCertificate{Issuer: "Caddy Local Authority - ECC Intermediate", NotBefore: c.now, NotAfter: c.now.Add(12 * time.Hour)}

	c.check(certTick)
	if got := c.status(t, "web")[0]; got.Status != api.CertOK {
		t.Errorf("a fresh twelve-hour certificate: %+v, want ok", got)
	}
	// Caddy renews it with four hours to go; with two left it has not.
	c.check(10 * time.Hour)
	if got := c.status(t, "web")[0]; got.Status != api.CertExpiring || !strings.HasPrefix(got.Message, "expires in 1 hour") {
		t.Errorf("with under two hours left: %+v, want expiring", got)
	}
	for _, e := range rec.Events() {
		if e.Kind == notify.CertificateExpiring {
			return
		}
	}
	t.Error("no notification for a short-lived certificate that was not renewed")
}

func TestCertificateStatusIsUnknownWhenTheProxyCannotBeAsked(t *testing.T) {
	// No proxy configured at all.
	h := newHarness(t)
	a := app("my-api", "my-api:1.0", 1)
	a.Domain = "api.example.com"
	h.deploy(a)
	detail, err := h.engine.Application(context.Background(), "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Certificates; len(got) != 1 || got[0].Status != api.CertUnknown || !strings.Contains(got[0].Message, "no reverse proxy") {
		t.Errorf("without a proxy: %+v", got)
	}
	// An application without a hostname has none to report, as a list.
	h.deploy(app("worker", "worker:1.0", 1))
	if detail, _ := h.engine.Application(context.Background(), "worker"); detail.Certificates == nil || len(detail.Certificates) != 0 {
		t.Errorf("without a domain: %+v, want an empty list", detail.Certificates)
	}

	// A proxy that does not answer.
	c := newCertified(t)
	rec := notified(c.harness)
	c.deploy(web("web:1.0", 1))
	c.certs.issue("web.example.com", c.now, 80*day)
	c.check(certTick)
	c.certs.down = errors.New("dial tcp: connection refused")
	c.check(time.Minute)
	got := c.status(t, "web")[0]
	if got.Status != api.CertUnknown || !strings.Contains(got.Message, "connection refused") {
		t.Errorf("proxy down: %+v, want unknown with the reason", got)
	}
	// It comes back with the same certificate: nothing was obtained, nothing
	// is announced.
	c.certs.down = nil
	c.check(time.Minute)
	if got := c.status(t, "web")[0]; got.Status != api.CertOK {
		t.Errorf("proxy back: %+v", got)
	}
	for _, e := range c.appEvents(t, "web") {
		if strings.Contains(e, "Certificate") {
			t.Errorf("event %q after the proxy was briefly unreachable", e)
		}
	}
	if len(rec.Events()) != 1 { // the deployment
		t.Errorf("notifications = %v", rec.Kinds())
	}
}

func TestProbeReadsTheCertificateTheServerPresents(t *testing.T) {
	// httptest's certificate is for example.com, issued by "Acme Co".
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	ctx := context.Background()

	cert, err := probeCertificate(ctx, addr, "example.com")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	leaf := srv.Certificate()
	if cert.Issuer != "Acme Co" || !cert.NotAfter.Equal(leaf.NotAfter) || !cert.NotBefore.Equal(leaf.NotBefore) {
		t.Errorf("certificate = %+v, want the server's: issuer Acme Co, until %s", cert, leaf.NotAfter)
	}

	// A certificate for another name is no certificate for this one: it is
	// what Caddy falls back to while the right one is being obtained.
	if _, err := probeCertificate(ctx, addr, "other.example.org"); !errors.Is(err, ErrNoCertificate) {
		t.Errorf("another name: err = %v, want ErrNoCertificate", err)
	}

	// Nobody listening is not an answer about certificates.
	srv.Close()
	if _, err := probeCertificate(ctx, addr, "example.com"); err == nil || errors.Is(err, ErrNoCertificate) {
		t.Errorf("proxy down: err = %v, want a connection error", err)
	}
}

func TestIssuerNameReadsLikeTheAuthorityIsKnown(t *testing.T) {
	for _, tc := range []struct{ org, cn, want string }{
		{"Let's Encrypt", "E7", "Let's Encrypt E7"},
		{"", "Caddy Local Authority - ECC Intermediate", "Caddy Local Authority - ECC Intermediate"},
		{"ZeroSSL", "ZeroSSL ECC Domain Secure Site CA", "ZeroSSL ECC Domain Secure Site CA"},
		{"Acme Co", "", "Acme Co"},
	} {
		c := &x509.Certificate{Issuer: pkix.Name{CommonName: tc.cn}}
		if tc.org != "" {
			c.Issuer.Organization = []string{tc.org}
		}
		if got := issuerName(c); got != tc.want {
			t.Errorf("issuer %q / %q = %q, want %q", tc.org, tc.cn, got, tc.want)
		}
	}
}

func TestAWildcardIsAskedForUnderANameItCovers(t *testing.T) {
	c := newCertified(t)
	c.engine.opts.DNSChallenge = true
	a := web("web:1.0", 1)
	a.Aliases = []string{"*.example.com"}
	c.deploy(a)
	c.certs.issue("web.example.com", c.now, 60*day)
	c.certs.issue("certificate-check.example.com", c.now, 60*day)

	c.check(certTick)
	for _, got := range c.status(t, "web") {
		if got.Status != api.CertOK {
			t.Errorf("%s: %+v", got.Hostname, got)
		}
	}
	if n := c.certs.times("*.example.com"); n != 0 {
		t.Errorf("the proxy was asked for the wildcard itself %d times; no client can send that name", n)
	}
}

func TestASuppliedCertificateRunningOutSaysHowToReplaceIt(t *testing.T) {
	c := newCertified(t)
	rec := notified(c.harness)
	c.supply("web.example.com")
	c.deploy(web("web:1.0", 1))
	c.certs.issue("web.example.com", c.now, 10*day)

	c.check(certTick)
	var texts []string
	for _, e := range rec.Events() {
		if e.Kind == notify.CertificateExpiring {
			texts = append(texts, e.Message)
		}
	}
	if len(texts) != 1 {
		t.Fatalf("notifications = %q, want one", texts)
	}
	if !strings.Contains(texts[0], "shipwick cert set web.example.com") || strings.Contains(texts[0], "renews certificates by itself") {
		t.Errorf("notification = %q, want the command that replaces a supplied certificate", texts[0])
	}
}
