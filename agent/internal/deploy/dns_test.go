package deploy

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/pkg/api"
)

// serverAddress is where the harness's server is; every hostname resolves
// to it unless a test says otherwise.
const serverAddress = "203.0.113.10"

// fakeResolver stands in for DNS. It counts lookups, and can be made to
// block, so that a test can see what else waits on a slow resolver.
type fakeResolver struct {
	mu        sync.Mutex
	addresses map[string][]string // hostnames that resolve elsewhere
	errs      map[string]error    // hostnames whose lookup fails
	calls     map[string]int
	block     chan struct{} // when set, every lookup waits on it
	entered   chan struct{} // closed by the first lookup that waits
	enterOnce *sync.Once
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{addresses: map[string][]string{}, errs: map[string]error{}, calls: map[string]int{}}
}

func (f *fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	f.mu.Lock()
	f.calls[host]++
	addrs, elsewhere := f.addresses[host]
	err := f.errs[host]
	block, entered, once := f.block, f.entered, f.enterOnce
	f.mu.Unlock()

	if block != nil {
		once.Do(func() { close(entered) })
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch {
	case err != nil:
		return nil, err
	case elsewhere:
		return addrs, nil
	}
	return []string{serverAddress}, nil
}

// resolve makes host resolve to addrs; none puts it back on the server.
func (f *fakeResolver) resolve(host string, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.errs, host)
	if len(addrs) == 0 {
		delete(f.addresses, host)
		return
	}
	f.addresses[host] = addrs
}

// unresolved makes host answer "no such host", the way a record that does
// not exist yet does.
func (f *fakeResolver) unresolved(host string) {
	f.fail(host, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true})
}

func (f *fakeResolver) fail(host string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[host] = err
}

func (f *fakeResolver) lookups(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[host]
}

// blockLookups makes every lookup wait until release is called. entered is
// closed once the first lookup is waiting.
func (f *fakeResolver) blockLookups() (entered <-chan struct{}, release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	block, in := make(chan struct{}), make(chan struct{})
	f.block, f.entered, f.enterOnce = block, in, &sync.Once{}
	return in, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.block == block {
			f.block = nil
			close(block)
		}
	}
}

// newGated is a routed harness whose hostname cache runs on the synthetic
// clock, so a test can age a verdict without waiting for it.
func newGated(t *testing.T) (*supervised, *fakeProxy) {
	s, p := newRouted(t)
	s.engine.dns.now = func() time.Time { return s.now }
	return s, p
}

// hasRoute reports whether the proxy's last configuration mentions host in
// any role.
func (p *fakeProxy) hasRoute(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.syncs) == 0 {
		return false
	}
	for _, r := range p.syncs[len(p.syncs)-1] {
		for _, h := range routeHostnames(r) {
			if h == host {
				return true
			}
		}
	}
	return false
}

// steps joins a deployment's step events, warnings included, one per line.
func (h *harness) steps(id int64) string {
	var lines []string
	for _, e := range h.events(id) {
		if e.Type == api.EventStep {
			lines = append(lines, e.Level+": "+e.Message)
		}
	}
	return strings.Join(lines, "\n")
}

func TestAHostnameWithoutDNSIsNotRoutedUntilItPointsHere(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("web.example.com")

	d := s.deploy(web("web:1.0", 2))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s); DNS gates the proxy, not the deployment", d.Status, d.Error)
	}
	if p.hasRoute("web.example.com") {
		t.Error("a hostname that does not resolve must not reach the proxy: Caddy would ask for a certificate and burn the rate limit")
	}
	steps := s.steps(d.ID)
	if strings.Contains(steps, "Routed https://") {
		t.Errorf("the deployment claims the domain is routed:\n%s", steps)
	}
	for _, want := range []string{
		"warn: Routing https://web.example.com is waiting for DNS: does not resolve yet; it is served, and its certificate obtained, once the record points at this server",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps lack %q:\n%s", want, steps)
		}
	}

	// The record is created; the next look at it finds the server.
	s.dns.resolve("web.example.com")
	s.advance(time.Second)
	if p.hasRoute("web.example.com") {
		t.Fatal("a negative verdict is trusted for hostnameRetryAfter; the proxy was told too soon")
	}
	s.advance(hostnameRetryAfter)
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Fatalf("upstreams = %v; once DNS points here the domain must be served", got)
	}
	s.advance(time.Second)
	s.advance(hostnameReadyFor)
	const announced = "web.example.com now points at this server and is being served"
	if n := strings.Count(strings.Join(s.appEvents(t, "web"), "\n"), announced); n != 1 {
		t.Errorf("%q was recorded %d times, want exactly once", announced, n)
	}
}

func TestAHostnameThatPointsElsewhereIsNotRouted(t *testing.T) {
	s, p := newGated(t)
	s.dns.resolve("web.example.com", "104.21.5.6")

	d := s.deploy(web("web:1.0", 1))
	if p.hasRoute("web.example.com") {
		t.Error("a hostname that points at another server must not reach the proxy")
	}
	want := "waiting for DNS: resolves to 104.21.5.6, not to this server (" + serverAddress + ")"
	if steps := s.steps(d.ID); !strings.Contains(steps, want) {
		t.Errorf("steps lack %q:\n%s", want, steps)
	}
}

func TestALookupFailureHoldsTheHostnameBackWithTheError(t *testing.T) {
	s, p := newGated(t)
	s.dns.fail("web.example.com", errors.New("lookup web.example.com: i/o timeout"))

	d := s.deploy(web("web:1.0", 1))
	if p.hasRoute("web.example.com") {
		t.Error("a hostname that could not be resolved must not reach the proxy")
	}
	if steps := s.steps(d.ID); !strings.Contains(steps, "waiting for DNS: could not be resolved: lookup web.example.com: i/o timeout") {
		t.Errorf("steps lack the resolver's error:\n%s", steps)
	}
}

