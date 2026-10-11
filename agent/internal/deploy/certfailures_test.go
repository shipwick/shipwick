package deploy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
)

// The lines of testdata/caddy_tls_errors.log are shaped as CertMagic 0.25 and
// acmez 3.1 write them — their log calls were read for it — around the two
// failures a real server produced: a firewall that kept Let's Encrypt from
// port 443, and a Cloudflare token that could not create the record.

func TestProxyLogLinesAboutCertificatesAreParsedAndTheRestIgnored(t *testing.T) {
	f, err := os.Open("testdata/caddy_tls_errors.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type parsed struct {
		host string
		f    certFailure
	}
	var got []parsed
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if host, failure, ok := parseProxyErrorLine(sc.Bytes()); ok {
			got = append(got, parsed{host, failure})
		}
	}
	if len(got) != 8 {
		t.Fatalf("parsed %d lines, want 8: the two info lines, the access error and the OCSP warning are not failures", len(got))
	}

	// The authority's answer, with the challenge it was about.
	if p := got[0]; p.host != "api.example.com" || p.f.challenge != "tls-alpn-01" || p.f.problem != "urn:ietf:params:acme:error:connection" ||
		p.f.detail != "203.0.113.10: Timeout during connect (likely firewall problem)" {
		t.Errorf("challenge failed: %+v", p)
	}
	if want := time.Date(2026, 10, 11, 14, 0, 12, 500000000, time.UTC); !got[0].f.at.Equal(want) {
		t.Errorf("time = %s, want %s", got[0].f.at, want)
	}
	// CertMagic's summary of it: the problem read out of the text, no challenge.
	if p := got[2]; p.host != "api.example.com" || p.f.issuer != "acme-v02.api.letsencrypt.org-directory" || p.f.problem != "urn:ietf:params:acme:error:connection" ||
		p.f.challenge != "" || p.f.detail != "203.0.113.10: Timeout during connect (likely firewall problem)" {
		t.Errorf("could not get certificate from issuer: %+v", p)
	}
	// The retry line names the hostname in brackets; the order and the CA are dropped.
	if p := got[3]; p.host != "api.example.com" || strings.Contains(p.f.detail, "order=") || strings.Contains(p.f.detail, "ca=") || p.f.problem != "urn:ietf:params:acme:error:connection" {
		t.Errorf("will retry: %+v", p)
	}
	// The DNS provider's refusal is not an ACME problem; the challenge is read from the words.
	if p := got[4]; p.host != "scrumpokerplay.com" || p.f.problem != "" || p.f.challenge != "dns-01" ||
		!strings.HasPrefix(p.f.detail, "[scrumpokerplay.com] solving challenges: presenting for challenge: adding temporary record") || strings.Contains(p.f.detail, "order=") {
		t.Errorf("cloudflare refusal: %+v", p)
	}
	if p := got[6]; p.host != "*.example.com" || p.f.challenge != "dns-01" || p.f.problem != "urn:ietf:params:acme:error:unauthorized" {
		t.Errorf("wildcard: %+v", p)
	}
	if p := got[7]; p.host != "blog.example.com" || p.f.problem != "urn:ietf:params:acme:error:rateLimited" {
		t.Errorf("rate limit: %+v", p)
	}
}

// failed is a "challenge failed" line for host, as acmez logs it.
func failed(at time.Time, host, challenge, problem, detail string) string {
	return fmt.Sprintf(`{"level":"error","ts":%d.%06d,"logger":"tls.issuance.acme.acme_client","msg":"challenge failed","identifier":%q,"challenge_type":%q,"problem":{"type":"urn:ietf:params:acme:error:%s","title":"","detail":%q,"instance":"","subproblems":[]}}`,
		at.Unix(), at.Nanosecond()/1000, host, challenge, problem, detail)
}

// summarized is CertMagic's "could not get certificate from issuer" for the
// same failure, logged a moment later without the challenge.
func summarized(at time.Time, host, text string) string {
	return fmt.Sprintf(`{"level":"error","ts":%d.%06d,"logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":%q,"issuer":"acme-v02.api.letsencrypt.org-directory","error":%q}`,
		at.Unix(), at.Nanosecond()/1000, host, text)
}

