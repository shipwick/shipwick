package deploy

import (
	"context"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// CPU usage is a rate: it takes two samples to know it. Docker will take both
// itself, a second apart, but then every metrics request costs a second.
// Instead the last sample of each container is remembered here, and a request
// that finds a recent enough one computes the rate against it — instantly.
// Only a first request (or one after a long pause) falls back to Docker's
// blocking two-sample call.
//
// Nothing runs in the background: with nobody watching, nothing is sampled.
const (
	// Samples closer together than this make a noisy rate; further apart than
	// sampleMaxAge, a stale one.
	sampleMinGap = 500 * time.Millisecond
	sampleMaxAge = time.Minute
)

type metricsCache struct {
	mu      sync.Mutex
	samples map[string]docker.StatsSample // by container ID
}

func newMetricsCache() *metricsCache {
	return &metricsCache{samples: map[string]docker.StatsSample{}}
}

// exchange stores cur and returns the sample it replaces, if any.
func (c *metricsCache) exchange(id string, cur docker.StatsSample) (docker.StatsSample, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.samples[id]
	c.samples[id] = cur
	for other, s := range c.samples { // containers come and go; do not keep them forever
		if cur.Read.Sub(s.Read) > 5*sampleMaxAge {
			delete(c.samples, other)
		}
	}
	return prev, ok
}

// Metrics returns the current resource usage of the application's replicas.
func (e *Engine) Metrics(ctx context.Context, name string) (api.Metrics, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.Metrics{}, err
	}
	if app.ActiveDeploymentID == nil {
		return api.Metrics{}, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return api.Metrics{}, err
	}
	replicas, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return api.Metrics{}, err
	}

	// In parallel: a first request blocks a second per replica otherwise.
	perReplica := make([]api.ReplicaMetrics, len(replicas))
	parallel(indexes(len(replicas)), func(i int) error {
		perReplica[i] = e.replicaMetrics(ctx, replicas[i], d.Spec.Resources)
		return nil
	})

	out := api.Metrics{Application: name, CollectedAt: time.Now().UTC(), Replicas: perReplica}
	for _, m := range perReplica {
		out.CPUPercent += m.CPUPercent
		out.CPULimitPercent += m.CPULimitPercent
		out.MemoryBytes += m.MemoryBytes
		out.MemoryLimitBytes += m.MemoryLimitBytes
	}
	return out, nil
}

// replicaMetrics never fails: a replica that cannot be measured — stopped,
// just removed — is reported with zeros rather than failing the whole view.
func (e *Engine) replicaMetrics(ctx context.Context, r store.Replica, limits spec.Resources) api.ReplicaMetrics {
	m := api.ReplicaMetrics{
		Replica:          r.Index,
		Container:        r.ContainerName,
		CPULimitPercent:  limits.CPU * 100, // cores → percent of one core, the unit of CPUPercent
		MemoryLimitBytes: limits.MemoryBytes,
	}

	cur, err := e.rt.Stats(ctx, r.ContainerID, false)
	if err != nil || cur.Read.IsZero() {
		return m // not running: Docker reports no reading
	}
	m.MemoryBytes = cur.MemoryBytes

	prev, ok := e.metrics.exchange(r.ContainerID, cur)
	if gap := cur.Read.Sub(prev.Read); ok && gap >= sampleMinGap && gap <= sampleMaxAge {
		m.CPUPercent = docker.CPUPercent(prev, cur)
		return m
	}
	// No usable earlier sample: let Docker take two, a second apart.
	if both, err := e.rt.Stats(ctx, r.ContainerID, true); err == nil && both.Previous != nil {
		m.CPUPercent = docker.CPUPercent(*both.Previous, both)
		m.MemoryBytes = both.MemoryBytes
		e.metrics.exchange(r.ContainerID, both)
	}
	return m
}

func indexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
