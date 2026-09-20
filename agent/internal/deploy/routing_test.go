package deploy

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// fakeProxy records every Sync. onSync, when set, runs inside Sync — the
// moment a test can observe the rest of the system mid-switch.
type fakeProxy struct {
	mu     sync.Mutex
	syncs  [][]proxy.Route
	err    error
	onSync func(routes []proxy.Route)
}

func (p *fakeProxy) Sync(_ context.Context, routes []proxy.Route) error {
	p.mu.Lock()
	err, hook := p.err, p.onSync
	if err == nil {
		p.syncs = append(p.syncs, routes)
	}
	p.mu.Unlock()
	if hook != nil {
		hook(routes)
	}
	return err
}

func (p *fakeProxy) Status() proxy.Status { return proxy.Status{Enabled: true, Reachable: true} }

func (p *fakeProxy) failWith(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// upstreams returns what domain is currently routed to, sorted; nil if the
// domain has no route at all.
func (p *fakeProxy) upstreams(domain string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.syncs) == 0 {
		return nil
	}
	for _, r := range p.syncs[len(p.syncs)-1] {
		if r.Domain == domain {
			ups := append([]string{}, r.Upstreams...)
			sort.Strings(ups)
			return ups
		}
	}
	return nil
}

// history returns every distinct upstream set domain went through, in order.
func (p *fakeProxy) history(domain string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, routes := range p.syncs {
		for _, r := range routes {
			if r.Domain != domain {
				continue
			}
			ups := append([]string{}, r.Upstreams...)
			sort.Strings(ups)
			if s := strings.Join(ups, ","); len(out) == 0 || out[len(out)-1] != s {
				out = append(out, s)
			}
		}
	}
	return out
}

// storeFilterLatest selects the most recent deployment.
var storeFilterLatest = store.DeploymentFilter{Limit: 1}

func newRouted(t *testing.T) (*supervised, *fakeProxy) {
	s := newSupervised(t)
	p := &fakeProxy{}
	s.engine.opts.Proxy = p
	return s, p
}

func web(image string, replicas int) spec.App {
	a := app("web", image, replicas)
	a.Domain = "web.example.com"
	return a
}

func TestDeployRoutesDomainToReplicas(t *testing.T) {
	s, p := newRouted(t)
	d := s.deploy(web("web:1.0", 2))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}

	got := p.upstreams("web.example.com")
	want := []string{"shipwick_web_1_1:8080", "shipwick_web_1_2:8080"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("upstreams = %v, want %v (by container name, so a restarted replica is still found)", got, want)
	}

	var routed bool
	for _, e := range s.events(d.ID) {
		routed = routed || (e.Type == api.EventStep && strings.Contains(e.Message, "Routed https://web.example.com to 2 replicas"))
	}
	if !routed {
		t.Error("expected a step event for the traffic switch")
	}
}

func TestTrafficMovesBeforeTheCommitAndWhileTheOldReplicaStillRuns(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 1))

	var observed struct {
		sync.Mutex
		status     api.DeploymentStatus
		containers int
	}
	p.onSync = func(routes []proxy.Route) {
		for _, r := range routes {
			if len(r.Upstreams) == 1 && r.Upstreams[0] == "shipwick_web_2_1:8080" {
				all, _ := s.store.ListDeployments(context.Background(), storeFilterLatest)
				observed.Lock()
				if observed.status == "" {
					observed.status, observed.containers = all[0].Status, len(s.rt.Containers())
				}
				observed.Unlock()
			}
		}
	}
	s.deploy(web("web:1.1", 1))

	if observed.status != api.StatusHealthChecking {
		t.Errorf("traffic moved while the deployment was %s; a replica joins the rotation as soon as it is verified, long before the commit", observed.status)
	}
	if observed.containers != 2 {
		t.Errorf("%d containers existed at the switch; the old replica must still be running then, or a failed switch would be an outage", observed.containers)
	}
}

func TestRollingRedeployKeepsFullCapacityInRotation(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))
	s.advance(time.Second)
	s.deploy(web("web:1.1", 2))
	s.advance(time.Second)

	history := p.history("web.example.com")
	want := []string{
		"shipwick_web_1_1:8080,shipwick_web_1_2:8080", // v1
		"shipwick_web_1_2:8080,shipwick_web_2_1:8080", // replica 1 replaced
		"shipwick_web_2_1:8080,shipwick_web_2_2:8080", // replica 2 replaced
	}
	if strings.Join(history, " | ") != strings.Join(want, " | ") {
		t.Errorf("routing history:\n  got  %q\n  want %q", history, want)
	}
	for _, step := range history {
		if strings.Count(step, ",") != 1 {
			t.Errorf("rotation dropped below 2 replicas: %q", step)
		}
	}
}

func TestProxyFailureFailsTheDeploymentAndKeepsTheOldVersion(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 1))

	p.failWith(errors.New("cannot reach Caddy's admin endpoint: connection refused"))
	v2 := s.deploy(web("web:1.1", 1))
	p.failWith(nil)

	if v2.Status != api.StatusFailed || !strings.Contains(v2.Error, "could not route web.example.com") || !strings.Contains(v2.Error, "connection refused") {
		t.Fatalf("unexpected v2: %s %q", v2.Status, v2.Error)
	}
	containers := s.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("v1 must keep running and v2's containers must be gone: %+v", containers)
	}

	s.advance(time.Second)
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_1_1:8080" {
		t.Errorf("after the failure the domain must point at v1 again, got %v", got)
	}
}

