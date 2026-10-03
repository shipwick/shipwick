package deploy

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A notification reports something that happened; an alert reports something
// that is the case and, left alone, ends badly: a replica close to its memory
// limit, a disk filling up, a replica that keeps being restarted, an
// application that has not been healthy for a while. Each is raised once when
// it becomes true and cleared once when it stops being true, with some
// distance between the two levels so that a value hovering at the threshold
// is one alert, not one per sample.
//
// Whether a condition holds is decided by the pure functions below, from
// samples and times that are handed to them. What is active is kept in
// memory, like the rest of what the supervisor knows: after an agent restart
// a condition that still holds is raised again.
const (
	// memorySamples is how many consecutive samples must be over the
	// threshold: one reading during a garbage collection is not a trend.
	memorySamples = 3
	// The clear levels, in percentage points below the thresholds.
	memoryClearMargin = 10
	diskClearMargin   = 5
	// diskCriticalPercent is not configurable: past it the remaining space
	// is a matter of one image pull.
	diskCriticalPercent = 95

	restartAlertCount  = 3
	restartAlertWindow = 10 * time.Minute

	unhealthyWarning  = 5 * time.Minute
	unhealthyCritical = time.Hour
)

// Defaults of the configurable thresholds.
const (
	defaultAlertMemoryPercent = 90
	defaultAlertDiskPercent   = 85
)

// memoryAlerting reports whether a replica's memory alert stands. samples are
// the replica's, oldest first; only those of the last few intervals count, so
// that "three in a row" cannot be three readings with an outage in between.
// Without a recent sample — the replica is not running — nothing changes.
func memoryAlerting(was bool, samples []store.MetricSample, now time.Time, interval time.Duration, limit int64, percent int) bool {
	oldest := now.Add(-interval*memorySamples + interval/2)
	for len(samples) > 0 && samples[0].At.Before(oldest) {
		samples = samples[1:]
	}
	if len(samples) == 0 || limit <= 0 {
		return was
	}
	if was {
		return percentOf(samples[len(samples)-1].MemoryBytes, limit) >= float64(percent-memoryClearMargin)
	}
	if len(samples) < memorySamples {
		return false
	}
	for _, m := range samples[len(samples)-memorySamples:] {
		if percentOf(m.MemoryBytes, limit) < float64(percent) {
			return false
		}
	}
	return true
}

// diskSeverity is the severity of the disk alert for a usage, "" for none,
// given the severity it had. Each level is left a few points below where it
// is entered.
func diskSeverity(was string, u api.DiskUsage, percent int) string {
	used := percentOf(u.UsedBytes, u.TotalBytes)
	switch {
	case used >= diskCriticalPercent:
		return api.SeverityCritical
	case was == api.SeverityCritical && used >= diskCriticalPercent-diskClearMargin:
		return api.SeverityCritical
	case used >= float64(percent):
		return api.SeverityWarning
	case was != "" && used >= float64(percent-diskClearMargin):
		return api.SeverityWarning
	}
	return ""
}

// restartAlerting reports whether a replica's restart alert stands, from the
// times the supervisor restarted it. While the replica is held — crash-looping,
// or its application down, each of which is reported in its own way — the
// alert is neither raised nor cleared.
func restartAlerting(was bool, restarts []time.Time, now time.Time, held bool) bool {
	if held {
		return was
	}
	return len(within(restarts, now, restartAlertWindow)) >= restartAlertCount
}

// unhealthySeverity is the severity of an application that has not been
// healthy since `since`: nothing at first — going down is notified at once,
// and most outages end within minutes — a warning after five minutes and
// critical after an hour.
func unhealthySeverity(since, now time.Time) string {
	switch d := now.Sub(since); {
	case d >= unhealthyCritical:
		return api.SeverityCritical
	case d >= unhealthyWarning:
		return api.SeverityWarning
	}
	return ""
}

func percentOf(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

// within returns the times that lie in the window ending at now.
func within(times []time.Time, now time.Time, window time.Duration) []time.Time {
	for len(times) > 0 && now.Sub(times[0]) > window {
		times = times[1:]
	}
	return times
}

type alertKey struct {
	kind        string
	application string
	replica     int
}

// observation is how the supervisor last saw an application. The alerts are
// decided from it, and a metrics scrape serves it rather than ask Docker.
type observation struct {
	replicas  api.ReplicaCount
	crashLoop bool
}

// alertBook is the engine's memory of alerts. The zero value is ready to use.
type alertBook struct {
	mu     sync.Mutex
	active map[alertKey]api.Alert
	// restarts are the times the supervisor restarted each container, as far
	// back as the alert looks.
	restarts map[string][]time.Time
	// unhealthy is since when each application has not been healthy.
	unhealthy map[string]time.Time
	observed  map[string]observation
}

// locked runs fn with the book's maps in place.
func (b *alertBook) locked(fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		b.active = map[alertKey]api.Alert{}
		b.restarts = map[string][]time.Time{}
		b.unhealthy = map[string]time.Time{}
		b.observed = map[string]observation{}
	}
	fn()
}

