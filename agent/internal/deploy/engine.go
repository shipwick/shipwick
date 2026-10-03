// Package deploy contains the deployment engine: the state machine that takes
// an application spec and turns it into running containers, plus the
// application-level operations (stop, start, delete) built on the same parts.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/health"
	"github.com/shipwick/shipwick/agent/internal/notify"
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
	// PullImage pulls with the credential given; with none, with whatever
	// the Docker configuration file on the server offers for the registry.
	PullImage(ctx context.Context, image string, auth *docker.RegistryAuth) error
	ImageExists(ctx context.Context, image string) (bool, error)
	// RemoveImage untags an image, or returns docker.ErrImageInUse if a
	// container still uses it.
	RemoveImage(ctx context.Context, image string) error
	CreateContainer(ctx context.Context, spec docker.ContainerSpec) (id, name string, err error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error
	// SetServiceNames replaces the names a container answers to on the
	// services network; none takes it out of service discovery.
	SetServiceNames(ctx context.Context, id string, names []string) error
	InspectContainer(ctx context.Context, id string) (docker.Container, error)
	ListContainers(ctx context.Context, app string) ([]docker.Container, error)
	Logs(ctx context.Context, id string, tail int) ([]docker.LogEntry, error)
	FollowLogs(ctx context.Context, id string, tail int, emit func(docker.LogEntry)) error
	Stats(ctx context.Context, id string, withPrevious bool) (docker.StatsSample, error)
	Info(ctx context.Context) (docker.Info, error)
	// Exec runs cmd inside a running container: its exit code and the tail of
	// its output, bounded by timeout. Command health checks use it.
	Exec(ctx context.Context, id string, cmd []string, timeout time.Duration) (exitCode int, output string, err error)
	// WaitContainer blocks until the container stops and returns its exit code.
	WaitContainer(ctx context.Context, id string) (int, error)
	// ExportPath streams a tar archive of a path inside the container;
	// ImportPath extracts one into it. Volume backups use them.
	ExportPath(ctx context.Context, id, path string) (io.ReadCloser, error)
	ImportPath(ctx context.Context, id, path string, archive io.Reader) error
	// LoadImage loads an image archive (the `docker save` format) and returns
	// the references it carried. ListImages lists the references under one
	// repository, such as shipwick.local/<app>.
	LoadImage(ctx context.Context, archive io.Reader) ([]string, error)
	ListImages(ctx context.Context, repository string) ([]string, error)
	// ListVolumes lists the volumes Shipwick created, with the application
	// each belongs to; RemoveVolume removes one of them.
	ListVolumes(ctx context.Context) ([]docker.Volume, error)
	RemoveVolume(ctx context.Context, app, volume string) error
	// ProxyContainer is the reverse proxy's own container, for the files it
	// serves itself.
	ProxyContainer(ctx context.Context) (string, error)
	// ImageLayers returns the diff IDs of every image's layers, base layer
	// first: what the daemon has, for a client that asks what to send.
	ImageLayers(ctx context.Context) ([][]string, error)
	// RegistryLogin checks a credential against its registry and keeps
	// nothing; a credential that is not accepted is a *docker.LoginError.
	RegistryLogin(ctx context.Context, auth docker.RegistryAuth) error
	// FollowOutput streams the lines a container writes to standard output
	// from `since` on, until it stops. The proxy's access log is read with it.
	FollowOutput(ctx context.Context, id string, since time.Time, emit func(line []byte)) error
}

