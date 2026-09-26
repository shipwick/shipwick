package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/pkg/api"
)

// who is what Docker's DNS answers for an application's own name: the replicas
// another application would reach at http://<app>:<port>.
func (s *supervised) who(app string) []string {
	return s.rt.Resolve(app)
}

func TestAnOrdinaryRolloutDoesNotReloadTheProxy(t *testing.T) {
	// Every config the proxy is given is a reload, and a reload resets
	// connections that are being established. A rollout that keeps domain and
	// port must therefore not produce one: replicas change behind the name.
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 3))
	s.advance(time.Second)
	before := p.configs()

	for _, image := range []string{"web:1.1", "web:1.2"} {
		if d := s.deploy(web(image, 3)); d.Status != api.StatusActive {
			t.Fatalf("deploy %s: %s (%s)", image, d.Status, d.Error)
		}
		s.advance(time.Second)
	}

	if after := p.configs(); after != before {
		t.Errorf("two rollouts gave the proxy %d new configurations; each is a reload", after-before)
	}
	if got := p.upstreams("web.example.com"); len(got) != 3 || !strings.HasPrefix(got[0], "shipwick_web_3_") {
		t.Errorf("serving = %v, want the three replicas of the third deployment", got)
	}
}

func TestARolloutRenamesOnlyTheNewcomers(t *testing.T) {
	// Changing a replica's names takes it off the services network for a
	// moment, which cuts whatever it is serving. Newcomers have nothing to
	// lose; a replica on its way out is stopped with its names on.
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 3))
	before := s.rt.NameChanges()

	s.deploy(web("web:1.1", 3))

	if got := s.rt.NameChanges() - before; got != 3 {
		t.Errorf("%d name changes for a rollout of 3 replicas, want 3: one per newcomer, none for the replicas they replace", got)
	}
}

func TestAnApplicationIsFoundByItsNameWithoutADomain(t *testing.T) {
	// An internal service: no domain, so the proxy never hears of it, and
	// still other applications reach it at http://orders:8080.
	s, p := newRouted(t)
	s.deploy(app("orders", "orders:1.0", 2))

	if got := s.who("orders"); len(got) != 2 {
		t.Errorf("orders resolves to %v, want both replicas", got)
	}
	for _, routes := range p.syncs {
		for _, r := range routes {
			if len(r.Backends) > 0 {
				t.Errorf("an application without a domain must not appear in the proxy: %+v", r)
			}
		}
	}

	s.deploy(app("orders", "orders:1.1", 2))
	if got := s.who("orders"); len(got) != 2 || !strings.HasPrefix(got[0], "shipwick_orders_2_") {
		t.Errorf("after a rollout orders resolves to %v", got)
	}
}

func TestTheNameIsNeverWithoutAReplicaDuringARollout(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 1))

	// Sampled at every Sync of the rollout — the moments routing changes.
	var seen []int
	p.onSync = func([]proxy.Route) { seen = append(seen, len(s.who("web"))) }
	s.deploy(web("web:1.1", 1))

	if len(seen) == 0 {
		t.Fatal("the rollout never synced routing")
	}
	if slices.Min(seen) < 1 {
		t.Errorf("replicas behind the name during the rollout: %v; a single-replica application must never be unreachable", seen)
	}
}

func TestACrashedReplicaLosesItsNameAtOnceAndEarnsItBack(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 2)))
	s.advance(time.Second)
	victim := s.container(t, 1)

	s.rt.Crash(victim.ID, 137)
	// No tick yet: Docker's DNS stops answering for a stopped container by
	// itself, which is faster than any supervisor.
	if got := s.who("web"); len(got) != 1 || got[0] != "shipwick_web_1_2" {
		t.Fatalf("right after the crash web resolves to %v, want only the survivor", got)
	}

	s.probes.setFailing(victim.IP, errors.New("connection refused"))
	s.advance(time.Second) // notices the exit
	s.advance(time.Second) // backoff elapsed: restarts it
	if !s.container(t, 1).Running {
		t.Fatal("replica was not restarted")
	}
	if got := s.who("web"); len(got) != 1 {
		t.Errorf("a restarted replica is findable before it is healthy: %v", got)
	}

	s.probes.setFailing(victim.IP, nil)
	s.advance(10 * time.Second) // its health check passes
	s.advance(time.Second)
	if got := s.who("web"); len(got) != 2 {
		t.Errorf("once healthy it answers to the name again; got %v", got)
	}
}

