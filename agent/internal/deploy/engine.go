// Package deploy contains the deployment engine: the state machine that takes
// an application spec and turns it into running containers, plus the
// application-level operations (stop, start, delete) built on the same parts.
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
	"github.com/shipwick/shipwick/agent/internal/health"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

var (
	// ErrBusy means another operation holds the application's lock.
	ErrBusy = errors.New("another operation is in progress for this application")
	// ErrNotDeployed means the application has no active deployment to act on.
	ErrNotDeployed  = errors.New("application has no active deployment")
	ErrShuttingDown = errors.New("agent is shutting down")
)

// Runtime is what the engine needs from the container runtime. It exists so
// the engine can be tested without a Docker daemon; *docker.Runtime is the
// production implementation.
type Runtime interface {
	EnsureNetwork(ctx context.Context) error
	PullImage(ctx context.Context, image string) error
	ImageExists(ctx context.Context, image string) (bool, error)
	CreateContainer(ctx context.Context, spec docker.ContainerSpec) (id, name string, err error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RestartContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	InspectContainer(ctx context.Context, id string) (docker.Container, error)
	ListContainers(ctx context.Context, app string) ([]docker.Container, error)
	Logs(ctx context.Context, id string, tail int) ([]docker.LogEntry, error)
	FollowLogs(ctx context.Context, id string, tail int, emit func(docker.LogEntry)) error
	Stats(ctx context.Context, id string, withPrevious bool) (docker.StatsSample, error)
	Info(ctx context.Context) (docker.Info, error)
}

type Options struct {
	// StabilizeWindow is how long freshly started replicas must stay running
	// before the deployment may proceed. It catches containers that crash on
	// boot (bad config, missing env, wrong command).
	StabilizeWindow time.Duration
	// StopTimeout is the grace period between SIGTERM and SIGKILL.
	StopTimeout time.Duration
	// DeployTimeout bounds a whole deployment, image pull included.
	DeployTimeout time.Duration
	// LockWait is how long a user operation waits for the supervisor to let
	// go of an application before giving up with ErrBusy.
	LockWait time.Duration

	// Probe performs one health check against a replica. The default is an
	// HTTP GET expecting 2xx; tests substitute their own.
	Probe ProbeFunc
	// StartupPollInterval paces health probes while a new replica is coming
	// up, when waiting a full health.interval between attempts would only
	// make deployments slow.
	StartupPollInterval time.Duration

	// SuperviseInterval is the supervisor's tick.
	SuperviseInterval time.Duration
	// RestartDelays is the backoff between consecutive restarts of a replica
	// that does not stay up. Once exhausted, the replica is crash-looping
	// and is retried every CrashLoopDelay.
	RestartDelays  []time.Duration
	CrashLoopDelay time.Duration
	// StableAfter is how long a replica must run before its restart history
	// is forgiven.
	StableAfter time.Duration

	// Proxy routes public traffic to applications. Nil disables routing:
	// domains are recorded but nothing serves them.
	Proxy Proxy
	// ExtraRoutes are served next to the applications' routes — the agent's
	// own API, when it is given a domain.
	ExtraRoutes []proxy.Route

	Logger *slog.Logger
}

// ProbeFunc checks one replica once. A nil error means healthy.
type ProbeFunc func(ctx context.Context, ip string, port int, path string, timeout time.Duration) error

func (o *Options) applyDefaults() {
	if o.LockWait == 0 {
		o.LockWait = 30 * time.Second
	}
	if o.Probe == nil {
		o.Probe = health.New().Check
	}
	if o.StartupPollInterval == 0 {
		o.StartupPollInterval = time.Second
	}
	if o.SuperviseInterval == 0 {
		o.SuperviseInterval = time.Second
	}
	if o.RestartDelays == nil {
		o.RestartDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	}
	if o.CrashLoopDelay == 0 {
		o.CrashLoopDelay = 5 * time.Minute
	}
	if o.StableAfter == 0 {
		o.StableAfter = time.Minute
	}
	if o.StabilizeWindow == 0 {
		o.StabilizeWindow = 3 * time.Second
	}
	if o.StopTimeout == 0 {
		o.StopTimeout = 10 * time.Second
	}
	if o.DeployTimeout == 0 {
		o.DeployTimeout = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// cleanupTimeout bounds work that must happen even when the triggering
// context is already dead: marking a deployment FAILED, removing containers.
const cleanupTimeout = 2 * time.Minute

type Engine struct {
	store *store.Store
	rt    Runtime
	opts  Options
	log   *slog.Logger

	// baseCtx parents every background deployment; cancel aborts them.
	baseCtx context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	closed bool
	locks  map[string]*appLock // applications with an operation in progress
	wg     sync.WaitGroup      // one count per held lock
	bg     sync.WaitGroup      // the supervisor loop

	sup     *supervisor
	metrics *metricsCache

	// routeOverrides: see routeVia. Guarded by mu.
	routeOverrides map[string]routeOverride
}

// appLock records who holds an application, because the two kinds of holder
// deserve different treatment from a user operation that finds it taken.
type appLock struct {
	bySupervisor bool
	released     chan struct{} // closed on release
}

func New(st *store.Store, rt Runtime, opts Options) *Engine {
	opts.applyDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		store:   st,
		rt:      rt,
		opts:    opts,
		log:     opts.Logger,
		baseCtx: ctx,
		cancel:  cancel,
		locks:   map[string]*appLock{},

		routeOverrides: map[string]routeOverride{},
	}
	e.sup = newSupervisor(e)
	e.metrics = newMetricsCache()
	return e
}

// lock claims the application for a user operation. Deployments, stop, start,
// delete and the supervisor all contend for the same lock, so they never
// interleave.
//
// If another user operation holds it, the caller is told at once (ErrBusy):
// that conflict is theirs to resolve. If the supervisor holds it — it does so
// for moments at a time, to restart a replica — the caller waits instead:
// "operation in progress" would be a baffling answer to a lone `deploy`.
func (e *Engine) lock(ctx context.Context, app string) error {
	deadline := time.NewTimer(e.opts.LockWait)
	defer deadline.Stop()
	for {
		released, err := e.tryLock(app, false)
		if released == nil {
			return err
		}
		select {
		case <-released:
		case <-deadline.C:
			return ErrBusy
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// tryLock attempts to take the lock without blocking. On success both results
// are nil. When the supervisor holds the lock, it returns a channel that is
// closed on release; in every other case, the error to report.
func (e *Engine) tryLock(app string, bySupervisor bool) (wait <-chan struct{}, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrShuttingDown
	}
	if held, ok := e.locks[app]; ok {
		if held.bySupervisor && !bySupervisor {
			return held.released, nil
		}
		return nil, ErrBusy
	}
	e.locks[app] = &appLock{bySupervisor: bySupervisor, released: make(chan struct{})}
	// Counted under the same mutex that guards closed, so Shutdown can never
	// start waiting while an operation is about to begin.
	e.wg.Add(1)
	return nil, nil
}

func (e *Engine) unlock(app string) {
	e.release(app)
	e.wg.Done()
}

// release frees the application for the next operation without ending the
// current one as far as Wait and Shutdown are concerned.
func (e *Engine) release(app string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if held, ok := e.locks[app]; ok {
		close(held.released)
		delete(e.locks, app)
	}
}

// Deploy records a new deployment and runs it in the background. It returns
// as soon as the PENDING record exists; progress is observable through the
// deployment's status and events.
func (e *Engine) Deploy(ctx context.Context, app spec.App) (store.Deployment, error) {
	return e.start(ctx, app.Name, func(context.Context) (origin, error) {
		return origin{spec: app, kind: api.KindDeploy}, nil
	})
}

// origin is what a deployment is made from: a spec, and where it came from.
type origin struct {
	spec     spec.App
	kind     string
	sourceID *int64 // the deployment whose stored spec this is, if any
}

// start is the one way a deployment begins, whatever its origin. resolve runs
// under the application's lock, so that "the active deployment" it may look up
// is still the active deployment when the new record is created.
func (e *Engine) start(ctx context.Context, name string, resolve func(context.Context) (origin, error)) (store.Deployment, error) {
	if err := e.lock(ctx, name); err != nil {
		return store.Deployment{}, err
	}
	o, err := resolve(ctx)
	if err != nil {
		e.unlock(name)
		return store.Deployment{}, err
	}
	app := o.spec
	// Refused up front, as a config error: nothing is recorded, pulled or started.
	if err := e.checkDomain(ctx, app.Name, app.Domain); err != nil {
		e.unlock(name)
		return store.Deployment{}, err
	}
	d, err := e.store.CreateDeploymentFrom(ctx, app, o.kind, o.sourceID, time.Now())
	if err != nil {
		e.unlock(name)
		return store.Deployment{}, err
	}
	e.log.Info("deployment created", "app", d.Application, "deployment", d.ID, "version", d.Version, "kind", d.Kind)

	go func() {
		defer e.wg.Done()
		e.run(d)

		// completed_at is the signal clients wait for before issuing the next
		// operation, so the lock must be free by the time it becomes visible.
		e.release(name)
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := e.store.CompleteDeployment(ctx, d.ID, time.Now()); err != nil {
			e.log.Error("could not stamp deployment completion", "deployment", d.ID, "error", err)
		}
	}()
	return d, nil
}

// Wait blocks until every operation in progress, background deployments
// included, has finished.
func (e *Engine) Wait() {
	e.wg.Wait()
}

// Shutdown rejects new operations, aborts in-flight deployments (each is
// marked FAILED and cleaned up) and waits for running operations until ctx
// expires.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		e.bg.Wait()         // the supervisor loop: no more probes are launched after this
		e.sup.probes.Wait() // in-flight probes may still record events
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("operations still running at shutdown: %w", ctx.Err())
	}
}

// pullImage refreshes the image from its registry. If the pull fails but the
// image is available locally (built on the server, registry briefly down), the
// local copy is used and the fallback is recorded.
func (e *Engine) pullImage(ctx context.Context, d *store.Deployment) error {
	pullErr := e.rt.PullImage(ctx, d.Image)
	if pullErr == nil {
		e.step(ctx, d, "Pulled image %s", d.Image)
		return nil
	}
	if ctx.Err() != nil {
		return pullErr
	}
	exists, err := e.rt.ImageExists(ctx, d.Image)
	if err != nil || !exists {
		return pullErr
	}
	e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not pull %s, using the local copy (%v)", d.Image, pullErr))
	return nil
}

// awaitStable watches the new replicas for the stabilization window and fails
// as soon as one of them stops running.
func (e *Engine) awaitStable(ctx context.Context, d *store.Deployment, replicas []store.Replica) error {
	deadline := time.NewTimer(e.opts.StabilizeWindow)
	defer deadline.Stop()
	poll := time.NewTicker(max(e.opts.StabilizeWindow/10, 10*time.Millisecond))
	defer poll.Stop()

	for {
		for _, r := range replicas {
			c, err := e.rt.InspectContainer(ctx, r.ContainerID)
			if err != nil {
				return fmt.Errorf("replica %d: %w", r.Index, err)
			}
			if c.Running {
				continue
			}
			e.captureLogs(ctx, d, r)
			if c.OOMKilled {
				return fmt.Errorf("replica %d was killed for exceeding its memory limit shortly after start", r.Index)
			}
			return fmt.Errorf("replica %d exited with code %d shortly after start", r.Index, c.ExitCode)
		}

		select {
		case <-deadline.C:
			return nil
		case <-poll.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// StartupBudget is how long a freshly started replica has to pass its first
// health check: interval × retries, 30s with the defaults. Slow starters
// (JVMs, apps that migrate a database on boot) raise retries.
func StartupBudget(h *spec.Health) time.Duration {
	return h.Interval.Std() * time.Duration(h.Retries)
}

// awaitHealthy waits until every new replica has answered its health check
// once. Replicas are probed every StartupPollInterval rather than every
// health.interval: a connection refused by an app that is still booting is
// not a failed check, it is "not yet" — and a fast app should not make its
// deployment wait ten seconds to be told it was ready after one.
func (e *Engine) awaitHealthy(ctx context.Context, d *store.Deployment, replicas []store.Replica) error {
	h := d.Spec.Health
	budget := StartupBudget(h)
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	poll := time.NewTicker(e.opts.StartupPollInterval)
	defer poll.Stop()

	pending := append([]store.Replica(nil), replicas...)
	lastErr := map[int]error{}
	for {
		stillPending := pending[:0]
		for _, r := range pending {
			c, err := e.rt.InspectContainer(ctx, r.ContainerID)
			if err != nil {
				return fmt.Errorf("replica %d: %w", r.Index, err)
			}
			if !c.Running {
				e.captureLogs(ctx, d, r)
				if c.OOMKilled {
					return fmt.Errorf("replica %d was killed for exceeding its memory limit before it became healthy", r.Index)
				}
				return fmt.Errorf("replica %d exited with code %d before it became healthy", r.Index, c.ExitCode)
			}
			if c.IP == "" {
				lastErr[r.Index] = errors.New("container has no address on the Shipwick network yet")
				stillPending = append(stillPending, r)
				continue
			}
			if err := e.opts.Probe(ctx, c.IP, d.Spec.Port, h.Path, h.Timeout.Std()); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr[r.Index] = err
				stillPending = append(stillPending, r)
			}
		}
		pending = stillPending
		if len(pending) == 0 {
			return nil
		}

		select {
		case <-deadline.C:
			r := pending[0]
			e.captureLogs(ctx, d, r)
			return fmt.Errorf("replica %d did not become healthy within %s: GET %s on port %d: %v",
				r.Index, shortDuration(budget), h.Path, d.Spec.Port, lastErr[r.Index])
		case <-poll.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// captureLogs preserves the last output of a crashed replica as an event. Its
// container is about to be removed, and with it the only clue to the crash.
func (e *Engine) captureLogs(ctx context.Context, d *store.Deployment, r store.Replica) {
	const lines, maxBytes = 20, 4096
	entries, err := e.rt.Logs(ctx, r.ContainerID, lines)
	if err != nil || len(entries) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Last output of replica %d:", r.Index)
	for _, entry := range entries {
		b.WriteString("\n")
		b.WriteString(entry.Message)
	}
	msg := b.String()
	if len(msg) > maxBytes {
		msg = msg[:maxBytes] + "\n… (truncated)"
	}
	e.event(ctx, d, api.LevelError, api.EventLog, msg)
}

// retireOthers removes every container of the application that does not
// belong to the now-active deployment. Sweeping by label, rather than only
// the previous deployment's replicas, also collects any stray leftovers.
func (e *Engine) retireOthers(ctx context.Context, d *store.Deployment, previous *store.Deployment) {
	containers, err := e.rt.ListContainers(ctx, d.Application)
	if err != nil {
		e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not list old containers: %v", err))
		return
	}
	var old []docker.Container
	for _, c := range containers {
		if c.DeploymentID != d.ID {
			old = append(old, c)
		}
	}
	removed := 0
	for i, err := range e.retireAll(ctx, old) {
		if err != nil {
			e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not remove old container %s: %v", old[i].Name, err))
			continue
		}
		removed++
	}
	if previous != nil && removed > 0 {
		e.step(ctx, d, "Removed %s of %s", plural(removed, "container"), previous.Version)
	}
}

// retireAll retires containers concurrently, so the total time is one grace
// period rather than one per container. The result is aligned with the input.
func (e *Engine) retireAll(ctx context.Context, containers []docker.Container) []error {
	return parallel(containers, func(c docker.Container) error {
		return e.retireContainer(ctx, c.ID)
	})
}

// retireContainer stops a container gracefully, then removes it.
func (e *Engine) retireContainer(ctx context.Context, id string) error {
	if err := e.rt.StopContainer(ctx, id, e.opts.StopTimeout); err != nil {
		e.log.Warn("graceful stop failed, forcing removal", "container", id, "error", err)
	}
	return e.removeContainer(ctx, id)
}

// parallel runs fn for every item concurrently and returns the errors aligned
// with items. Item counts are bounded by the replica limit, so no pool is needed.
func parallel[T any](items []T, fn func(T) error) []error {
	errs := make([]error, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(item)
		}()
	}
	wg.Wait()
	return errs
}

func (e *Engine) removeContainer(ctx context.Context, id string) error {
	if err := e.rt.RemoveContainer(ctx, id); err != nil {
		return err
	}
	return e.store.MarkReplicaRemoved(ctx, id, time.Now())
}

func (e *Engine) transition(ctx context.Context, d *store.Deployment, to api.DeploymentStatus) error {
	return e.transitionWithError(ctx, d, to, "")
}

func (e *Engine) transitionWithError(ctx context.Context, d *store.Deployment, to api.DeploymentStatus, errMsg string) error {
	if !CanTransition(d.Status, to) {
		return fmt.Errorf("illegal state transition %s → %s", d.Status, to)
	}
	if err := e.store.TransitionDeployment(ctx, d.ID, d.Status, to, errMsg); err != nil {
		return err
	}
	d.Status = to

	level, msg := api.LevelInfo, string(to)
	if to == api.StatusFailed {
		level, msg = api.LevelError, fmt.Sprintf("%s: %s", to, errMsg)
	}
	e.event(ctx, d, level, api.EventState, msg)
	return nil
}

func (e *Engine) step(ctx context.Context, d *store.Deployment, format string, args ...any) {
	e.event(ctx, d, api.LevelInfo, api.EventStep, fmt.Sprintf(format, args...))
}

// event records a deployment event. Events are a progress log for humans: a
// failure to write one is logged but never fails the deployment.
func (e *Engine) event(ctx context.Context, d *store.Deployment, level, typ, message string) {
	ctx = context.WithoutCancel(ctx)
	if err := e.store.AddEvent(ctx, d.ApplicationID, &d.ID, level, typ, message, time.Now()); err != nil {
		e.log.Warn("could not record event", "deployment", d.ID, "error", err)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