func (b *alertBook) get(key alertKey) (a api.Alert, ok bool) {
	b.locked(func() { a, ok = b.active[key] })
	return a, ok
}

// noteRestart records that the supervisor restarted a container.
func (b *alertBook) noteRestart(containerID string, now time.Time) {
	b.locked(func() {
		b.restarts[containerID] = append(within(b.restarts[containerID], now, restartAlertWindow), now)
	})
}

// Alerts returns the active alerts, oldest first.
func (e *Engine) Alerts() []api.Alert {
	out := []api.Alert{}
	e.alerts.locked(func() {
		for _, a := range e.alerts.active {
			out = append(out, a)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case !a.Since.Equal(b.Since):
			return a.Since.Before(b.Since)
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.Application != b.Application:
			return a.Application < b.Application
		}
		return a.Replica < b.Replica
	})
	return out
}

// Disk reports how full the disk that holds the agent's data is, or nil
// where that cannot be measured.
func (e *Engine) Disk() *api.DiskUsage {
	if e.opts.DiskUsage == nil {
		return nil
	}
	u, ok := e.opts.DiskUsage()
	if !ok || u.TotalBytes <= 0 {
		return nil
	}
	return &u
}

// raiseAlert makes the alert active at the given severity. It is told about
// when it is new or more severe than it was; an alert that merely persists,
// or steps down a level, changes nothing anybody has to read. app is nil for
// an alert about the server.
func (e *Engine) raiseAlert(ctx context.Context, app *store.Application, key alertKey, severity, message string, now time.Time) {
	var tell bool
	e.alerts.locked(func() {
		was, active := e.alerts.active[key]
		if active && was.Severity == severity {
			return
		}
		tell = !active || severity == api.SeverityCritical
		a := api.Alert{Kind: key.kind, Severity: severity, Application: key.application, Replica: key.replica, Message: message, Since: now.UTC()}
		if active {
			a.Since = was.Since
		}
		e.alerts.active[key] = a
	})
	if !tell {
		return
	}
	level := api.LevelWarn
	if severity == api.SeverityCritical {
		level = api.LevelError
	}
	e.tellAlert(ctx, app, notify.AlertRaised, key, severity, level, message, now)
}

// clearAlert ends an alert, if it is active. quiet is for a clearing that
// some other notification already announces.
func (e *Engine) clearAlert(ctx context.Context, app *store.Application, key alertKey, message string, now time.Time, quiet bool) {
	var was api.Alert
	var active bool
	e.alerts.locked(func() {
		was, active = e.alerts.active[key]
		delete(e.alerts.active, key)
	})
	if !active {
		return
	}
	if quiet {
		e.alertEvent(ctx, app, api.LevelInfo, message, now)
		return
	}
	e.tellAlert(ctx, app, notify.AlertCleared, key, was.Severity, api.LevelInfo, message, now)
}

func (e *Engine) tellAlert(ctx context.Context, app *store.Application, kind string, key alertKey, severity, level, message string, now time.Time) {
	e.alertEvent(ctx, app, level, message, now)
	e.notify(ctx, notify.Event{
		Kind:        kind,
		Application: key.application,
		Message:     message,
		At:          now,
		Alert:       &notify.Alert{Kind: key.kind, Severity: severity, Replica: key.replica},
	})
}

// alertEvent writes the alert to the log and, when it is about an
// application, to that application's event feed.
func (e *Engine) alertEvent(ctx context.Context, app *store.Application, level, message string, now time.Time) {
	if app == nil {
		e.log.Log(ctx, slogLevel(level), message)
		return
	}
	e.log.Log(ctx, slogLevel(level), message, "app", app.Name)
	ctx = context.WithoutCancel(ctx)
	if err := e.store.AddEvent(ctx, app.ID, nil, level, api.EventAlert, message, now); err != nil {
		e.log.Warn("could not record event", "app", app.Name, "error", err)
		return
	}
	if err := e.store.PruneApplicationEvents(ctx, app.ID, appEventsKept); err != nil {
		e.log.Warn("could not prune events", "app", app.Name, "error", err)
	}
}