func TestReplicasFromBeforeTheServicesNetworkAreNamedBeforeTheProxyLooksForThem(t *testing.T) {
	// Upgrading the agent: the replicas it finds were created by a version
	// that knew no services network. They must carry their names by the time
	// the proxy is switched from container lists to names, or the upgrade
	// answers 503 until someone redeploys.
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))
	for _, c := range s.rt.Containers() {
		s.rt.LeaveServicesNetwork(c.ID)
	}
	s.engine.mu.Lock()
	s.engine.names = map[string][]string{} // a freshly started agent knows nothing
	s.engine.mu.Unlock()

	var atSync []int
	p.onSync = func([]proxy.Route) { atSync = append(atSync, len(s.who("web"))) }
	if err := s.engine.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := s.engine.SyncProxy(context.Background()); err != nil {
		t.Fatalf("SyncProxy: %v", err)
	}

	if len(atSync) == 0 || atSync[0] != 2 {
		t.Errorf("replicas behind the name when the proxy was told to use it: %v, want 2", atSync)
	}
}

func TestAnAgentRestartDoesNotRenameReplicas(t *testing.T) {
	// Renaming cuts connections. An agent that comes back must recognize the
	// names its predecessor gave and leave them alone.
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 2))
	before := s.rt.NameChanges()

	s.engine.mu.Lock()
	s.engine.names = map[string][]string{}
	s.engine.mu.Unlock()
	if err := s.engine.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := s.engine.SyncProxy(context.Background()); err != nil {
		t.Fatalf("SyncProxy: %v", err)
	}

	if got := s.rt.NameChanges() - before; got != 0 {
		t.Errorf("%d replicas were renamed by an agent restart", got)
	}
}

func TestANewPortIsASecondBackendWhileTheRolloutLasts(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))

	moved := web("web:2.0", 2)
	moved.Port = 9090
	var mixed bool
	p.onSync = func(routes []proxy.Route) {
		for _, r := range routes {
			if r.Domain == "web.example.com" && len(r.Backends) == 2 {
				mixed = true
			}
		}
	}
	if d := s.deploy(moved); d.Status != api.StatusActive {
		t.Fatalf("deploy: %s (%s)", d.Status, d.Error)
	}

	if !mixed {
		t.Error("while both versions serve, the proxy must know both ports")
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 || !strings.HasSuffix(got[0], ":9090") {
		t.Errorf("after the rollout: %v", got)
	}
}

func TestStoppedApplicationHasNoNameAndStartedReplicasEarnTheirs(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 2)))
	s.advance(time.Second)
	var ips []string // a stopped container has no address to read
	for _, c := range s.rt.Containers() {
		ips = append(ips, c.IP)
	}

	if err := s.engine.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := s.who("web"); len(got) != 0 {
		t.Errorf("a stopped application resolves to %v", got)
	}
	// The proxy must not be left asking for a name nobody carries: Docker's
	// DNS forwards unknown names, and every request would wait for that.
	for _, r := range p.syncs[len(p.syncs)-1] {
		if r.Domain == "web.example.com" && len(r.Backends) != 0 {
			t.Errorf("the route of a stopped application still names a backend: %+v", r)
		}
	}

	for _, ip := range ips {
		s.probes.setFailing(ip, errors.New("starting up"))
	}
	if err := s.engine.Start(context.Background(), "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.advance(time.Second)
	if got := s.who("web"); len(got) != 0 {
		t.Errorf("started replicas are findable before they are healthy: %v", got)
	}

	for _, ip := range ips {
		s.probes.setFailing(ip, nil)
	}
	s.advance(10 * time.Second)
	s.advance(time.Second)
	if got := s.who("web"); len(got) != 2 {
		t.Errorf("healthy again, web resolves to %v", got)
	}
}

