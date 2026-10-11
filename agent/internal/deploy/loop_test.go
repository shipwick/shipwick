package deploy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// answerWith is a RedirectProbe whose application always answers the same.
func answerWith(status int, location string) RedirectProbe {
	return func(context.Context, string, string, string) (int, string, error) { return status, location, nil }
}

// askedProxy records what the probe was asked, and answers from a script:
// failures first, then the answer.
type askedProxy struct {
	mu       sync.Mutex
	failures int
	status   int
	location string
	asked    []string // "<addr> <host> <path>" per attempt
}

func (p *askedProxy) probe(_ context.Context, addr, host, path string) (int, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, addr+" "+host+" "+path)
	if p.failures > 0 {
		p.failures--
		return 0, "", errors.New("connection refused")
	}
	return p.status, p.location, nil
}

func (p *askedProxy) attempts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.asked)
}

// probeEvents are the deployment's probe events, "level: message" per line.
func (h *harness) probeEvents(id int64) string {
	var lines []string
	for _, e := range h.events(id) {
		if e.Type == api.EventProbe {
			lines = append(lines, e.Level+": "+e.Message)
		}
	}
	return strings.Join(lines, "\n")
}

func wwwRedirected(image string) spec.App {
	a := web(image, 1)
	a.Redirects = []string{"www.example.com"}
	return a
}

func TestARedirectToARedirectedHostnameIsReportedAsALoop(t *testing.T) {
	s, _ := newRouted(t)
	s.engine.opts.RedirectProbe = answerWith(301, "https://www.example.com/")
	d := s.deploy(wwwRedirected("web:1.0"))
	if d.Status != api.StatusActive || d.CompletedAt == nil {
		t.Fatalf("a loop is a warning, not a failure: %s %q", d.Status, d.Error)
	}
	want := "warn: https://web.example.com/ sends its visitors to www.example.com, which `redirects` sends back: every request loops. " +
		"Make www.example.com the `domain` and web.example.com a redirect, or stop the application redirecting"
	if got := s.probeEvents(d.ID); got != want {
		t.Errorf("probe events:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnyOtherAnswerIsRecordedAndNotJudged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		location string
		want     string
	}{
		{"a page", 200, "", "info: https://web.example.com/ answers HTTP 200 through the proxy"},
		{"a redirect to a sign-in page", 302, "https://web.example.com/login", "info: https://web.example.com/ answers HTTP 302 through the proxy to https://web.example.com/login"},
		{"a redirect elsewhere", 301, "https://example.org/", "info: https://web.example.com/ answers HTTP 301 through the proxy to https://example.org/"},
		{"a failure of the application's own", 500, "", "info: https://web.example.com/ answers HTTP 500 through the proxy"},
	} {
		s, _ := newRouted(t)
		s.engine.opts.RedirectProbe = answerWith(tc.status, tc.location)
		d := s.deploy(wwwRedirected("web:1.0"))
		if got := s.probeEvents(d.ID); got != tc.want {
			t.Errorf("%s: probe events = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTheProbeGoesThroughTheProxyUnderTheHostnameAndFollowsNothing(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Host+" "+r.TLS.ServerName+" "+r.URL.Path)
		mu.Unlock()
		if r.Host == "www.example.com" {
			// Following the redirect would land here, as the proxy does
			// for a redirected hostname.
			http.Redirect(w, r, "https://web.example.com/", http.StatusPermanentRedirect)
			return
		}
		http.Redirect(w, r, "https://www.example.com/", http.StatusMovedPermanently)
	}))
	defer proxy.Close()

	s, _ := newRouted(t)
	// The real probe, at the test's proxy: its certificate is nobody's.
	s.engine.opts.RedirectProbe = nil
	s.engine.opts.ProxyTLSAddr = proxy.Listener.Addr().String()
	a := wwwRedirected("web:1.0")
	a.Path = "/shop"
	d := s.deploy(a)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0] != "web.example.com web.example.com /shop/" {
		t.Errorf("the proxy saw %q; want one request for web.example.com, by name, at the application's path", requests)
	}
	if got := s.probeEvents(d.ID); !strings.Contains(got, "warn: https://web.example.com/shop/ sends its visitors to www.example.com") {
		t.Errorf("probe events:\n%s", got)
	}
}