func TestDomainConflict(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))

	other := app("other", "other:1.0", 1)
	other.Domain = "web.example.com"
	_, err := s.engine.Deploy(context.Background(), other)

	var conflict *DomainConflictError
	if !errors.As(err, &conflict) || conflict.Owner != `application "web"` {
		t.Fatalf("err = %v, want a DomainConflictError naming the owner", err)
	}
	if _, err := s.store.GetApplication(context.Background(), "other"); err == nil {
		t.Error("a refused deployment must leave no trace")
	}

	// The owner itself may of course deploy again, and may move elsewhere.
	if d := s.deploy(web("web:1.1", 1)); d.Status != api.StatusActive {
		t.Errorf("redeploying the owner: %s %q", d.Status, d.Error)
	}
	moved := web("web:1.2", 1)
	moved.Domain = "www.example.com"
	s.deploy(moved)
	if d := s.deploy(other); d.Status != api.StatusActive {
		t.Errorf("the domain was given up, so it should be free now: %s %q", d.Status, d.Error)
	}
}

func TestAgentRouteIsAlwaysServedAndItsDomainIsReserved(t *testing.T) {
	s, p := newRouted(t)
	s.engine.opts.ExtraRoutes = []proxy.Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true}}

	if err := s.engine.SyncProxy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.upstreams("agent.example.com"); len(got) != 1 || got[0] != "agent:9000" {
		t.Errorf("agent route = %v, want it present before any application exists", got)
	}

	thief := app("thief", "thief:1.0", 1)
	thief.Domain = "agent.example.com"
	_, err := s.engine.Deploy(context.Background(), thief)
	var conflict *DomainConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("err = %v; an application must not be able to take over the agent's hostname", err)
	}
}

func TestSupervisorTakesUnreadyReplicasOutOfRotation(t *testing.T) {
	s, p := newRouted(t)
	a := withHealth(web("web:1.0", 2))
	s.deploy(a)
	s.advance(time.Second)
	sick := s.container(t, 1)

	// Unhealthy → out.
	s.probes.setFailing(sick.IP, errors.New("HTTP 500"))
	for range 3 {
		s.advance(10 * time.Second)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_1_2:8080" {
		t.Fatalf("upstreams = %v; an unhealthy replica must stop receiving traffic", got)
	}

	// Restarted, passing again → back in.
	s.probes.setFailing(sick.IP, nil)
	s.advance(time.Second) // restart
	if got := p.upstreams("web.example.com"); len(got) != 1 {
		t.Errorf("upstreams = %v; a replica that was just restarted is 'starting' and must wait for its first passing check", got)
	}
	s.advance(time.Second) // probe passes
	s.advance(time.Second)
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("upstreams = %v; a recovered replica should rejoin", got)
	}

	// Crashed → out within a tick.
	s.rt.Crash(s.container(t, 2).ID, 1)
	s.advance(100 * time.Millisecond)
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_1_1:8080" {
		t.Errorf("upstreams = %v; a crashed replica must leave the rotation", got)
	}
}

func TestStopServes503BeforeStoppingAndStartRejoins(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))

	var runningAtSwitch int
	p.onSync = func(routes []proxy.Route) {
		for _, r := range routes {
			if r.Domain == "web.example.com" && len(r.Upstreams) == 0 && runningAtSwitch == 0 {
				for _, c := range s.rt.Containers() {
					if c.Running {
						runningAtSwitch++
					}
				}
			}
		}
	}
	if err := s.engine.Stop(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	p.onSync = nil

	if got := p.upstreams("web.example.com"); got == nil || len(got) != 0 {
		t.Errorf("upstreams = %v; a stopped application keeps its route (and certificate) with no upstreams → 503", got)
	}
	if runningAtSwitch != 2 {
		t.Errorf("%d replicas were still running when traffic was cut; cut first, stop second, so no request dies mid-flight", runningAtSwitch)
	}

	if err := s.engine.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("upstreams after start = %v", got)
	}
}

func TestStartWithHealthCheckWaitsForHealthy(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 1)))
	s.engine.Stop(context.Background(), "web")

	s.engine.Start(context.Background(), "web")
	if got := p.upstreams("web.example.com"); len(got) != 0 {
		t.Errorf("upstreams = %v; a freshly started replica with a health check is not routed to until it passes", got)
	}
	s.advance(time.Second) // probe passes
	s.advance(time.Second)
	if got := p.upstreams("web.example.com"); len(got) != 1 {
		t.Errorf("upstreams = %v; it should join once healthy", got)
	}
}

func TestDeleteRemovesTheRoute(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 1))
	if err := s.engine.Delete(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if got := p.upstreams("web.example.com"); got != nil {
		t.Errorf("route still present after delete: %v", got)
	}
}

func TestDomainWithoutProxyWarnsButDeploys(t *testing.T) {
	s := newSupervised(t) // no proxy configured
	d := s.deploy(web("web:1.0", 1))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s); a missing proxy is a warning, not a reason to refuse", d.Status, d.Error)
	}
	var warned bool
	for _, e := range s.events(d.ID) {
		warned = warned || (e.Level == api.LevelWarn && strings.Contains(e.Message, "web.example.com is not being served"))
	}
	if !warned {
		t.Error("the user should be told that their domain is not served")
	}
	if status := s.engine.ProxyStatus(); status.Enabled {
		t.Errorf("ProxyStatus = %+v, want disabled", status)
	}
}

func TestApplicationsWithoutDomainAreNotRouted(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(app("worker", "worker:1.0", 1))
	s.advance(time.Second)

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, routes := range p.syncs {
		if len(routes) != 0 {
			t.Errorf("unexpected routes for an application without a domain: %+v", routes)
		}
	}
}