const firewalled = "Let's Encrypt could not connect to the server on port 443 (tls-alpn-01): a firewall in front of it?"

func TestAFailureTheProxyLoggedIsTheReasonTheCertificateIsMissing(t *testing.T) {
	c := newCertified(t)
	c.deploy(web("web:1.0", 1))
	c.check(certTick)
	if got := c.status(t, "web"); got[0].Status != api.CertObtaining || got[0].Failure != nil {
		t.Fatalf("before any failure: %+v", got[0])
	}

	at := c.now.Add(-time.Second)
	c.engine.noteProxyError([]byte(failed(at, "web.example.com", "tls-alpn-01", "connection", "203.0.113.10: Timeout during connect (likely firewall problem)")))
	c.engine.noteProxyError([]byte(summarized(at.Add(100*time.Millisecond), "web.example.com", "HTTP 400 urn:ietf:params:acme:error:connection - 203.0.113.10: Timeout during connect (likely firewall problem)")))

	got := c.status(t, "web")[0]
	if got.Status != api.CertObtaining || got.Message != "the proxy has no certificate for it yet: "+firewalled {
		t.Errorf("status = %+v, want obtaining with the reason", got)
	}
	if got.Failure == nil || got.Failure.Challenge != "tls-alpn-01" || got.Failure.Reason != firewalled ||
		got.Failure.Detail != "203.0.113.10: Timeout during connect (likely firewall problem)" || !got.Failure.At.Equal(at.Add(100*time.Millisecond)) {
		t.Errorf("failure = %+v; the summary without a challenge inherits the one logged a moment before", got.Failure)
	}
	if p := c.listed("web").CertificateProblem; p == nil || p.Status != api.CertObtaining || !strings.Contains(p.Message, firewalled) {
		t.Errorf("certificate_problem = %+v, want the reason in the list too", p)
	}

	// Said once in the events, not once per attempt.
	c.check(certTick)
	c.engine.noteProxyError([]byte(failed(c.now, "web.example.com", "tls-alpn-01", "connection", "203.0.113.10: Timeout during connect (likely firewall problem)")))
	c.check(certTick)
	c.check(certTick)
	want := "Certificate for web.example.com could not be obtained: " + firewalled
	if events := c.appEvents(t, "web"); strings.Count(strings.Join(events, "\n"), want) != 1 {
		t.Errorf("want %q once in:\n%s", want, strings.Join(events, "\n"))
	}

	// A different reason is news again.
	c.engine.noteProxyError([]byte(failed(c.now.Add(time.Second), "web.example.com", "http-01", "connection", "Fetching http://web.example.com/.well-known/acme-challenge/x: Timeout")))
	c.check(certTick)
	if events := strings.Join(c.appEvents(t, "web"), "\n"); !strings.Contains(events, "could not be obtained: Let's Encrypt could not connect to the server on port 80 (http-01): a firewall in front of it?") {
		t.Errorf("the second reason was not recorded:\n%s", events)
	}

	// The certificate arrives: the failure is over.
	c.certs.issue("web.example.com", c.now, 90*day)
	c.check(certRetryPending)
	if got := c.status(t, "web")[0]; got.Status != api.CertOK || got.Failure != nil || got.Message != "" {
		t.Errorf("after the certificate arrived: %+v", got)
	}
}

func TestARenewalThatFailsIsSaidWhileTheCertificateStillServes(t *testing.T) {
	c := newCertified(t)
	c.certs.issue("web.example.com", c.now, 80*day)
	c.deploy(web("web:1.0", 1))
	c.check(certTick)

	c.engine.noteProxyError([]byte(failed(c.now, "web.example.com", "tls-alpn-01", "connection", "203.0.113.10: Timeout during connect (likely firewall problem)")))
	c.check(certTick)
	got := c.status(t, "web")[0]
	if got.Status != api.CertOK || got.Message != "" || got.Failure == nil || got.Failure.Reason != firewalled {
		t.Errorf("status = %+v, want ok with the failure beside it", got)
	}
	if events := strings.Join(c.appEvents(t, "web"), "\n"); !strings.Contains(events, "Certificate for web.example.com could not be renewed: "+firewalled) {
		t.Errorf("want the renewal failure in:\n%s", events)
	}

	// A newer certificate ends it.
	c.certs.issue("web.example.com", c.now, 90*day)
	c.check(time.Minute)
	if got := c.status(t, "web")[0]; got.Failure != nil {
		t.Errorf("failure = %+v after a newer certificate", got.Failure)
	}
}

