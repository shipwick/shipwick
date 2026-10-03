package store

import (
	"context"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestRecentSamplesAreRawAndOrdered(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	other, _ := s.CreateDeployment(ctx, testApp("web", "nginx:1"), time.Now())
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return base.Add(time.Duration(i) * 30 * time.Second) }
	err := s.AddSamples(ctx, []MetricSample{
		{ApplicationID: d.ApplicationID, Replica: 2, At: at(2), MemoryBytes: 22},
		{ApplicationID: d.ApplicationID, Replica: 1, At: at(2), MemoryBytes: 12},
		{ApplicationID: d.ApplicationID, Replica: 1, At: at(1), MemoryBytes: 11},
		{ApplicationID: d.ApplicationID, Replica: 1, At: at(0), MemoryBytes: 10},
		{ApplicationID: other.ApplicationID, Replica: 1, At: at(2), MemoryBytes: 99},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentSamples(ctx, d.ApplicationID, at(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].MemoryBytes != 11 || got[1].MemoryBytes != 12 || got[2].MemoryBytes != 22 || !got[0].At.Equal(at(1)) {
		t.Errorf("samples = %+v; this application's, from the given time on, by replica and time", got)
	}
}

func TestScrapeReadsWhatAScrapeReports(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	transition := func(id int64, to api.DeploymentStatus) {
		t.Helper()
		if err := s.TransitionDeployment(ctx, id, api.StatusPending, to, ""); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}

	// api: one superseded deployment, an active one that took 12 seconds,
	// and one in flight. web: a deployment that failed.
	first, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), start)
	transition(first.ID, api.StatusHealthy)
	if _, err := s.ActivateDeployment(ctx, first.ID, start); err != nil {
		t.Fatal(err)
	}
	s.CompleteDeployment(ctx, first.ID, start.Add(5*time.Second))
	second, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), start.Add(time.Minute))
	transition(second.ID, api.StatusHealthy)
	if _, err := s.ActivateDeployment(ctx, second.ID, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.CompleteDeployment(ctx, second.ID, start.Add(time.Minute+12*time.Second))
	s.AddReplica(ctx, Replica{DeploymentID: first.ID, Index: 1, ContainerID: "old", ContainerName: "shipwick_api_1_1"}, start)
	s.AddReplica(ctx, Replica{DeploymentID: second.ID, Index: 1, ContainerID: "c1", ContainerName: "shipwick_api_2_1"}, start)
	s.AddReplica(ctx, Replica{DeploymentID: second.ID, Index: 2, ContainerID: "c2", ContainerName: "shipwick_api_2_2"}, start)
	s.AddReplica(ctx, Replica{DeploymentID: second.ID, Index: 3, ContainerID: "gone", ContainerName: "shipwick_api_2_3"}, start)
	s.MarkReplicaRemoved(ctx, "gone", start)
	s.IncrementReplicaRestarts(ctx, "c2")
	if _, err := s.CreateDeployment(ctx, testApp("api", "nginx:3"), start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	failed, _ := s.CreateDeployment(ctx, testApp("web", "nginx:1"), start)
	transition(failed.ID, api.StatusFailed)
	s.CompleteDeployment(ctx, failed.ID, start.Add(3*time.Second))

	now := start.Add(2 * time.Hour)
	err := s.AddSamples(ctx, []MetricSample{
		{ApplicationID: first.ApplicationID, Replica: 1, At: now.Add(-30 * time.Second), CPUPercent: 10, MemoryBytes: 100},
		{ApplicationID: first.ApplicationID, Replica: 1, At: now, CPUPercent: 20, MemoryBytes: 200},
		{ApplicationID: first.ApplicationID, Replica: 2, At: now.Add(-10 * time.Minute), CPUPercent: 30, MemoryBytes: 300},
	})
	if err != nil {
		t.Fatal(err)
	}

	sc, err := s.Scrape(ctx, []api.DeploymentStatus{api.StatusPending}, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(sc.Applications) != 2 || sc.Applications[0].Name != "api" {
		t.Fatalf("applications = %+v", sc.Applications)
	}
	if d, ok := sc.Active[second.ID]; len(sc.Active) != 1 || !ok || d.Image != "nginx:2" || d.Spec.Name != "api" {
		t.Errorf("active deployments = %+v; the one of api, with its spec", sc.Active)
	}
	if len(sc.Replicas) != 2 || sc.Replicas[0].ContainerID != "c1" || sc.Replicas[1].Restarts != 1 {
		t.Errorf("replicas = %+v; those of the active deployment that still exist", sc.Replicas)
	}
	if !sc.InFlight["api"] || sc.InFlight["web"] {
		t.Errorf("in flight = %v", sc.InFlight)
	}
	counts := map[string]int{}
	for _, c := range sc.Counts {
		counts[c.Application+" "+string(c.Status)] = c.Count
	}
	if len(counts) != 4 || counts["api SUPERSEDED"] != 1 || counts["api ACTIVE"] != 1 || counts["api PENDING"] != 1 || counts["web FAILED"] != 1 {
		t.Errorf("counts = %v", counts)
	}
	if sc.LastDuration["api"] != 12*time.Second || sc.LastDuration["web"] != 3*time.Second {
		t.Errorf("last durations = %v; the most recently completed deployment of each", sc.LastDuration)
	}
	if len(sc.Samples) != 1 || sc.Samples[0].Replica != 1 || sc.Samples[0].CPUPercent != 20 || sc.Samples[0].MemoryBytes != 200 || !sc.Samples[0].At.Equal(now) {
		t.Errorf("samples = %+v; the latest of each replica, none older than asked for", sc.Samples)
	}
}
