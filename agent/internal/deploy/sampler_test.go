package deploy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
)

// sampled is a harness whose sampler is ticked by hand on a synthetic clock,
// which the fake runtime's stats readings follow too.
type sampled struct {
	*harness
	now time.Time
}

func newSampled(t *testing.T) *sampled {
	h := newHarness(t)
	// Near the wall clock, since MetricsHistory measures its window from
	// it; on a bucket boundary, so a few ticks land in one bucket.
	s := &sampled{harness: h, now: time.Now().UTC().Truncate(5 * time.Minute)}
	h.rt.Clock = func() time.Time { return s.now }
	h.rt.CPUBusy, h.rt.MemoryUsed = 0.5, 200<<20
	return s
}

// advance moves the clock and takes one round of samples.
func (s *sampled) advance(d time.Duration) {
	s.now = s.now.Add(d)
	s.engine.sample(context.Background(), s.now)
}

func (s *sampled) history(t *testing.T, name string, window, step time.Duration) []store.MetricBucket {
	t.Helper()
	app, err := s.store.GetApplication(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := s.store.MetricHistory(context.Background(), app.ID, s.now.Add(-window), step)
	if err != nil {
		t.Fatal(err)
	}
	return buckets
}

func TestSamplerRecordsRunningReplicasOnly(t *testing.T) {
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	s.rt.Crash(s.container(t, 1), 1)

	// The first reading of a container only primes the rate.
	s.advance(30 * time.Second)
	if got := s.history(t, "my-api", time.Hour, 30*time.Second); len(got) != 0 {
		t.Fatalf("buckets after the first tick = %+v; CPU is a rate and needs two readings", got)
	}
	s.advance(30 * time.Second)
	got := s.history(t, "my-api", time.Hour, 30*time.Second)
	if len(got) != 1 || got[0].Replica != 2 {
		t.Fatalf("buckets = %+v; only the running replica is measured", got)
	}
	if !near(got[0].CPUPercent, 50, 1) || got[0].MemoryBytes != 200<<20 || !got[0].At.Equal(s.now) {
		t.Errorf("bucket = %+v; want half a core, 200 MB, at %s", got[0], s.now)
	}
	if _, blocking := s.rt.StatsCalls(); blocking != 0 {
		t.Errorf("the sampler made %d blocking stats calls; it must use the cheap reading", blocking)
	}
}

func (s *sampled) container(t *testing.T, replica int) string {
	t.Helper()
	for _, c := range s.rt.Containers() {
		if c.Replica == replica {
			return c.ID
		}
	}
	t.Fatalf("no container for replica %d", replica)
	return ""
}

func TestSamplerSkipsStoppedApplications(t *testing.T) {
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	s.deploy(app("other", "other:1.0", 1))
	s.advance(30 * time.Second)
	s.advance(30 * time.Second)
	if err := s.engine.Stop(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	s.advance(30 * time.Second)
	s.advance(30 * time.Second)

	if got := s.history(t, "my-api", time.Hour, 30*time.Second); len(got) != 3 {
		t.Errorf("my-api buckets = %d, want 3", len(got))
	}
	if got := s.history(t, "other", time.Hour, 30*time.Second); len(got) != 1 {
		t.Errorf("other buckets = %d, want the 1 from before it was stopped", len(got))
	}
}

func TestSamplerPrunesOncePerHour(t *testing.T) {
	ctx := context.Background()
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	a, _ := s.store.GetApplication(ctx, "my-api")

	// Planted rows carry a replica number the sampler's own rows never have.
	old := func(at time.Time) {
		if err := s.store.AddSamples(ctx, []store.MetricSample{{ApplicationID: a.ID, Replica: 9, At: at, CPUPercent: 1, MemoryBytes: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		n := 0
		for _, b := range s.history(t, "my-api", 30*24*time.Hour, time.Hour) {
			if b.Replica == 9 {
				n++
			}
		}
		return n
	}

	old(s.now.Add(-8 * 24 * time.Hour))
	s.advance(30 * time.Second) // the first tick prunes
	if count() != 0 {
		t.Fatal("a sample older than the retention period survived the first tick")
	}
	old(s.now.Add(-8 * 24 * time.Hour))
	old(s.now.Add(-6 * 24 * time.Hour))
	s.advance(30 * time.Second)
	if count() != 2 {
		t.Error("pruning ran again within the hour")
	}
	s.advance(time.Hour)
	if count() != 1 {
		t.Error("an hour later, the sample beyond retention should be gone and the one within kept")
	}
}

func TestMetricsHistoryView(t *testing.T) {
	ctx := context.Background()
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	for range 5 {
		s.advance(30 * time.Second)
	}

	h, err := s.engine.MetricsHistory(ctx, "my-api", time.Hour, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if h.Application != "my-api" || h.Step != "30s" || h.Limits.CPU != 0.5 || h.Limits.MemoryBytes != 256<<20 {
		t.Errorf("unexpected history: %+v", h)
	}
	if len(h.Series) != 2 || h.Series[0].Replica != 1 || h.Series[1].Replica != 2 {
		t.Fatalf("series = %+v; one per replica, in order", h.Series)
	}
	for _, series := range h.Series {
		if len(series.Points) != 4 { // five readings make four rates
			t.Errorf("replica %d has %d points, want 4", series.Replica, len(series.Points))
		}
	}
	if h, _ := s.engine.MetricsHistory(ctx, "my-api", 24*time.Hour, 5*time.Minute); h.Step != "5m" || len(h.Series[0].Points) != 1 {
		t.Errorf("aggregated into 5m buckets: step=%s points=%d", h.Step, len(h.Series[0].Points))
	}

	if _, err := s.engine.MetricsHistory(ctx, "ghost", time.Hour, 30*time.Second); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v", err)
	}
	s.rt.PullErr = errors.New("nope")
	s.deploy(app("new", "new:1.0", 1))
	if _, err := s.engine.MetricsHistory(ctx, "new", time.Hour, 30*time.Second); !errors.Is(err, ErrNotDeployed) {
		t.Errorf("never deployed: err = %v", err)
	}
}

func TestHistoryWindows(t *testing.T) {
	tests := map[string]struct {
		window, step time.Duration
	}{"1h": {time.Hour, 30 * time.Second}, "24h": {24 * time.Hour, 5 * time.Minute}, "7d": {7 * 24 * time.Hour, time.Hour}}
	for since, want := range tests {
		window, step, ok := HistoryWindow(since)
		if !ok || window != want.window || step != want.step {
			t.Errorf("HistoryWindow(%s) = %s, %s, %v", since, window, step, ok)
		}
	}
	for _, bad := range []string{"", "2h", "1d", "60m", "7D"} {
		if _, _, ok := HistoryWindow(bad); ok {
			t.Errorf("HistoryWindow(%q) accepted", bad)
		}
	}
}
