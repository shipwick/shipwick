package deploy

import (
	"context"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// noRuntime fails the test, by way of a nil interface, the moment anything
// is asked of Docker.
type noRuntime struct{ Runtime }

func TestScrapeServesWhatTheAgentAlreadyHasWithoutAskingDocker(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	s.now = time.Now().UTC().Truncate(time.Minute)
	s.rt.Clock = func() time.Time { return s.now }
	s.rt.CPUBusy, s.rt.MemoryUsed = 0.5, 200<<20
	s.deploy(app("my-api", "my-api:1.0", 2))
	s.rt.CrashImages["broken:1.0"] = true
	s.deploy(app("broken", "broken:1.0", 1))

	scrape := func() Scrape {
		t.Helper()
		docker := s.engine.rt
		s.engine.rt = noRuntime{}
		defer func() { s.engine.rt = docker }()
		sc, err := s.engine.Scrape(ctx, s.now)
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}

	// Before the supervisor's first pass only the database speaks.
	sc := scrape()
	if len(sc.Applications) != 2 || sc.Applications[0].Name != "broken" || sc.Applications[1].Name != "my-api" {
		t.Fatalf("applications = %+v; both, by name", sc.Applications)
	}
	broken, mine := sc.Applications[0], sc.Applications[1]
	if broken.Status != api.AppFailed || broken.Deployments != (DeploymentOutcomes{Failed: 1}) || !broken.Completed || len(broken.ReplicaStats) != 0 {
		t.Errorf("never deployed successfully: %+v", broken)
	}
	if mine.Observed || mine.Status != "" || mine.Replicas != (api.ReplicaCount{Desired: 2}) {
		t.Errorf("not looked at yet: %+v; only what is desired is known", mine)
	}
	if mine.Deployments != (DeploymentOutcomes{Succeeded: 1}) || !mine.Completed || mine.LastDuration <= 0 {
		t.Errorf("deployments = %+v, last took %s", mine.Deployments, mine.LastDuration)
	}

	s.advance(time.Second)
	for range 2 { // the first reading primes the CPU rate
		s.now = s.now.Add(30 * time.Second)
		s.engine.sample(ctx, s.now)
	}
	mine = scrape().Applications[1]
	if mine.Status != api.AppHealthy || mine.Replicas != (api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}) {
		t.Errorf("status = %q, replicas = %+v", mine.Status, mine.Replicas)
	}
	if len(mine.ReplicaStats) != 2 {
		t.Fatalf("replicas = %+v", mine.ReplicaStats)
	}
	if r := mine.ReplicaStats[1]; r.Replica != 2 || !r.Sampled || !near(r.CPUCores, 0.5, 0.01) || r.MemoryBytes != 200<<20 || r.MemoryLimitBytes != 256<<20 || r.Restarts != 0 {
		t.Errorf("replica 2 = %+v", r)
	}

	// A replica that died: restarted and counted; the other one's sample
	// ages until it no longer says anything about now.
	s.rt.Crash(s.container(t, 1).ID, 1)
	s.advance(time.Second)
	mine = scrape().Applications[1]
	if mine.Status != api.AppDegraded || mine.Replicas.Healthy != 1 {
		t.Errorf("status = %q, replicas = %+v", mine.Status, mine.Replicas)
	}
	s.advance(time.Second)
	if mine = scrape().Applications[1]; mine.ReplicaStats[0].Restarts != 1 || mine.Replicas.Running != 1 {
		// Running is what the supervisor saw before it restarted the replica.
		t.Errorf("replica 1 = %+v, replicas = %+v", mine.ReplicaStats[0], mine.Replicas)
	}
	s.advance(2 * time.Minute)
	if mine = scrape().Applications[1]; mine.ReplicaStats[1].Sampled || mine.Status != api.AppHealthy {
		t.Errorf("two minutes after the last sample: %+v, status %q", mine.ReplicaStats[1], mine.Status)
	}

	if err := s.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if mine = scrape().Applications[1]; mine.Status != api.AppStopped || mine.Replicas != (api.ReplicaCount{Desired: 2}) {
		t.Errorf("stopped: status = %q, replicas = %+v", mine.Status, mine.Replicas)
	}
}

func TestScrapeCarriesDiskAndAlerts(t *testing.T) {
	h := newHarness(t)
	h.engine.opts.DiskUsage = func() (api.DiskUsage, bool) { return api.DiskUsage{TotalBytes: 100, UsedBytes: 90}, true }
	h.engine.checkAlerts(context.Background(), time.Now())
	sc, err := h.engine.Scrape(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sc.Disk == nil || *sc.Disk != (api.DiskUsage{TotalBytes: 100, UsedBytes: 90}) || len(sc.Alerts) != 1 || sc.Alerts[0].Kind != api.AlertDisk {
		t.Errorf("disk = %+v, alerts = %+v", sc.Disk, sc.Alerts)
	}
}