func TestFailingToNameAReplicaFailsTheDeployment(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))

	s.rt.NamesErr = errors.New("network shipwick-services not found")
	d := s.deploy(web("web:1.1", 2))
	s.rt.NamesErr = nil

	if d.Status == api.StatusActive {
		t.Fatal("a replica that cannot be given its name serves nobody; the deployment must not succeed")
	}
	if !strings.Contains(d.Error, "shipwick-services not found") {
		t.Errorf("error = %q, want the cause", d.Error)
	}
	s.advance(time.Second)
	if got := p.upstreams("web.example.com"); len(got) != 2 || !strings.HasPrefix(got[0], "shipwick_web_1_") {
		t.Errorf("after the failure: %v, want the two replicas of the version that worked", got)
	}
}

func TestNamingIsNotDoneTwiceAtOnce(t *testing.T) {
	// Giving a replica its names is two calls to Docker — leave the network,
	// join it again. The supervisor syncs routing every tick and a rollout
	// syncs it at every step; if both rename the same replica at the same
	// time, the second one finds the endpoint already there and fails.
	s, _ := newRouted(t)
	s.rt.NamesDelay = 2 * time.Millisecond
	s.deploy(web("web:1.0", 2))
	s.engine.opts.SuperviseInterval = time.Millisecond
	s.engine.sup.inlineProbes = false
	s.engine.StartSupervisor()

	for i := range 6 {
		image := "web:2." + string(rune('0'+i))
		if d := s.deploy(web(image, 2)); d.Status != api.StatusActive {
			t.Fatalf("deploy %s: %s (%s)", image, d.Status, d.Error)
		}
	}
	if got := s.who("web"); len(got) != 2 {
		t.Errorf("web resolves to %v", got)
	}
}

func TestAPredecessorOutlivesItsSuccessorsNamingByTheSettleTime(t *testing.T) {
	// The proxy asks who stands behind a name about once a second. Stop the
	// old replica sooner after naming the new one, and for the rest of that
	// second the proxy only knows a replica that is gone.
	//
	// It must hold no matter who did the naming: the supervisor's tick syncs
	// routing too, and may give the newcomer its names a moment before the
	// rollout gets to it.
	const settle = 40 * time.Millisecond
	s, _ := newRouted(t)
	s.engine.opts.NameSettle = settle
	s.rt.NamesDelay = time.Millisecond
	s.deploy(web("web:1.0", 2))
	s.engine.opts.SuperviseInterval = time.Millisecond
	s.engine.sup.inlineProbes = false
	s.engine.StartSupervisor()

	for deployment := 2; deployment <= 6; deployment++ {
		if d := s.deploy(web("web:1."+string(rune('0'+deployment)), 2)); d.Status != api.StatusActive {
			t.Fatalf("deployment %d: %s (%s)", deployment, d.Status, d.Error)
		}
		for replica := 1; replica <= 2; replica++ {
			successor := docker.ContainerName("web", deployment, replica)
			predecessor := docker.ContainerName("web", deployment-1, replica)
			named, stopped := s.rt.NamedAt(successor), s.rt.StoppedAt(predecessor)
			if named.IsZero() || stopped.IsZero() {
				t.Fatalf("%s named at %v, %s stopped at %v", successor, named, predecessor, stopped)
			}
			if gap := stopped.Sub(named); gap < settle {
				t.Errorf("%s was stopped %s after %s got its names; the proxy needs %s to learn of the newcomer", predecessor, gap, successor, settle)
			}
		}
	}
}