func TestAnOlderLineDoesNotReplaceTheFailureKept(t *testing.T) {
	c := newCertified(t)
	c.deploy(web("web:1.0", 1))
	c.engine.noteProxyError([]byte(failed(c.now, "web.example.com", "tls-alpn-01", "connection", "new")))
	if _, _, ok := c.engine.noteProxyError([]byte(failed(c.now.Add(-time.Hour), "web.example.com", "http-01", "connection", "old"))); ok {
		t.Error("a line from before the failure kept was taken: Docker repeats lines when a stream is reopened")
	}
	if got := c.status(t, "web")[0]; got.Failure == nil || got.Failure.Detail != "new" {
		t.Errorf("failure = %+v", got.Failure)
	}
}

const cloudflareRefused = `[web.example.com] solving challenges: presenting for challenge: adding temporary record for zone "example.com.": got error status: HTTP 403: [{Code:10000 Message:Authentication error}] (order=https://acme-v02.api.letsencrypt.org/acme/order/1/2) (ca=https://acme-v02.api.letsencrypt.org/directory)`

func TestADNSRecordThatCouldNotBeCreatedIsExplainedWithTheTokenCheck(t *testing.T) {
	c := newCloudflared(t)
	c.deploy(web("web:1.0", 1))
	c.check(certTick)

	// Before any check of the token, Cloudflare's answer as the proxy quoted it.
	c.engine.noteProxyError([]byte(summarized(c.now, "web.example.com", cloudflareRefused)))
	got := c.status(t, "web")[0]
	want := "the DNS record for the challenge could not be created: Cloudflare answered HTTP 403, code 10000: Authentication error; the token's IP filter and its Zone → DNS → Edit permission are what to check (shipwick doctor checks them)"
	if got.Failure == nil || got.Failure.Reason != want || got.Failure.Challenge != "dns-01" {
		t.Errorf("failure = %+v\nwant reason %q", got.Failure, want)
	}

	// With the check's finding, its sentence: the one a person acts on.
	c.verifier.status = api.CloudflareStatus{Zones: []api.CloudflareZone{{Name: "example.com"}},
		Problems: []string{"the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)"}}
	if _, err := c.engine.VerifyCloudflare(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = c.status(t, "web")[0]
	want = "the DNS record for the challenge could not be created: the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)"
	if got.Failure == nil || got.Failure.Reason != want {
		t.Errorf("failure = %+v\nwant reason %q", got.Failure, want)
	}
	if got.Message != "the proxy has no certificate for it yet: "+want {
		t.Errorf("message = %q", got.Message)
	}
}

func TestReasonsForTheOtherAnswersOfAnAuthority(t *testing.T) {
	c := newCertified(t)
	for _, tc := range []struct {
		f    certFailure
		want string
	}{
		{certFailure{challenge: "dns-01", problem: "urn:ietf:params:acme:error:dns", detail: "no TXT record found at _acme-challenge.web.example.com"},
			"Let's Encrypt did not find the DNS record for the challenge (dns-01): no TXT record found at _acme-challenge.web.example.com"},
		{certFailure{challenge: "tls-alpn-01", problem: "urn:ietf:params:acme:error:dns", detail: "DNS problem: NXDOMAIN looking up A for web.example.com"},
			"Let's Encrypt could not look up the hostname (tls-alpn-01): DNS problem: NXDOMAIN looking up A for web.example.com"},
		{certFailure{challenge: "tls-alpn-01", problem: "urn:ietf:params:acme:error:unauthorized", detail: "Incorrect validation certificate for tls-alpn-01 challenge"},
			"Let's Encrypt was answered by something other than this server (tls-alpn-01): Incorrect validation certificate for tls-alpn-01 challenge; a proxy in front of the server, such as Cloudflare's, needs SHIPWICK_CLOUDFLARE_API_TOKEN on the agent"},
		{certFailure{challenge: "dns-01", problem: "urn:ietf:params:acme:error:unauthorized", detail: `Incorrect TXT record "abc" found`},
			`Let's Encrypt found the wrong TXT record for the challenge (dns-01): Incorrect TXT record "abc" found; another DNS provider, or an older record, may be answering for the zone`},
		{certFailure{problem: "urn:ietf:params:acme:error:rateLimited", detail: "too many failed authorizations (5) for \"example.com\" in the last 1h0m0s"},
			"Let's Encrypt refuses for now, its rate limit is reached: too many failed authorizations (5) for \"example.com\" in the last 1h0m0s; it lifts within the hour"},
		{certFailure{problem: "urn:ietf:params:acme:error:badCSR", detail: "CSR is not valid", issuer: "acme.zerossl.com-v2-DV90"},
			"ZeroSSL refused (badCSR): CSR is not valid"},
		{certFailure{detail: "timed out waiting for record to fully propagate; verify DNS provider configuration is correct - last error: NXDOMAIN"},
			"the DNS record for the challenge was created but did not become visible in time: timed out waiting for record to fully propagate; verify DNS provider configuration is correct - last error: NXDOMAIN"},
		{certFailure{detail: `could not determine zone for domain "_acme-challenge.web.example.com": unexpected response code 'REFUSED'`},
			"the DNS record for the challenge could not be created: the zone of web.example.com is not one the Cloudflare token sees; it needs Zone → Zone → Read and Zone → DNS → Edit on that zone"},
		{certFailure{detail: "no solvers available for remaining challenges (configured=[tls-alpn-01] offered=[dns-01] remaining=[dns-01])"},
			"the certificate authority offers no challenge the proxy can solve: no solvers available for remaining challenges (configured=[tls-alpn-01] offered=[dns-01] remaining=[dns-01])"},
		{certFailure{detail: "something else entirely"}, "Let's Encrypt did not issue it: something else entirely"},
	} {
		if got := c.engine.failureReason("web.example.com", tc.f); got != tc.want {
			t.Errorf("reason for %+v:\n got %q\nwant %q", tc.f, got, tc.want)
		}
	}
	c.engine.opts.Network.ACMEDirectory = "https://ca.internal/acme/directory"
	if got := c.engine.failureReason("web.example.com", certFailure{detail: "x"}); got != "the certificate authority did not issue it: x" {
		t.Errorf("with an ACME directory of the operator's own: %q", got)
	}
}

func TestTheProxysOwnMessagesAreFollowedThroughTheRuntime(t *testing.T) {
	c := newCertified(t)
	c.deploy(web("web:1.0", 1))
	ctx := context.Background()
	start := c.now.Add(-time.Minute)
	later := c.now.Add(-30 * time.Second)

	c.rt.WriteErrors(dockertest.ProxyID,
		`{"level":"info","ts":1791727200.1,"logger":"tls.obtain","msg":"obtaining certificate","identifier":"web.example.com"}`,
		failed(later, "web.example.com", "tls-alpn-01", "connection", "203.0.113.10: Timeout during connect (likely firewall problem)"))
	c.rt.EndErrors(dockertest.ProxyID)
	next := c.engine.followProxyErrorsOnce(ctx, start)
	if !next.Equal(later) {
		t.Errorf("resume at %s, want the time of the last failure, %s", next, later)
	}
	if got := c.status(t, "web")[0]; got.Failure == nil || got.Failure.Reason != firewalled {
		t.Errorf("failure = %+v, want the one the proxy logged", got.Failure)
	}

	// Nothing about certificates: the reading resumes where it started.
	c.rt.WriteErrors(dockertest.ProxyID, `{"level":"info","ts":1791727300,"logger":"admin","msg":"admin endpoint started"}`)
	c.rt.EndErrors(dockertest.ProxyID)
	if again := c.engine.followProxyErrorsOnce(ctx, next); !again.Equal(next) {
		t.Errorf("resume at %s, want %s", again, next)
	}
}
