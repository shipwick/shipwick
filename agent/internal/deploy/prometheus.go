package deploy

import (
	"context"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// A metrics scrape is answered from what the agent already has: the database,
// read in one transaction; the replicas' usage as the sampler last recorded
// it; their state as the supervisor last saw it. It never asks Docker — a
// scraper comes every few seconds, for ever, and must cost next to nothing
// and keep answering when the daemon is slow.

// Scrape is the state of the server for GET /metrics.
type Scrape struct {
	Applications []ScrapeApplication // by name
	Disk         *api.DiskUsage      // nil where it cannot be measured
	Alerts       []api.Alert
}

type ScrapeApplication struct {
	Name string
	// Status is empty, and Replicas holds only the desired count, for an
	// application the supervisor has not looked at yet: the first second
	// after the agent starts.
	Status   api.ApplicationStatus
	Replicas api.ReplicaCount
	Observed bool
	// Deployments counts the finished deployments by outcome.
	Deployments DeploymentOutcomes
	// LastDuration is how long the most recently completed deployment took;
	// Completed is false when there is none.
	LastDuration time.Duration
	Completed    bool
	// ReplicaStats lists the replicas of the active deployment, by number.
	ReplicaStats []ScrapeReplica
}

type DeploymentOutcomes struct {
	Succeeded, Failed, RolledBack int
}

type ScrapeReplica struct {
	Replica  int
	Restarts int
	// Sampled is false for a replica without a recent sample: it is not
	// running, or started less than a minute ago.
	Sampled          bool
	CPUCores         float64
	MemoryBytes      int64
	MemoryLimitBytes int64 // 0: no limit
}

// Scrape collects the state of the server for a metrics scrape.
func (e *Engine) Scrape(ctx context.Context, now time.Time) (Scrape, error) {
	// A sample older than two intervals is of a replica that has stopped
	// since, not its usage now.
	db, err := e.store.Scrape(ctx, InFlightStatuses(), now.Add(-e.opts.SampleInterval*5/2))
	if err != nil {
		return Scrape{}, err
	}
	type replicaKey struct {
		app     int64
		replica int
	}
	samples := make(map[replicaKey]store.MetricSample, len(db.Samples))
	for _, m := range db.Samples {
		samples[replicaKey{m.ApplicationID, m.Replica}] = m
	}
	replicas := map[int64][]store.Replica{}
	for _, r := range db.Replicas {
		replicas[r.DeploymentID] = append(replicas[r.DeploymentID], r)
	}
	outcomes := map[string]DeploymentOutcomes{}
	for _, c := range db.Counts {
		o := outcomes[c.Application]
		switch c.Status {
		case api.StatusActive, api.StatusSuperseded:
			o.Succeeded += c.Count
		case api.StatusFailed:
			o.Failed += c.Count
		case api.StatusRolledBack:
			o.RolledBack += c.Count
		}
		outcomes[c.Application] = o
	}
	observed := map[string]observation{}
	e.alerts.locked(func() {
		for name, o := range e.alerts.observed {
			observed[name] = o
		}
	})

	out := Scrape{Disk: e.Disk(), Alerts: e.Alerts(), Applications: make([]ScrapeApplication, 0, len(db.Applications))}
	for _, app := range db.Applications { // ordered by name
		a := ScrapeApplication{Name: app.Name, Deployments: outcomes[app.Name], Observed: true}
		a.LastDuration, a.Completed = db.LastDuration[app.Name]
		var crashLoop bool
		if app.ActiveDeploymentID != nil {
			d := db.Active[*app.ActiveDeploymentID]
			if d.StaticDigest == "" {
				a.Replicas.Desired = d.Spec.Replicas
			}
			for _, r := range replicas[d.ID] {
				stats := ScrapeReplica{Replica: r.Index, Restarts: r.Restarts, MemoryLimitBytes: d.Spec.Resources.MemoryBytes}
				if m, ok := samples[replicaKey{app.ID, r.Index}]; ok {
					stats.Sampled, stats.CPUCores, stats.MemoryBytes = true, m.CPUPercent/100, m.MemoryBytes
				}
				a.ReplicaStats = append(a.ReplicaStats, stats)
			}
			if app.DesiredState == api.DesiredRunning && d.StaticDigest == "" {
				seen, ok := observed[app.Name]
				a.Observed = ok
				a.Replicas.Running, a.Replicas.Healthy, crashLoop = seen.replicas.Running, seen.replicas.Healthy, seen.crashLoop
			}
		}
		if a.Observed {
			a.Status = applicationStatus(app.ActiveDeploymentID != nil, app.DesiredState, a.Replicas, db.InFlight[app.Name], crashLoop)
		}
		out.Applications = append(out.Applications, a)
	}
	return out, nil
}