func TestAliasesAndRedirectsWaitForTheirOwnDNS(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("api.example.com")
	s.dns.resolve("example.net", "104.21.5.6")

	d := s.deploy(withHostnames(web("web:1.0", 1)))
	r := p.route(t, "web.example.com")
	if len(r.Aliases) != 0 || strings.Join(r.Redirects, ",") != "www.example.com" {
		t.Errorf("route = %+v; each alias and redirect is gated on its own", r)
	}
	steps := s.steps(d.ID)
	if !strings.Contains(steps, "info: Routed https://web.example.com to 1 replica") {
		t.Errorf("the domain itself is served and must be announced as such:\n%s", steps)
	}
	for _, want := range []string{
		"warn: api.example.com does not resolve yet;",
		"warn: example.net resolves to 104.21.5.6, not to this server (" + serverAddress + ");",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps lack %q:\n%s", want, steps)
		}
	}

	s.dns.resolve("api.example.com")
	s.advance(hostnameRetryAfter)
	if r := p.route(t, "web.example.com"); strings.Join(r.Aliases, ",") != "api.example.com" {
		t.Errorf("route = %+v; the alias should join once its DNS points here", r)
	}
	if events := strings.Join(s.appEvents(t, "web"), "\n"); !strings.Contains(events, "api.example.com now points at this server and is being served") {
		t.Errorf("app events lack the alias's arrival:\n%s", events)
	}
}

func TestADomainWithoutDNSHoldsBackItsAliasesAndRedirectsToo(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("web.example.com")

	s.deploy(withHostnames(web("web:1.0", 1)))
	for _, host := range []string{"web.example.com", "api.example.com", "www.example.com", "example.net"} {
		if p.hasRoute(host) {
			t.Errorf("%s reached the proxy; the route waits for its domain, which the redirects point to", host)
		}
	}
}

func TestTheAgentsOwnRoutesAreNeverGated(t *testing.T) {
	s, p := newGated(t)
	s.dns.unresolved("agent.example.com")
	s.engine.opts.ExtraRoutes = []proxy.Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}}}

	if err := s.engine.SyncProxy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.upstreams("agent.example.com"); len(got) != 1 {
		t.Errorf("agent route = %v; holding it back could lock the operator out", got)
	}
	if n := s.dns.lookups("agent.example.com"); n != 0 {
		t.Errorf("the agent's hostname was looked up %d times; it is the operator's, not a deploy.yaml's", n)
	}
}

func TestWithoutKnownServerAddressesResolvingAnywhereIsEnough(t *testing.T) {
	s, p := newGated(t)
	s.engine.opts.ServerAddresses = nil
	s.dns.resolve("web.example.com", "104.21.5.6")

	d := s.deploy(web("web:1.0", 1))
	if got := p.upstreams("web.example.com"); len(got) != 1 {
		t.Errorf("upstreams = %v; with no addresses to compare against, resolving is all that can be asked", got)
	}
	if steps := s.steps(d.ID); !strings.Contains(steps, "info: Routed https://web.example.com to 1 replica") {
		t.Errorf("steps:\n%s", steps)
	}
}

func TestAVerdictIsTrustedForItsTTL(t *testing.T) {
	s, _ := newGated(t)
	s.deploy(web("web:1.0", 2)) // several syncs: the rollout's, then the ticks'
	for range 3 {
		s.advance(time.Second)
	}
	if n := s.dns.lookups("web.example.com"); n != 1 {
		t.Fatalf("%d lookups within hostnameReadyFor, want 1", n)
	}
	s.advance(hostnameReadyFor)
	if n := s.dns.lookups("web.example.com"); n != 2 {
		t.Errorf("%d lookups after hostnameReadyFor, want 2", n)
	}

	// A hostname that is not ready yet is asked about sooner.
	s.dns.unresolved("other.example.com")
	other := app("other", "other:1.0", 1)
	other.Domain = "other.example.com"
	s.deploy(other)
	s.advance(hostnameRetryAfter / 2)
	if n := s.dns.lookups("other.example.com"); n != 1 {
		t.Fatalf("%d lookups within hostnameRetryAfter, want 1", n)
	}
	s.advance(hostnameRetryAfter / 2)
	if n := s.dns.lookups("other.example.com"); n != 2 {
		t.Errorf("%d lookups after hostnameRetryAfter, want 2", n)
	}
}

func TestLookupsDoNotHoldTheRoutingLock(t *testing.T) {
	// A resolver that does not answer must not stop anyone from renaming a
	// replica: that is what the lock is for, and a lookup can take its whole
	// timeout.
	s, _ := newGated(t)
	s.deploy(web("web:1.0", 1))
	victim := s.container(t, 1)

	s.now = s.now.Add(hostnameReadyFor) // the verdict is stale: the next sync looks it up
	entered, release := s.dns.blockLookups()
	synced := make(chan error, 1)
	go func() { synced <- s.engine.SyncProxy(context.Background()) }()
	<-entered

	// Would deadlock, and the test time out, if the lookup held e.routing.
	if err := s.engine.startNameless(context.Background(), victim.ID); err != nil {
		t.Errorf("startNameless while a lookup is in flight: %v", err)
	}
	release()
	if err := <-synced; err != nil {
		t.Errorf("SyncProxy: %v", err)
	}
}