type Options struct {
	// StabilizeWindow is how long freshly started replicas must stay running
	// before the deployment may proceed. It catches containers that crash on
	// boot (bad config, missing env, wrong command).
	StabilizeWindow time.Duration
	// StopTimeout is the grace period between SIGTERM and SIGKILL.
	StopTimeout time.Duration
	// NameSettle is how long a rollout waits between giving a new replica its
	// names and stopping the replica it replaces. The proxy asks who stands
	// behind a name about once a second; an application with a single replica
	// would otherwise spend that second with a proxy that only knows the one
	// that just stopped.
	NameSettle time.Duration
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
	// LookupHost resolves a hostname; an application's hostname is handed to
	// the proxy only once it points at this server (see dns.go). The default
	// is the system resolver; tests substitute their own.
	LookupHost func(ctx context.Context, host string) ([]string, error)
	// ServerAddresses are the server's public addresses, as far as they are
	// known. Empty means unknown, and a hostname only has to resolve at all.
	ServerAddresses []string

	Logger *slog.Logger

	// ReservedHostPorts are server ports an application may not publish: the
	// ones the agent and the proxy listen on.
	ReservedHostPorts []int
	// ProbeTCP performs one TCP health check: a connection to the port is
	// accepted, or not. Command checks need no option; they run through the
	// Runtime.
	ProbeTCP func(ctx context.Context, ip string, port int, timeout time.Duration) error

	// Notifier is told about deployment outcomes and about applications
	// going down and recovering. Nil: nobody is told.
	Notifier notify.Notifier

	// SampleInterval is how often the resource usage of every running
	// replica is recorded for the metrics history; MetricsRetention is how
	// long those samples are kept.
	SampleInterval   time.Duration
	MetricsRetention time.Duration

	// UploadDir is where the folders of static applications wait for the
	// deployment that serves them, one archive per application. Empty means
	// static applications cannot be uploaded to this agent.
	UploadDir string

	// DashboardURL is where the dashboard is served, for clients that want
	// to send the user there. Empty: it has no hostname.
	DashboardURL string
	// AlertMemoryPercent is the share of its memory limit at which a replica
	// raises an alert, AlertDiskPercent how full the disk may get before it
	// does. DiskUsage measures that disk; nil, or not ok, means it cannot be
	// measured here, and there is no disk alert.
	AlertMemoryPercent int
	AlertDiskPercent   int
	DiskUsage          func() (usage api.DiskUsage, ok bool)
	// DNSChallenge says that the proxy obtains certificates through a DNS
	// record rather than from the server itself. A hostname may then stand
	// behind Cloudflare's proxy, and may be a wildcard.
	DNSChallenge bool
	// ProxyTLSAddr is where the proxy accepts TLS connections, as the agent
	// reaches it: the certificate each hostname is served with is read there.
	// CertificateProbe does the reading; tests substitute their own.
	ProxyTLSAddr     string
	CertificateProbe CertificateProbe
	// Backups is where the agent keeps the backups it takes, and what it
	// backs up of itself: see backups.go.
	Backups BackupOptions
	// Transfer is what the agent does on a schedule for the server that would
	// replace it, or as that server: see export.go and standby.go.
	Transfer TransferOptions
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
	if o.ProbeTCP == nil {
		o.ProbeTCP = health.CheckTCP
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
	if o.NameSettle == 0 {
		o.NameSettle = 1500 * time.Millisecond
	}
	if o.DeployTimeout == 0 {
		o.DeployTimeout = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.LookupHost == nil {
		o.LookupHost = PublicLookupHost
	}
	if o.SampleInterval == 0 {
		o.SampleInterval = 30 * time.Second
	}
	if o.MetricsRetention == 0 {
		o.MetricsRetention = 7 * 24 * time.Hour
	}
	if o.AlertMemoryPercent == 0 {
		o.AlertMemoryPercent = defaultAlertMemoryPercent
	}
	if o.AlertDiskPercent == 0 {
		o.AlertDiskPercent = defaultAlertDiskPercent
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
	// ops counts operations in progress: one per lock taken, until the
	// operation is done (a deployment outlives its lock by its bookkeeping).
	// A counter under mu rather than a WaitGroup, because Wait may be called
	// at any time — also while the supervisor is starting new operations,
	// which a WaitGroup does not allow once its counter has reached zero.
	ops  int
	idle *sync.Cond     // on mu; signalled when ops drops to zero
	bg   sync.WaitGroup // the supervisor loop

	sup     *supervisor
	metrics *metricsCache
	jobs    *jobRunner

	// routing serializes syncRouting and everything else that renames replicas.
	routing    sync.Mutex
	lastRoutes string // what the proxy was last told, for the log; guarded by routing
	// heldBack are the hostnames kept out of the proxy because they do not
	// point at this server yet, with the reason: see dns.go. Guarded by
	// routing; lastHeld is its one-line summary, for the log.
	heldBack map[string]string
	lastHeld string
	dns      *hostnameCache
	// routeOverrides: see routeVia. Guarded by mu.
	routeOverrides map[string]routeOverride
	// names are the names each container answers to on the services network,
	// as far as this agent gave or found them: see setNames. Guarded by mu.
	names map[string][]string

	// alerts are the conditions that hold right now: see alerts.go.
	alerts alertBook
	// certs are the certificates the operator supplied: see certificates.go.
	certs certificateCache

	// hashes are the bcrypt hashes of the basic-auth accounts in the routes:
	// see accounts.go.
	hashes hashCache

	// traffic is what the proxy's access log said (traffic.go), certStatus
	// what its certificates look like (certstatus.go).
	traffic    *trafficRecorder
	certStatus *certWatch
	// drains are the containers being retired in the background, by ID: see
	// drain.go. Guarded by mu.
	drains map[string]*drain
	// backups is when each application was last looked at for a backup that
	// is due: see backups.go.
	backups *backupSchedule
	// transfer is the import that runs or ran last, and how far the export
	// and standby schedules have looked: see import.go.
	transfer *transfer
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

		heldBack:       map[string]string{},
		dns:            newHostnameCache(),
		routeOverrides: map[string]routeOverride{},
		names:          map[string][]string{},
	}
	e.idle = sync.NewCond(&e.mu)
	e.drains = map[string]*drain{}
	e.sup = newSupervisor(e)
	e.metrics = newMetricsCache()
	e.jobs = newJobRunner()
	e.traffic = newTrafficRecorder()
	e.certStatus = newCertWatch()
	e.backups = newBackupSchedule()
	e.transfer = newTransfer()
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
	e.ops++
	return nil, nil
}

func (e *Engine) unlock(app string) {
	e.release(app)
	e.opDone()
}

// opDone ends an operation begun by a successful tryLock.
func (e *Engine) opDone() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ops--
	if e.ops == 0 {
		e.idle.Broadcast()
	}
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
	return e.start(ctx, app.Name, func(ctx context.Context) (origin, error) {
		// The one place a spec arrives from outside: whatever the CLI left
		// for the server to fill in is filled in here, and stored filled in.
		app, err := e.resolveSecrets(ctx, app)
		if err != nil {
			return origin{}, err
		}
		return origin{spec: app, kind: api.KindDeploy}, nil
	})
}

// admit is what every deployment passes before it is recorded, whatever its
// origin; a refusal here is a config error, and nothing has been recorded,
// pulled or started. Validate runs the same checks for a document that is not
// being deployed yet: beforeBuild then lets a `build` without its image pass,
// because the image is built once the document is known to be acceptable.
func (e *Engine) admit(ctx context.Context, app spec.App, beforeBuild bool) error {
	if app.Build != nil && app.Image == "" && !beforeBuild {
		return ErrImageNotBuilt
	}
	if err := e.checkDomain(ctx, app); err != nil {
		return err
	}
	if err := e.checkWildcards(ctx, app); err != nil {
		return err
	}
	return e.checkPublish(ctx, app.Name, app.Publish)
}

// Validate answers what Deploy would answer for app, short of deploying it:
// nothing is recorded, pulled or started, and no lock is taken — a deployment
// that is running for the application neither delays the answer nor notices
// the question. It is asked before an image is built or a folder uploaded, so
// neither has to exist yet.
func (e *Engine) Validate(ctx context.Context, app spec.App) error {
	app, err := e.resolveSecrets(ctx, app)
	if err != nil {
		return err
	}
	return e.admit(ctx, app, true)
}

// origin is what a deployment is made from: a spec, and where it came from.
type origin struct {
	spec     spec.App
	kind     string
	sourceID *int64 // the deployment whose stored spec this is, if any
	// static is the uploaded folder a static application serves; nil for an
	// application that runs containers.
	static *store.StaticFiles
	// dormant: the deployment is to exist without running (see
	// executeDormant). An import sets it for an application that was stopped
	// where it comes from.
	dormant bool
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
	if err := e.admit(ctx, app, false); err != nil {
		e.unlock(name)
		return store.Deployment{}, err
	}
	var d store.Deployment
	if o.static != nil {
		d, err = e.store.CreateStaticDeployment(ctx, app, o.kind, o.sourceID, actorFrom(ctx), *o.static, time.Now())
	} else {
		d, err = e.store.CreateDeploymentFrom(ctx, app, o.kind, o.sourceID, actorFrom(ctx), time.Now())
	}
	if err != nil {
		e.unlock(name)
		return store.Deployment{}, err
	}
	e.log.Info("deployment created", "app", d.Application, "deployment", d.ID, "version", d.Version, "kind", d.Kind)
	r := e.newRollout(d)
	r.dormant = o.dormant
	e.launch(r)
	return d, nil
}

// launch runs a deployment in the background. The caller holds the
// application's lock and hands it over.
func (e *Engine) launch(r *rollout) {
	d := r.d
	go func() {
		defer e.opDone()
		interrupted := e.run(r)

		// completed_at is the signal clients wait for before issuing the next
		// operation, so the lock must be free by the time it becomes visible.
		e.release(d.Application)
		if interrupted {
			// Not completed: the agent that starts next takes it from here,
			// and whoever polls it keeps polling.
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := e.store.CompleteDeployment(ctx, d.ID, time.Now()); err != nil {
			e.log.Error("could not stamp deployment completion", "deployment", d.ID, "error", err)
		}
	}()
}

// Wait blocks until every operation in progress, background deployments
// included, has finished.
func (e *Engine) Wait() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for e.ops > 0 {
		e.idle.Wait()
	}
}

// Shutdown rejects new operations, interrupts in-flight deployments (each
// stays as it is, for the next start to resume: see resume.go) and waits for
// running operations until ctx expires.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()

	done := make(chan struct{})
	go func() {
		e.Wait()
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
// local copy is used and the fallback is recorded. An image built on a
// developer's machine has no registry (see localimages.go): it is either here
// already or the deployment cannot go on.
func (e *Engine) pullImage(ctx context.Context, d *store.Deployment) error {
	if spec.IsLocalImage(d.Image) {
		exists, err := e.rt.ImageExists(ctx, d.Image)
		if err != nil {
			return err
		}
		if !exists {
			return localImageMissing(d.Image)
		}
		e.step(ctx, d, "Using image %s, sent from a developer's machine", d.Image)
		return nil
	}
	pullErr := e.pull(ctx, d.Image)
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
// health check: start_period + interval × retries, 30s with the defaults.
// Slow starters (JVMs, apps that migrate a database on boot) set a
// start_period, which buys them time at startup without making a running
// replica's failures take longer to notice. The supervisor grants the same
// budget to a replica it restarted.
func StartupBudget(h *spec.Health) time.Duration {
	return h.StartPeriod.Std() + h.Interval.Std()*time.Duration(h.Retries)
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
			if err := e.probeReplica(ctx, d, c); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr[r.Index] = err
				stillPending = append(stillPending, r)
				continue
			}
			// The supervisor may still call it "starting" from a restart on
			// request; it has just answered, and the status must say so.
			e.sup.markHealthy(c.ID)
		}
		pending = stillPending
		if len(pending) == 0 {
			return nil
		}

		select {
		case <-deadline.C:
			r := pending[0]
			e.captureLogs(ctx, d, r)
			return fmt.Errorf("replica %d did not become healthy within %s: %v",
				r.Index, shortDuration(budget), withCheck(h, d.Spec.Port, lastErr[r.Index]))
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
		if c.Job != "" {
			continue // a job of the old version runs to its end
		}
		if e.draining(c.ID) {
			continue // replaced a moment ago and on its way out: see drain.go
		}
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
		return e.retireContainer(ctx, c.ID, e.gracePeriodOf(ctx, c.DeploymentID))
	})
}

// retireContainer stops a container gracefully, with timeout as its grace
// period, then removes it.
func (e *Engine) retireContainer(ctx context.Context, id string, timeout time.Duration) error {
	if err := e.rt.StopContainer(ctx, id, timeout); err != nil {
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