// dropAlerts forgets alerts without a word: what they were about is gone —
// the application stopped or deleted, the replica scaled away — and whoever
// did that knows.
func (e *Engine) dropAlerts(gone func(alertKey) bool) {
	e.alerts.locked(func() {
		for key := range e.alerts.active {
			if gone(key) {
				delete(e.alerts.active, key)
			}
		}
	})
}

// checkAlerts evaluates the conditions that are read from samples rather
// than seen by the supervisor: the disk and the replicas' memory. It runs
// after every round of sampling. Time is a parameter so tests can drive it.
func (e *Engine) checkAlerts(ctx context.Context, now time.Time) {
	e.checkDisk(ctx, now)
	e.checkMemory(ctx, now)
}

func (e *Engine) checkDisk(ctx context.Context, now time.Time) {
	u := e.Disk()
	if u == nil {
		return
	}
	key := alertKey{kind: api.AlertDisk}
	was, _ := e.alerts.get(key)
	used := int(percentOf(u.UsedBytes, u.TotalBytes))
	left := fmt.Sprintf("%s of %s free", spec.FormatMemory(u.TotalBytes-u.UsedBytes), spec.FormatMemory(u.TotalBytes))
	const look = "See what takes the space with: docker system df"
	switch diskSeverity(was.Severity, *u, e.opts.AlertDiskPercent) {
	case api.SeverityCritical:
		e.raiseAlert(ctx, nil, key, api.SeverityCritical,
			fmt.Sprintf("The server's disk is %d%% full (%s). Deployments, databases and logs fail when it runs out. %s", used, left, look), now)
	case api.SeverityWarning:
		e.raiseAlert(ctx, nil, key, api.SeverityWarning, fmt.Sprintf("The server's disk is %d%% full (%s). %s", used, left, look), now)
	default:
		e.clearAlert(ctx, nil, key, fmt.Sprintf("The server's disk is back to %d%% full (%s)", used, left), now, false)
	}
}

func (e *Engine) checkMemory(ctx context.Context, now time.Time) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		e.log.Warn("alerts: list applications", "error", err)
		return
	}
	limited := map[alertKey]bool{}
	for _, app := range apps {
		if app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
		if err != nil {
			e.log.Warn("alerts: load active deployment", "app", app.Name, "error", err)
			return
		}
		limit := d.Spec.Resources.MemoryBytes
		if limit <= 0 || d.StaticDigest != "" {
			continue
		}
		samples, err := e.store.RecentSamples(ctx, app.ID, now.Add(-e.opts.SampleInterval*memorySamples))
		if err != nil {
			e.log.Warn("alerts: read samples", "app", app.Name, "error", err)
			return
		}
		byReplica := map[int][]store.MetricSample{}
		for _, m := range samples {
			byReplica[m.Replica] = append(byReplica[m.Replica], m)
		}
		for replica := 1; replica <= d.Spec.Replicas; replica++ {
			key := alertKey{kind: api.AlertMemory, application: app.Name, replica: replica}
			limited[key] = true
			_, was := e.alerts.get(key)
			mine := byReplica[replica]
			is := memoryAlerting(was, mine, now, e.opts.SampleInterval, limit, e.opts.AlertMemoryPercent)
			if is == was {
				continue
			}
			latest := mine[len(mine)-1].MemoryBytes
			usage := fmt.Sprintf("%.0f%% of its memory limit (%s of %s)", math.Floor(percentOf(latest, limit)), spec.FormatMemory(latest), spec.FormatMemory(limit))
			if is {
				e.raiseAlert(ctx, &app, key, api.SeverityWarning,
					fmt.Sprintf("%s replica %d is at %s. At the limit it is killed and restarted; raise resources.memory in deploy.yaml, or watch it with: shipwick status %s",
						app.Name, replica, usage, app.Name), now)
			} else {
				e.clearAlert(ctx, &app, key, fmt.Sprintf("%s replica %d is back at %s", app.Name, replica, usage), now, false)
			}
		}
	}
	// No limit any more, or fewer replicas.
	e.dropAlerts(func(key alertKey) bool { return key.kind == api.AlertMemory && !limited[key] })
}

