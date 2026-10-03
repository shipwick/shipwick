package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// listed is the application as GET /applications lists it.
func (h *harness) listed(name string) api.Application {
	h.t.Helper()
	apps, err := h.engine.Applications(context.Background())
	if err != nil {
		h.t.Fatalf("Applications: %v", err)
	}
	for _, a := range apps {
		if a.Name == name {
			return a
		}
	}
	h.t.Fatalf("%s is not among the applications", name)
	return api.Application{}
}

func TestAReplacedReplicaIsListedAsStoppingUntilItIsGone(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))
	old := s.container(t, 1)

	stopping := s.rt.HoldStops()
	v2 := s.begin(web("web:1.1", 1))
	<-stopping
	s.awaitCompleted(v2.ID)

	view, err := s.engine.Application(ctx, "web")
	if err != nil {
		t.Fatalf("Application: %v", err)
	}
	if len(view.Containers) != 2 {
		t.Fatalf("containers = %+v, want the new replica and the one on its way out", view.Containers)
	}
	for _, c := range view.Containers {
		if want := c.ID == old.ID; c.Stopping != want {
			t.Errorf("%s: stopping = %v, want %v", c.Name, c.Stopping, want)
		}
	}
	if view.Replicas.Running != 1 || view.Replicas.Healthy != 1 {
		t.Errorf("replicas = %+v; the one that is stopping is not a replica any more", view.Replicas)
	}

	s.rt.ReleaseStops()
	s.engine.Wait()
	view, _ = s.engine.Application(ctx, "web")
	if len(view.Containers) != 1 || view.Containers[0].Stopping {
		t.Errorf("containers once the old replica is gone: %+v", view.Containers)
	}
}

func TestTheListOfApplicationsNamesTheWorstCertificate(t *testing.T) {
	c := newCertified(t)
	c.dns.unresolved("api.example.com")
	c.deploy(withHostnames(web("web:1.0", 1)))
	c.deploy(app("worker", "worker:1.0", 1))

	// Every hostname that is served has a certificate with months to go; the
	// alias has no DNS record and is not served at all.
	for _, host := range []string{"web.example.com", "www.example.com", "example.net"} {
		c.certs.issue(host, c.now, 80*day)
	}
	c.check(certTick)
	got := c.listed("web").CertificateProblem
	if got == nil || got.Hostname != "api.example.com" || got.Status != api.CertWaitingForDNS || !strings.Contains(got.Message, "does not resolve") {
		t.Fatalf("certificate_problem = %+v, want the alias that waits for DNS, with why", got)
	}

	// The alias arrives and has no certificate yet, while the domain's is
	// running out: the one that needs somebody to act is the one named.
	c.dns.resolve("api.example.com")
	c.advance(hostnameRetryAfter)
	c.certs.issue("web.example.com", c.now, 10*day)
	c.check(certCheckEvery)
	got = c.listed("web").CertificateProblem
	if got == nil || got.Hostname != "web.example.com" || got.Status != api.CertExpiring || !strings.Contains(got.Message, "expires in 9 days") {
		t.Fatalf("certificate_problem = %+v, want the expiring domain before the alias that is being obtained", got)
	}

	// Renewed: what is left is the certificate the proxy is still obtaining.
	c.certs.issue("web.example.com", c.now, 80*day)
	c.check(certCheckEvery)
	got = c.listed("web").CertificateProblem
	if got == nil || got.Hostname != "api.example.com" || got.Status != api.CertObtaining {
		t.Fatalf("certificate_problem = %+v, want the alias whose certificate is being obtained", got)
	}

	c.certs.issue("api.example.com", c.now, 80*day)
	c.check(certCheckEvery)
	if got := c.listed("web").CertificateProblem; got != nil {
		t.Errorf("certificate_problem = %+v with every certificate in order", got)
	}
	if detail, _ := c.engine.Application(context.Background(), "web"); detail.CertificateProblem != nil {
		t.Errorf("the application's own view disagrees with the list: %+v", detail.CertificateProblem)
	}
	if got := c.listed("worker").CertificateProblem; got != nil {
		t.Errorf("an application without a hostname has a certificate problem: %+v", got)
	}
}

func TestACertificateNobodyHasLookedAtIsNotAProblem(t *testing.T) {
	c := newCertified(t)
	c.deploy(web("web:1.0", 1))
	if got := c.listed("web").CertificateProblem; got != nil {
		t.Errorf("certificate_problem = %+v before the first look; unknown is not out of order", got)
	}

	// Nor is one the proxy cannot be asked about.
	c.certs.down = context.DeadlineExceeded
	c.check(certTick)
	if got := c.listed("web").CertificateProblem; got != nil {
		t.Errorf("certificate_problem = %+v while the proxy does not answer", got)
	}

	h := newHarness(t)
	h.deploy(web("web:1.0", 1))
	if got := h.listed("web").CertificateProblem; got != nil {
		t.Errorf("certificate_problem = %+v on an agent without a proxy", got)
	}
}

func TestTheListOfApplicationsCountsEachApplicationsAlerts(t *testing.T) {
	s := newSupervised(t)
	a := app("my-api", "my-api:1.0", 2)
	a.Restart.Policy = spec.RestartNever
	s.deploy(a)
	s.deploy(app("worker", "worker:1.0", 1))

	if got := s.listed("my-api"); got.AlertCount != 0 || got.AlertSeverity != "" {
		t.Fatalf("alerts of a healthy application: %d, %q", got.AlertCount, got.AlertSeverity)
	}

	s.rt.Crash(s.container(t, 1).ID, 1)
	s.advance(time.Second)
	s.advance(5 * time.Minute)
	if got := s.listed("my-api"); got.AlertCount != 1 || got.AlertSeverity != api.SeverityWarning {
		t.Errorf("after five minutes: %d, %q; want its one warning", got.AlertCount, got.AlertSeverity)
	}

	// A second alert about the same application, less severe than the first
	// is about to become.
	now := s.now
	app, _ := s.store.GetApplication(context.Background(), "my-api")
	s.engine.raiseAlert(context.Background(), &app, alertKey{kind: api.AlertMemory, application: "my-api", replica: 2}, api.SeverityWarning, "memory", now)
	s.advance(time.Hour)
	if got := s.listed("my-api"); got.AlertCount != 2 || got.AlertSeverity != api.SeverityCritical {
		t.Errorf("after an hour: %d, %q; want both alerts and the higher severity", got.AlertCount, got.AlertSeverity)
	}
	if detail, _ := s.engine.Application(context.Background(), "my-api"); detail.AlertCount != 2 || detail.AlertSeverity != api.SeverityCritical {
		t.Errorf("the application's own view disagrees with the list: %d, %q", detail.AlertCount, detail.AlertSeverity)
	}
	if got := s.listed("worker"); got.AlertCount != 0 || got.AlertSeverity != "" {
		t.Errorf("another application's alerts were counted for worker: %d, %q", got.AlertCount, got.AlertSeverity)
	}
}
