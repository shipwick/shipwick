package deploy

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/spec"
)

func near(got, want, tolerance float64) bool { return math.Abs(got-want) <= tolerance }

func TestCPUPercent(t *testing.T) {
	base := docker.StatsSample{CPUTotal: 1_000_000_000, SystemCPU: 100_000_000_000, OnlineCPUs: 4}
	tests := []struct {
		name string
		cur  docker.StatsSample
		want float64
	}{
		// Over 1s of wall time a 4-CPU host accumulates 4s of system CPU time.
		{"idle", docker.StatsSample{CPUTotal: 1_000_000_000, SystemCPU: 104_000_000_000, OnlineCPUs: 4}, 0},
		{"half a core", docker.StatsSample{CPUTotal: 1_500_000_000, SystemCPU: 104_000_000_000, OnlineCPUs: 4}, 50},
		{"one core", docker.StatsSample{CPUTotal: 2_000_000_000, SystemCPU: 104_000_000_000, OnlineCPUs: 4}, 100},
		{"two cores read as 200", docker.StatsSample{CPUTotal: 3_000_000_000, SystemCPU: 104_000_000_000, OnlineCPUs: 4}, 200},
		// A restarted container starts counting from zero again.
		{"counter went backwards", docker.StatsSample{CPUTotal: 5, SystemCPU: 104_000_000_000, OnlineCPUs: 4}, 0},
		{"no time passed", docker.StatsSample{CPUTotal: 2_000_000_000, SystemCPU: 100_000_000_000, OnlineCPUs: 4}, 0},
		{"no cpu count", docker.StatsSample{CPUTotal: 2_000_000_000, SystemCPU: 104_000_000_000}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := docker.CPUPercent(base, tt.cur); !near(got, tt.want, 0.001) {
				t.Errorf("CPUPercent = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMetrics(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.CPUBusy, h.rt.MemoryUsed = 0.5, 200<<20

	a := app("my-api", "my-api:1.0", 2) // 256 MB limit per replica
	h.deploy(a)

	m, err := h.engine.Metrics(ctx, "my-api")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if m.Application != "my-api" || m.CollectedAt.IsZero() || len(m.Replicas) != 2 {
		t.Fatalf("unexpected metrics: %+v", m)
	}
	for i, r := range m.Replicas {
		if r.Replica != i+1 || r.Container == "" || !near(r.CPUPercent, 50, 2) || r.MemoryBytes != 200<<20 || r.MemoryLimitBytes != 256<<20 {
			t.Errorf("replica metrics: %+v", r)
		}
	}
	// Application numbers are sums over replicas — limit included.
	if !near(m.CPUPercent, 100, 4) || m.MemoryBytes != 400<<20 || m.MemoryLimitBytes != 512<<20 {
		t.Errorf("application totals: cpu=%v mem=%d limit=%d", m.CPUPercent, m.MemoryBytes, m.MemoryLimitBytes)
	}
}

func TestMetricsOnlyBlocksWhenItHasNoEarlierSample(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.CPUBusy = 1
	h.deploy(app("my-api", "my-api:1.0", 1))

	// First request: nothing to compare with, so Docker is asked to take two
	// samples itself (the slow path, about a second against a real daemon).
	h.engine.Metrics(ctx, "my-api")
	if _, blocking := h.rt.StatsCalls(); blocking != 1 {
		t.Fatalf("blocking calls after the first request = %d, want 1", blocking)
	}

	// A dashboard polling every few seconds must get instant answers.
	time.Sleep(sampleMinGap + 50*time.Millisecond)
	m, _ := h.engine.Metrics(ctx, "my-api")
	if _, blocking := h.rt.StatsCalls(); blocking != 1 {
		t.Errorf("blocking calls after the second request = %d; polling must use the cached sample", blocking)
	}
	if !near(m.CPUPercent, 100, 5) {
		t.Errorf("cpu from the cached sample = %v, want ~100", m.CPUPercent)
	}

	// Two requests in quick succession: the gap is too short for a meaningful
	// rate, so the slow path is taken rather than reporting noise.
	h.engine.Metrics(ctx, "my-api")
	if _, blocking := h.rt.StatsCalls(); blocking != 2 {
		t.Errorf("blocking calls = %d; a sample %s old is too fresh to compare against", blocking, "few ms")
	}
}

func TestMetricsOfUnlimitedAndStoppedReplicas(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.CPUBusy, h.rt.MemoryUsed = 1, 100<<20
	a := app("my-api", "my-api:1.0", 2)
	a.Resources = spec.Resources{}
	h.deploy(a)

	m, _ := h.engine.Metrics(ctx, "my-api")
	if m.MemoryLimitBytes != 0 || m.Replicas[0].MemoryLimitBytes != 0 {
		t.Errorf("unlimited must read as 0, not as the host's memory: %+v", m)
	}

	h.rt.Crash(h.rt.Containers()[0].ID, 1)
	m, err := h.engine.Metrics(ctx, "my-api")
	if err != nil {
		t.Fatalf("a dead replica must not fail the whole view: %v", err)
	}
	if m.Replicas[0].CPUPercent != 0 || m.Replicas[0].MemoryBytes != 0 || m.Replicas[1].MemoryBytes == 0 {
		t.Errorf("dead replica should read zero, the live one should not: %+v", m.Replicas)
	}
}

func TestMetricsErrors(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if _, err := h.engine.Metrics(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v", err)
	}
	h.rt.PullErr = errors.New("nope")
	h.deploy(app("my-api", "my-api:1.0", 1))
	if _, err := h.engine.Metrics(ctx, "my-api"); !errors.Is(err, ErrNotDeployed) {
		t.Errorf("never deployed: err = %v", err)
	}
}

func TestMetricsCacheForgetsOldContainers(t *testing.T) {
	c := newMetricsCache()
	now := time.Now()
	c.exchange("old", docker.StatsSample{Read: now.Add(-time.Hour)})
	c.exchange("new", docker.StatsSample{Read: now})

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, kept := c.samples["old"]; kept || len(c.samples) != 1 {
		t.Errorf("cache = %v; samples of long-gone containers must not pile up", c.samples)
	}
}
