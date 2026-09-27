package deploy

import (
	"context"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// The metrics history is sampled, not streamed: every SampleInterval the
// sampler takes one cheap reading per running replica and stores the rate
// against the previous one — the same cache the live endpoint uses, so a
// dashboard that is polling and the sampler feed each other's readings. Rows
// are aggregated on read, never on write: a week of raw 30-second samples of
// a few replicas is a few megabytes, and keeping them means the step can be
// chosen per query.

// History windows a client may ask for, and the bucket each is served in.
// The bucket keeps a series to a few hundred points, whatever the window.
var historyWindows = map[string]struct{ window, step time.Duration }{
	"1h":  {time.Hour, 30 * time.Second},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
}

// HistoryRanges lists the accepted values of the history endpoint's `since`.
const HistoryRanges = "1h, 24h or 7d"

// HistoryWindow resolves a `since` value to the window it covers and the
// bucket width it is served in.
func HistoryWindow(since string) (window, step time.Duration, ok bool) {
	w, ok := historyWindows[since]
	return w.window, w.step, ok
}

const pruneEvery = time.Hour

// startSampler runs the sampler until the engine shuts down.
func (e *Engine) startSampler() {
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		ticker := time.NewTicker(e.opts.SampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.sample(e.baseCtx, now)
			}
		}
	}()
}

// sample records one reading per running replica of every running
// application, in one transaction, and once an hour drops what is older than
// the retention period. Time is a parameter so tests can drive it.
func (e *Engine) sample(ctx context.Context, now time.Time) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		e.log.Warn("sampler: list applications", "error", err)
		return
	}
	var rows []store.MetricSample
	for _, app := range apps {
		if app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
			continue
		}
		replicas, err := e.store.ListReplicas(ctx, *app.ActiveDeploymentID)
		if err != nil {
			e.log.Warn("sampler: list replicas", "app", app.Name, "error", err)
			continue
		}
		for _, r := range replicas {
			cur, err := e.rt.Stats(ctx, r.ContainerID, false)
			if err != nil || cur.Read.IsZero() {
				continue // not running, or just gone: nothing to record
			}
			// The first reading of a container has nothing to compute a rate
			// against; it is remembered and the next tick records the rate.
			prev, ok := e.metrics.exchange(r.ContainerID, cur)
			if gap := cur.Read.Sub(prev.Read); !ok || gap < sampleMinGap || gap > sampleMaxAge {
				continue
			}
			rows = append(rows, store.MetricSample{
				ApplicationID: app.ID,
				Replica:       r.Index,
				At:            now,
				CPUPercent:    docker.CPUPercent(prev, cur),
				MemoryBytes:   cur.MemoryBytes,
			})
		}
	}
	if ctx.Err() != nil {
		return
	}
	if len(rows) > 0 {
		if err := e.store.AddSamples(ctx, rows); err != nil {
			e.log.Warn("sampler: could not store samples", "error", err)
		}
	}

	e.metrics.mu.Lock()
	due := now.Sub(e.metrics.lastPrune) >= pruneEvery
	if due {
		e.metrics.lastPrune = now
	}
	e.metrics.mu.Unlock()
	if due {
		if _, err := e.store.PruneSamples(ctx, now.Add(-e.opts.MetricsRetention)); err != nil {
			e.log.Warn("sampler: could not prune samples", "error", err)
		}
	}
}

// MetricsHistory returns the sampled resource usage of the application over
// the last `window`, one series per replica, aggregated into buckets of `step`.
func (e *Engine) MetricsHistory(ctx context.Context, name string, window, step time.Duration) (api.MetricsHistory, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.MetricsHistory{}, err
	}
	if app.ActiveDeploymentID == nil {
		return api.MetricsHistory{}, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return api.MetricsHistory{}, err
	}
	since := time.Now().Add(-window).UTC().Truncate(time.Second)
	buckets, err := e.store.MetricHistory(ctx, app.ID, since, step)
	if err != nil {
		return api.MetricsHistory{}, err
	}

	out := api.MetricsHistory{
		Application: name,
		Since:       since,
		Step:        shortDuration(step),
		Series:      []api.MetricsSeries{},
		Limits:      api.MetricsLimits{CPU: d.Spec.Resources.CPU, MemoryBytes: d.Spec.Resources.MemoryBytes},
	}
	for _, b := range buckets { // ordered by replica, then time
		n := len(out.Series)
		if n == 0 || out.Series[n-1].Replica != b.Replica {
			out.Series = append(out.Series, api.MetricsSeries{Replica: b.Replica, Points: []api.MetricsPoint{}})
			n++
		}
		out.Series[n-1].Points = append(out.Series[n-1].Points, api.MetricsPoint{At: b.At, CPUPercent: b.CPUPercent, MemoryBytes: b.MemoryBytes})
	}
	return out, nil
}