// noteAlerts records how the supervisor found an application and evaluates
// the conditions that follow from it: replicas that keep being restarted, and
// an application that stays unhealthy. Healthy is what noteAvailability calls
// recovered — every replica ready and none with a restart held against it —
// for the same reason: a crash-looping replica runs for a moment between
// crashes, and must not look like the end of the trouble each time.
func (s *supervisor) noteAlerts(ctx context.Context, now time.Time, app store.Application, d store.Deployment, replicas []store.Replica, containers map[string]docker.Container) {
	book := &s.e.alerts
	seen := observation{replicas: api.ReplicaCount{Desired: d.Spec.Replicas}}
	settled := true
	looping := map[string]bool{}
	s.mu.Lock()
	for _, r := range replicas {
		c, exists := containers[r.ContainerID]
		if !exists {
			continue
		}
		health := api.HealthNone
		if st, ok := s.states[r.ContainerID]; ok {
			health = st.health
			settled = settled && st.restarts == 0
			looping[r.ContainerID] = st.crashLoop
			seen.crashLoop = seen.crashLoop || st.crashLoop
		}
		if c.Running {
			seen.replicas.Running++
		}
		if isReady(c.Running, health) {
			seen.replicas.Healthy++
		}
	}
	// The recovery of an application that was reported down is announced by
	// noteAvailability, which runs next.
	reportedDown := s.down[app.Name]
	s.mu.Unlock()
	ready := seen.replicas.Healthy

	restarted := map[int][]time.Time{}
	var since time.Time
	healthy := ready >= d.Spec.Replicas && settled
	book.locked(func() {
		book.observed[app.Name] = seen
		for _, r := range replicas {
			restarted[r.Index] = within(book.restarts[r.ContainerID], now, restartAlertWindow)
		}
		switch _, counting := book.unhealthy[app.Name]; {
		case healthy:
			delete(book.unhealthy, app.Name)
		case !counting && ready < d.Spec.Replicas:
			book.unhealthy[app.Name] = now
		}
		since = book.unhealthy[app.Name]
	})

	for _, r := range replicas {
		if r.Index > d.Spec.Replicas {
			continue
		}
		key := alertKey{kind: api.AlertRestarts, application: app.Name, replica: r.Index}
		_, was := book.get(key)
		switch is := restartAlerting(was, restarted[r.Index], now, reportedDown || ready == 0 || looping[r.ContainerID]); {
		case is && !was:
			s.e.raiseAlert(ctx, &app, key, api.SeverityWarning,
				fmt.Sprintf("%s replica %d was restarted %d times in the last %d minutes. See why it keeps stopping with: shipwick logs %s",
					app.Name, r.Index, len(restarted[r.Index]), int(restartAlertWindow.Minutes()), app.Name), now)
		case was && !is:
			s.e.clearAlert(ctx, &app, key,
				fmt.Sprintf("%s replica %d has stayed up: fewer than %d restarts in the last %d minutes", app.Name, r.Index, restartAlertCount, int(restartAlertWindow.Minutes())), now, false)
		}
	}
	s.e.dropAlerts(func(key alertKey) bool {
		return key.application == app.Name && key.replica > d.Spec.Replicas
	})

	key := alertKey{kind: api.AlertUnhealthy, application: app.Name}
	state := fmt.Sprintf("%d/%d replicas ready", ready, d.Spec.Replicas)
	switch {
	case since.IsZero():
		s.e.clearAlert(ctx, &app, key, fmt.Sprintf("%s is healthy again: %s", app.Name, state), now, reportedDown)
	case unhealthySeverity(since, now) == api.SeverityCritical:
		s.e.raiseAlert(ctx, &app, key, api.SeverityCritical,
			fmt.Sprintf("%s has not been healthy for an hour: %s. See why with: shipwick status %s", app.Name, state, app.Name), now)
	case unhealthySeverity(since, now) == api.SeverityWarning:
		s.e.raiseAlert(ctx, &app, key, api.SeverityWarning,
			fmt.Sprintf("%s has not been healthy for 5 minutes: %s. See why with: shipwick status %s", app.Name, state, app.Name), now)
	}
}

// forgetAlerts drops what is remembered about applications that are no
// longer supervised — stopped, deleted, without a deployment — and restart
// times that have left the window.
func (s *supervisor) forgetAlerts(apps []store.Application, now time.Time) {
	supervised := make(map[string]bool, len(apps))
	for _, app := range apps {
		supervised[app.Name] = app.ActiveDeploymentID != nil && app.DesiredState == api.DesiredRunning
	}
	book := &s.e.alerts
	book.locked(func() {
		for key := range book.active {
			if key.application != "" && !supervised[key.application] {
				delete(book.active, key)
			}
		}
		for name := range book.unhealthy {
			if !supervised[name] {
				delete(book.unhealthy, name)
			}
		}
		for name := range book.observed {
			if !supervised[name] {
				delete(book.observed, name)
			}
		}
		for id, times := range book.restarts {
			if len(within(times, now, restartAlertWindow)) == 0 {
				delete(book.restarts, id)
			}
		}
	})
}