func TestTheProbeWaitsForAProxyThatCannotAnswerYet(t *testing.T) {
	p := &askedProxy{failures: 2, status: 200}
	s, _ := newRouted(t)
	s.engine.opts.RedirectProbe, s.engine.opts.RedirectRetry = p.probe, time.Millisecond
	d := s.deploy(web("web:1.0", 1))
	if p.attempts() != 3 {
		t.Errorf("asked %d times, want 3: twice refused, then answered", p.attempts())
	}
	if got := s.probeEvents(d.ID); got != "info: https://web.example.com/ answers HTTP 200 through the proxy" {
		t.Errorf("probe events = %q", got)
	}
	if p.asked[0] != DefaultProxyTLSAddr+" web.example.com " {
		t.Errorf("asked %q, want the compose setup's proxy, the hostname and no path", p.asked[0])
	}

	p = &askedProxy{failures: redirectAttempts + 5}
	s, _ = newRouted(t)
	s.engine.opts.RedirectProbe, s.engine.opts.RedirectRetry = p.probe, time.Millisecond
	d = s.deploy(web("web:1.1", 1))
	if p.attempts() != redirectAttempts {
		t.Errorf("asked %d times, want to give up after %d", p.attempts(), redirectAttempts)
	}
	if got := s.probeEvents(d.ID); got != "warn: https://web.example.com/ could not be asked through the proxy: connection refused" {
		t.Errorf("probe events = %q", got)
	}
}

func TestNothingIsAskedWithoutARoutedHostname(t *testing.T) {
	p := &askedProxy{status: 200}

	// No domain: nothing to ask.
	s, _ := newRouted(t)
	s.engine.opts.RedirectProbe = p.probe
	d := s.deploy(app("api", "api:1.0", 1))
	if p.attempts() != 0 || s.probeEvents(d.ID) != "" {
		t.Errorf("an application without a domain was asked %d times: %q", p.attempts(), s.probeEvents(d.ID))
	}

	// A hostname waiting for DNS is not in the proxy, so there is nothing
	// to ask it through.
	s, _ = newGated(t)
	s.engine.opts.RedirectProbe = p.probe
	s.dns.unresolved("web.example.com")
	d = s.deploy(web("web:1.0", 1))
	if p.attempts() != 0 || s.probeEvents(d.ID) != "" {
		t.Errorf("a hostname without DNS was asked %d times: %q", p.attempts(), s.probeEvents(d.ID))
	}

	// A folder is served by the proxy itself; it cannot redirect anywhere.
	s, _ = newStaticHarness(t)
	s.engine.opts.RedirectProbe = p.probe
	d = s.deployStatic(site("web"), map[string]string{"index.html": "<h1>hi</h1>"})
	if d.Status != api.StatusActive {
		t.Fatalf("static deployment: %s %q", d.Status, d.Error)
	}
	if p.attempts() != 0 {
		t.Errorf("a folder was asked %d times", p.attempts())
	}

	// Without a proxy there is nothing to ask through.
	h := newSupervised(t)
	h.engine.opts.RedirectProbe = p.probe
	d = h.deploy(web("web:1.0", 1))
	if p.attempts() != 0 || h.probeEvents(d.ID) != "" {
		t.Errorf("without a proxy the hostname was asked %d times: %q", p.attempts(), h.probeEvents(d.ID))
	}
}

func TestTheProbeRunsAfterTheDeploymentIsCompleted(t *testing.T) {
	s, _ := newRouted(t)
	asked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.engine.opts.RedirectProbe = func(ctx context.Context, _, _, _ string) (int, string, error) {
		once.Do(func() { close(asked) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return 200, "", nil
	}
	d, err := s.engine.Deploy(context.Background(), web("web:1.0", 1))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	<-asked
	// The probe is being asked: the deployment must be done by now, lock
	// and completed_at included, so that nobody waits for the answer.
	at, err := s.store.GetDeployment(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if at.Status != api.StatusActive || at.CompletedAt == nil {
		t.Errorf("while the probe runs the deployment is %s, completed_at=%v; want ACTIVE and completed", at.Status, at.CompletedAt)
	}
	if _, err := s.engine.Redeploy(context.Background(), "web", ""); err != nil {
		t.Errorf("a redeploy while the probe runs: %v; the application must be free", err)
	}
	close(release)
	s.engine.Wait()
	if got := s.probeEvents(d.ID); got != "info: https://web.example.com/ answers HTTP 200 through the proxy" {
		t.Errorf("probe events = %q", got)
	}
}
