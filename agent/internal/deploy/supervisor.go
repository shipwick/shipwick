package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// appEventsKept bounds the application-level event feed. A crash-looping
// replica adds to it for as long as it keeps dying.
const appEventsKept = 500

// supervisor keeps the replicas of every active deployment alive. Once per
// tick it compares what should be running with what is, and:
//
//   - restarts replicas that exited, as far as restart.policy allows;
//   - probes running replicas and restarts those that stopped answering;
//   - spaces restarts out with a backoff, and past its end declares the
//     replica crash-looping and retries only every CrashLoopDelay.
//
// It acts on an application only while holding that application's lock, so it
// can never interleave with a deployment, a stop or a delete. Its memory of
// restarts and health is per agent process: after an agent restart every
// replica gets a clean slate.
type supervisor struct {
	e *Engine

	mu     sync.Mutex
	states map[string]*replicaState // by container ID

	proxyErr string // last proxy sync error, to log changes only

	// recreate paces attempts to recreate missing replicas, by deployment ID.
	recreate map[int64]*recreateState

	probes       sync.WaitGroup // in-flight health probes
	inlineProbes bool           // tests only
}

type replicaState struct {
	// Restart bookkeeping.
	restarts     int       // consecutive restarts without a stable run in between
	lastRestart  time.Time // zero: never restarted by this supervisor
	nextRestart  time.Time // earliest moment the next restart may happen
	exitSeen     bool      // the current exit has been noticed, announced and scheduled
	leaveStopped bool      // restart.policy says this exit is final
	crashLoop    bool

	// Health bookkeeping.
	health    string    // api.Health*
	failures  int       // consecutive failed probes
	upSince   time.Time // when the supervisor first saw the current run
	lastProbe time.Time
	probing   bool // a probe is in flight
}

type recreateState struct {
	failures int
	next     time.Time
}

func newSupervisor(e *Engine) *supervisor {
	return &supervisor{e: e, states: map[string]*replicaState{}, recreate: map[int64]*recreateState{}}
}

// StartSupervisor runs the supervisor until the engine shuts down.
func (e *Engine) StartSupervisor() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.bg.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.bg.Done()
		ticker := time.NewTicker(e.opts.SuperviseInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.sup.tick(e.baseCtx, now)
			}
		}
	}()
}

// tick performs one supervision pass. Time is a parameter so that tests can
// drive the backoff schedule without sleeping through it.
func (s *supervisor) tick(ctx context.Context, now time.Time) {
	apps, err := s.e.store.ListApplications(ctx)
	if err != nil {
		s.logErr(ctx, "list applications", err)
		return
	}

	active := map[int64]bool{}
	for _, app := range apps {
		if app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
			continue
		}
		active[*app.ActiveDeploymentID] = true
		// Busy means a user operation owns the application right now; it
		// will be looked at again next tick.
		if wait, err := s.e.tryLock(app.Name, true); wait != nil || err != nil {
			continue
		}
		s.superviseApp(ctx, now, app)
		s.e.unlock(app.Name)
	}
	s.forgetGone(ctx, active)

	// Routing follows readiness: a replica that just died or turned unhealthy
	// leaves the rotation here, one that recovered rejoins it. Syncing is a
	// no-op unless the set of ready replicas actually changed.
	s.syncProxy(ctx)
}

// forgetGone drops bookkeeping for containers and deployments that no longer
// exist, so memory does not grow with every deployment.
func (s *supervisor) forgetGone(ctx context.Context, activeDeployments map[int64]bool) {
	containers, err := s.e.rt.ListContainers(ctx, "")
	if err != nil {
		s.logErr(ctx, "list containers", err)
		return
	}
	exists := make(map[string]bool, len(containers))
	for _, c := range containers {
		exists[c.ID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.states {
		if !exists[id] {
			delete(s.states, id)
		}
	}
	for id := range s.recreate {
		if !activeDeployments[id] {
			delete(s.recreate, id)
		}
	}
}

// syncProxy reports a proxy problem when it appears and when it clears, not
// on every tick in between.
func (s *supervisor) syncProxy(ctx context.Context) {
	err := s.e.SyncProxy(ctx)
	if ctx.Err() != nil {
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.mu.Lock()
	changed := msg != s.proxyErr
	s.proxyErr = msg
	s.mu.Unlock()

	switch {
	case !changed:
	case err != nil:
		s.e.log.Error("reverse proxy is out of sync; retrying every tick", "error", err)
	default:
		s.e.log.Info("reverse proxy is in sync again")
	}
}

// superviseApp reconciles one application with what Docker reports. The
// caller holds the application's lock, and the containers are listed under
// it: a snapshot taken earlier could predate a deployment that has since
// finished, and would make its brand-new replicas look missing.
func (s *supervisor) superviseApp(ctx context.Context, now time.Time, app store.Application) {
	app, err := s.e.store.GetApplication(ctx, app.Name)
	if err != nil || app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
		return
	}
	d, err := s.e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		s.logErr(ctx, "load active deployment", err)
		return
	}
	replicas, err := s.e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		s.logErr(ctx, "list replicas", err)
		return
	}
	containers, err := s.e.rt.ListContainers(ctx, app.Name)
	if err != nil {
		s.logErr(ctx, "list containers", err)
		return
	}
	byID := make(map[string]docker.Container, len(containers))
	for _, c := range containers {
		byID[c.ID] = c
	}

	// Containers of any other deployment are leftovers — of a cleanup that was
	// interrupted, say. With the lock held, no deployment can be in flight.
	for _, c := range containers {
		if c.DeploymentID != d.ID {
			if err := s.e.retireContainer(ctx, c.ID); err == nil {
				s.event(ctx, app, api.LevelWarn, "Removed leftover container %s", c.Name)
			}
		}
	}

	present := map[int]bool{}
	for _, r := range replicas {
		c, exists := byID[r.ContainerID]
		if !exists {
			s.noteDisappeared(ctx, app, r)
			continue
		}
		present[r.Index] = true
		if c.Running {
			s.superviseRunning(ctx, now, app, d, r, c)
		} else if c.State == "exited" || c.State == "dead" || c.State == "created" {
			s.superviseDown(ctx, now, app, d, r)
		}
	}

	var missing []int
	for i := 1; i <= d.Spec.Replicas; i++ {
		if !present[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		s.recreateReplicas(ctx, now, app, d, missing)
	}
}

// noteDisappeared retires the record of a replica whose container no longer
// exists — removed by hand, by `docker system prune`, by anything that is not
// Shipwick. Docker is asked once more by ID first: acting on a listing alone
// would be acting on hearsay.
func (s *supervisor) noteDisappeared(ctx context.Context, app store.Application, r store.Replica) {
	if _, err := s.e.rt.InspectContainer(ctx, r.ContainerID); !errors.Is(err, docker.ErrNotFound) {
		return
	}
	if err := s.e.store.MarkReplicaRemoved(ctx, r.ContainerID, time.Now()); err != nil {
		s.logErr(ctx, "mark replica removed", err)
		return
	}
	s.event(ctx, app, api.LevelWarn, "Replica %d's container %s has disappeared", r.Index, r.ContainerName)
}

// recreateReplicas brings the active deployment back to its desired replica
// count: desired 2, present 1 → create 1. Failures back off on the same
// schedule as restarts: a deployment whose image is gone for good must not be
// retried every second.
func (s *supervisor) recreateReplicas(ctx context.Context, now time.Time, app store.Application, d store.Deployment, missing []int) {
	s.mu.Lock()
	st := s.recreate[d.ID]
	if st == nil {
		st = &recreateState{}
		s.recreate[d.ID] = st
	}
	due := !now.Before(st.next)
	s.mu.Unlock()
	if !due {
		return
	}

	created, err := s.e.ensureReplicas(ctx, d, missing)
	if err != nil {
		for _, r := range created {
			s.e.removeContainer(ctx, r.ContainerID)
		}
		s.mu.Lock()
		delay := s.delay(st.failures)
		st.failures++
		st.next = now.Add(delay)
		s.mu.Unlock()
		s.event(ctx, app, api.LevelError, "Could not recreate %s: %v. Retrying in %s", plural(len(missing), "missing replica"), err, shortDuration(delay))
		return
	}

	s.mu.Lock()
	delete(s.recreate, d.ID)
	s.mu.Unlock()
	for _, r := range created {
		// Like any freshly started replica: with a health check, no traffic
		// until it has passed it.
		s.reset(r.ContainerID, d.Spec.Health != nil)
		s.event(ctx, app, api.LevelInfo, "Recreated replica %d", r.Index)
	}
}

// state returns the bookkeeping for a container, creating it on first sight.
// Callers must hold s.mu.
func (s *supervisor) state(containerID string, d store.Deployment) *replicaState {
	st, ok := s.states[containerID]
	if !ok {
		st = &replicaState{health: api.HealthNone}
		if d.Spec.Health != nil {
			// First sight of a running replica means either the agent just
			// started or a deployment just verified it. Neither is a reason
			// to doubt it before the first probe says otherwise.
			st.health = api.HealthUnknown
		}
		s.states[containerID] = st
	}
	return st
}

// reset wipes a container's history after it was started on request. With a
// health check it is "starting" — not yet proven, so not yet routed to.
func (s *supervisor) reset(containerID string, hasHealthCheck bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &replicaState{health: api.HealthNone}
	if hasHealthCheck {
		st.health = api.HealthStarting
	}
	s.states[containerID] = st
}

func (s *supervisor) superviseRunning(ctx context.Context, now time.Time, app store.Application, d store.Deployment, r store.Replica, c docker.Container) {
	s.mu.Lock()
	st := s.state(r.ContainerID, d)
	st.exitSeen, st.leaveStopped = false, false
	if st.upSince.IsZero() {
		st.upSince = now
	}

	// A long enough run forgives the past — but merely running is not
	// enough for a replica that has a health check and has yet to pass it.
	recovered := false
	proven := st.health != api.HealthUnhealthy && st.health != api.HealthStarting
	if st.restarts > 0 && proven && now.Sub(st.lastRestart) >= s.e.opts.StableAfter {
		recovered = st.crashLoop
		st.restarts, st.crashLoop, st.nextRestart = 0, false, time.Time{}
	}

	h := d.Spec.Health
	restartUnhealthy := false
	var probe func()
	if h != nil {
		restartUnhealthy = st.health == api.HealthUnhealthy &&
			d.Spec.Restart.Policy != spec.RestartNever && !now.Before(st.nextRestart)

		// While a replica is coming up, probe at the startup pace so that
		// "healthy" shows promptly; afterwards at the configured interval.
		every := h.Interval.Std()
		if st.health == api.HealthStarting || st.health == api.HealthUnknown {
			every = min(every, s.e.opts.StartupPollInterval)
		}
		if !restartUnhealthy && !st.probing && c.IP != "" && now.Sub(st.lastProbe) >= every {
			st.probing, st.lastProbe = true, now
			probe = func() { s.probe(ctx, now, app, d, r, c.IP) }
		}
	}
	s.mu.Unlock()

	if recovered {
		s.event(ctx, app, api.LevelInfo, "Replica %d has been stable for %s; no longer crash-looping", r.Index, shortDuration(s.e.opts.StableAfter))
	}
	if probe != nil {
		// Probes run outside the tick: a slow one must not delay the restart
		// of some other replica. (Tests run them inline to stay deterministic.)
		if s.inlineProbes {
			probe()
		} else {
			s.probes.Add(1)
			go func() {
				defer s.probes.Done()
				probe()
			}()
		}
	}
	if restartUnhealthy {
		s.restart(ctx, now, app, r, func() error {
			return s.e.rt.RestartContainer(ctx, r.ContainerID, s.e.opts.StopTimeout)
		})
	}
}

// probe runs one health check and folds the result into the replica's state.
// now is the tick that launched it; a probe's own duration is noise next to
// the budgets it is compared against.
func (s *supervisor) probe(ctx context.Context, now time.Time, app store.Application, d store.Deployment, r store.Replica, ip string) {
	h := d.Spec.Health
	err := s.e.opts.Probe(ctx, ip, d.Spec.Port, h.Path, h.Timeout.Std())
	if ctx.Err() != nil {
		return // shutting down: the result says nothing about the replica
	}

	s.mu.Lock()
	st, ok := s.states[r.ContainerID]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.probing = false
	was := st.health

	switch {
	case err == nil:
		st.health, st.failures = api.HealthHealthy, 0
	case st.health == api.HealthStarting:
		// Failing while coming up is expected, until the budget runs out.
		if now.Sub(st.upSince) > StartupBudget(h) {
			st.health = api.HealthUnhealthy
		}
	case st.health != api.HealthUnhealthy:
		if st.failures++; st.failures >= h.Retries {
			st.health = api.HealthUnhealthy
		}
	}
	became := st.health
	s.mu.Unlock()

	switch {
	case became == was:
	case became == api.HealthUnhealthy && was == api.HealthStarting:
		s.event(ctx, app, api.LevelError, "Replica %d did not become healthy within %s of starting: %v", r.Index, shortDuration(StartupBudget(h)), err)
	case became == api.HealthUnhealthy:
		s.event(ctx, app, api.LevelError, "Replica %d failed %d health checks in a row: %v", r.Index, h.Retries, err)
	case became == api.HealthHealthy && (was == api.HealthUnhealthy || was == api.HealthStarting):
		s.event(ctx, app, api.LevelInfo, "Replica %d is healthy", r.Index)
	}
}

func (s *supervisor) superviseDown(ctx context.Context, now time.Time, app store.Application, d store.Deployment, r store.Replica) {
	s.mu.Lock()
	st := s.state(r.ContainerID, d)
	firstSight := !st.exitSeen
	st.upSince = time.Time{}
	if d.Spec.Health != nil {
		st.health, st.failures = api.HealthUnhealthy, 0
	}
	s.mu.Unlock()

	// The first tick that finds a replica down decides its fate and says so,
	// once; later ticks only wait for the restart to fall due.
	if firstSight {
		c, err := s.e.rt.InspectContainer(ctx, r.ContainerID)
		if err != nil {
			s.logErr(ctx, "inspect exited replica", err)
			return // exitSeen stays false: the next tick tries again
		}
		why := fmt.Sprintf("exited with code %d", c.ExitCode)
		if c.OOMKilled {
			why = "was killed for exceeding its memory limit"
		}
		restart := shouldRestart(d.Spec.Restart.Policy, c)

		s.mu.Lock()
		st.exitSeen, st.leaveStopped = true, !restart
		delay := s.delay(st.restarts)
		st.nextRestart = now.Add(delay)
		s.mu.Unlock()

		if !restart {
			s.event(ctx, app, api.LevelWarn, "Replica %d %s; restart policy %q leaves it stopped", r.Index, why, d.Spec.Restart.Policy)
			return
		}
		s.event(ctx, app, api.LevelWarn, "Replica %d %s; restarting in %s", r.Index, why, shortDuration(delay))
	}

	s.mu.Lock()
	due := !st.leaveStopped && !now.Before(st.nextRestart)
	s.mu.Unlock()
	if due {
		s.restart(ctx, now, app, r, func() error { return s.e.rt.StartContainer(ctx, r.ContainerID) })
	}
}

// shouldRestart applies restart.policy to a replica that is down.
func shouldRestart(policy string, c docker.Container) bool {
	switch policy {
	case spec.RestartNever:
		return false
	case spec.RestartOnFailure:
		return c.ExitCode != 0 || c.OOMKilled
	}
	return true
}

// delay is the wait before restart number n+1 of a run of restarts.
func (s *supervisor) delay(restartsSoFar int) time.Duration {
	if restartsSoFar < len(s.e.opts.RestartDelays) {
		return s.e.opts.RestartDelays[restartsSoFar]
	}
	return s.e.opts.CrashLoopDelay
}

// restart brings a replica back with do, and advances its backoff.
func (s *supervisor) restart(ctx context.Context, now time.Time, app store.Application, r store.Replica, do func() error) {
	err := do()

	s.mu.Lock()
	st := s.states[r.ContainerID]
	if st == nil {
		s.mu.Unlock()
		return
	}
	st.restarts++
	st.lastRestart, st.exitSeen, st.upSince = now, false, time.Time{}
	st.failures, st.probing = 0, false
	if st.health != api.HealthNone {
		st.health = api.HealthStarting
	}
	// Also paces restarts of a replica that runs but never turns healthy.
	st.nextRestart = now.Add(s.delay(st.restarts))
	enteredCrashLoop := !st.crashLoop && st.restarts >= len(s.e.opts.RestartDelays)
	if enteredCrashLoop {
		st.crashLoop = true
	}
	attempt := st.restarts
	s.mu.Unlock()

	if err != nil {
		s.event(ctx, app, api.LevelError, "Replica %d could not be restarted (attempt %d): %v", r.Index, attempt, err)
	} else {
		s.event(ctx, app, api.LevelInfo, "Replica %d restarted (attempt %d)", r.Index, attempt)
		if err := s.e.store.IncrementReplicaRestarts(context.WithoutCancel(ctx), r.ContainerID); err != nil {
			s.e.log.Warn("could not count restart", "container", r.ContainerName, "error", err)
		}
	}
	if enteredCrashLoop {
		s.event(ctx, app, api.LevelError, "Replica %d is crash-looping: %d restarts without staying up. Retrying every %s",
			r.Index, attempt, shortDuration(s.e.opts.CrashLoopDelay))
	}
}

// snapshot reports what the supervisor knows about a container, for the API.
func (s *supervisor) snapshot(containerID string) (health string, crashLoop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[containerID]; ok {
		return st.health, st.crashLoop
	}
	return api.HealthNone, false
}

func (s *supervisor) event(ctx context.Context, app store.Application, level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.e.log.Log(ctx, slogLevel(level), msg, "app", app.Name)

	ctx = context.WithoutCancel(ctx)
	if err := s.e.store.AddEvent(ctx, app.ID, nil, level, api.EventSupervisor, msg, time.Now()); err != nil {
		s.e.log.Warn("could not record event", "app", app.Name, "error", err)
		return
	}
	if err := s.e.store.PruneApplicationEvents(ctx, app.ID, appEventsKept); err != nil {
		s.e.log.Warn("could not prune events", "app", app.Name, "error", err)
	}
}

func slogLevel(level string) slog.Level {
	switch level {
	case api.LevelWarn:
		return slog.LevelWarn
	case api.LevelError:
		return slog.LevelError
	}
	return slog.LevelInfo
}

func (s *supervisor) logErr(ctx context.Context, what string, err error) {
	if ctx.Err() == nil {
		s.e.log.Error("supervisor: "+what, "error", err)
	}
}

// shortDuration renders a duration without Go's trailing zero units:
// "5m" rather than "5m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
