package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
	"github.com/shipwick/shipwick/pkg/spec"
)

// One-off containers. The pre-deploy hook, scheduled jobs and `shipwick run`
// are the same thing at three moments: a container from the application's
// image, with its environment and limits, that runs one command and is gone.
// launchJob and collectJob are the one way such a container comes and goes.
//
// What a job never gets is the application's volumes. A replica may be
// writing them, and two writers on one volume is how data gets lost — the
// reason the recreate strategy exists. A job that needs the data goes through
// the application, like everybody else.

// ErrJobRunning means an earlier run of the job has not finished yet.
var ErrJobRunning = errors.New("a run of this job is still in progress")

// errJobTimeout is collectJob's verdict when the container outlived its timeout.
var errJobTimeout = errors.New("timed out")

// InvalidCommandError is returned by RunCommand for a command that cannot be
// handed to a container.
type InvalidCommandError struct{ Reason string }

func (e *InvalidCommandError) Error() string { return "command " + e.Reason }

const (
	// jobOutputLines and jobOutputBytes bound what a run keeps of its
	// container's output; the container itself is removed.
	jobOutputLines = 200
	jobOutputBytes = 64 * 1024
	// hookOutputLines is how much of a failed hook's output goes into the
	// deployment's events, next to the error it caused.
	hookOutputLines = 20
	// jobRunsKept bounds the history of one job.
	jobRunsKept = 50

	// The job names of runs that have no job in deploy.yaml. Both are
	// reserved (spec.ReservedJobNames).
	hookJobName    = "pre-deploy"
	commandJobName = "run"
)

// jobRunner is the engine's memory of jobs: which are running, so that a
// schedule that fires again before the last run ended does not start a second
// one, and up to which minute the scheduler has looked at each application.
type jobRunner struct {
	mu       sync.Mutex
	active   map[string]bool      // app + "/" + job
	lastTick map[string]time.Time // by application
}

func newJobRunner() *jobRunner {
	return &jobRunner{active: map[string]bool{}, lastTick: map[string]time.Time{}}
}

func (j *jobRunner) claim(app, job string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	key := app + "/" + job
	if j.active[key] {
		return false
	}
	j.active[key] = true
	return true
}

func (j *jobRunner) release(app, job string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.active, app+"/"+job)
}

// runJob runs one job to its end in the caller's goroutine, for the pre-deploy
// hook, which the deployment waits for. It returns the exit code; errJobTimeout
// when the container had to be stopped; ctx's error when the agent shut down
// under it; and any other error when the container could not be run at all.
func (e *Engine) runJob(ctx context.Context, d store.Deployment, run store.JobRun, timeout time.Duration) (int, error) {
	id, err := e.launchJob(ctx, d, run)
	if err != nil {
		return 0, err
	}
	return e.collectJob(ctx, run, id, timeout)
}

// launchJob creates and starts the run's container from the deployment's image
// and environment. It happens under the application's lock — the caller's —
// so that a delete or a deployment sweeping the application's containers
// either sees the container or comes before the run exists; created in the
// background, the container could slip in between and outlive its
// application. A run whose container never started is finished here, failed.
func (e *Engine) launchJob(ctx context.Context, d store.Deployment, run store.JobRun) (id string, err error) {
	cspec := docker.ContainerSpec{
		App:          d.Application,
		DeploymentID: d.ID,
		Sequence:     d.Sequence,
		Image:        d.Spec.Image,
		Env:          d.Spec.Env,
		NanoCPUs:     d.Spec.Resources.NanoCPUs(),
		MemoryBytes:  d.Spec.Resources.MemoryBytes,
		// The image's entrypoint stays unless deploy.yaml overrides it; the
		// job's command replaces the image's.
		Entrypoint: d.Spec.Entrypoint,
		Command:    run.Command,
		User:       d.Spec.User,
		Job:        &docker.JobSpec{Name: run.Job, RunID: run.ID},
		Init:       d.Spec.Init,
	}
	id, name, err := e.createContainer(ctx, cspec)
	if err != nil {
		e.finishRun(run, api.RunFailed, nil, "could not create the container: "+err.Error())
		return "", err
	}
	if err := e.rt.StartContainer(ctx, id); err != nil {
		e.finishRun(run, api.RunFailed, nil, "could not start the container: "+err.Error())
		if rerr := e.rt.RemoveContainer(context.WithoutCancel(ctx), id); rerr != nil {
			e.log.Warn("could not remove job container", "container", name, "error", rerr)
		}
		return "", err
	}
	return id, nil
}

// collectJob waits for a launched container within timeout, records the
// outcome and the tail of the output on the run, and removes the container.
// Whatever state ctx is in by the end, the container goes and the outcome is
// kept.
func (e *Engine) collectJob(ctx context.Context, run store.JobRun, id string, timeout time.Duration) (int, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	defer func() {
		e.archiveLogs(cleanup, id, logEnd{removing: true})
		if err := e.rt.RemoveContainer(cleanup, id); err != nil {
			e.log.Warn("could not remove job container", "container", id, "error", err)
		}
	}()

	waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
	defer cancelWait()
	code, err := e.rt.WaitContainer(waitCtx, id)
	if err != nil {
		if stopErr := e.rt.StopContainer(cleanup, id, e.opts.StopTimeout); stopErr != nil {
			e.log.Warn("could not stop job container", "container", id, "error", stopErr)
		}
		output := e.jobOutput(cleanup, id)
		switch {
		case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
			e.finishRun(run, api.RunInterrupted, nil, output)
			return 0, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			e.finishRun(run, api.RunTimedOut, nil, output)
			return 0, errJobTimeout
		}
		e.finishRun(run, api.RunFailed, nil, output)
		return 0, err
	}

	status := api.RunSucceeded
	if code != 0 {
		status = api.RunFailed
	}
	e.finishRun(run, status, &code, e.jobOutput(cleanup, id))
	return code, nil
}

// finishRun records a run's outcome on a fresh context: the run's own may be
// dead, and the outcome must not be lost for it.
func (e *Engine) finishRun(run store.JobRun, status api.RunStatus, code *int, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := e.store.FinishJobRun(ctx, run.ID, status, code, output, time.Now()); err != nil {
		e.log.Error("could not record the outcome of a job run", "run", run.ID, "error", err)
	}
}

// jobOutput is the tail of what the container wrote, as one text, bounded.
// Its container is about to be removed, and this is all that remains of it.
func (e *Engine) jobOutput(ctx context.Context, id string) string {
	entries, err := e.rt.Logs(ctx, id, jobOutputLines)
	if err != nil {
		return ""
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		lines[i] = entry.Message
	}
	out := strings.Join(lines, "\n")
	if len(out) > jobOutputBytes {
		out = out[len(out)-jobOutputBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
		out = "… (truncated)\n" + out
	}
	return out
}

// runHook runs the deployment's pre-deploy command, once the image is there
// and before any replica of the new version exists. It runs next to the
// version that is serving — under recreate too, whose replicas stop only
// afterwards — so what it does must be safe next to that version: a
// backward-compatible migration, not a destructive one.
func (e *Engine) runHook(ctx context.Context, d *store.Deployment) error {
	hook := d.Spec.PreDeploy
	e.step(ctx, d, "Running pre-deploy command")
	run, err := e.store.CreateJobRun(ctx, store.JobRun{
		ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: hookJobName, Kind: api.RunKindHook, Command: hook.Command,
	}, time.Now())
	if err != nil {
		return err
	}
	started := time.Now()
	code, err := e.runJob(ctx, *d, run, hook.Timeout.Std())
	switch {
	case errors.Is(err, errJobTimeout):
		e.hookOutput(ctx, d, run.ID)
		return fmt.Errorf("pre-deploy command timed out after %s", shortDuration(hook.Timeout.Std()))
	case err != nil:
		return fmt.Errorf("pre-deploy command: %w", err)
	case code != 0:
		e.hookOutput(ctx, d, run.ID)
		return fmt.Errorf("pre-deploy command exited %d", code)
	}
	e.step(ctx, d, "Pre-deploy command finished (%s)", shortDuration(time.Since(started).Round(time.Second)))
	return nil
}

// hookOutput copies the end of a failed hook's output into the deployment's
// events, where the person reading "pre-deploy command exited 1" looks next.
func (e *Engine) hookOutput(ctx context.Context, d *store.Deployment, runID int64) {
	const maxBytes = 4096
	run, err := e.store.GetJobRun(context.WithoutCancel(ctx), runID)
	if err != nil || run.Output == "" {
		return
	}
	lines := strings.Split(run.Output, "\n")
	if len(lines) > hookOutputLines {
		lines = lines[len(lines)-hookOutputLines:]
	}
	msg := "Last output of the pre-deploy command:\n" + strings.Join(lines, "\n")
	if len(msg) > maxBytes {
		msg = msg[:maxBytes] + "\n… (truncated)"
	}
	e.event(ctx, d, api.LevelError, api.EventLog, msg)
}

// RunCommand runs argv in a one-off container of the application's active
// deployment and returns the run as soon as its container has started; it
// finishes in the background, and callers poll it. Commands are independent
// of one another: two may run at the same time.
func (e *Engine) RunCommand(ctx context.Context, name string, argv []string) (store.JobRun, error) {
	if err := spec.ValidateCommand(argv); err != nil {
		return store.JobRun{}, &InvalidCommandError{Reason: err.Error()}
	}
	return e.startFromActive(ctx, name, api.RunKindManual, func(store.Deployment) (string, []string, time.Duration, error) {
		return commandJobName, argv, spec.DefaultJobTimeout, nil
	})
}

// RunJob starts a run of one of the application's scheduled jobs now, as the
// schedule would. Like the schedule, it refuses while an earlier run of the
// job is still going.
func (e *Engine) RunJob(ctx context.Context, name, job string) (store.JobRun, error) {
	return e.startFromActive(ctx, name, api.RunKindManual, func(d store.Deployment) (string, []string, time.Duration, error) {
		for _, j := range d.Spec.Jobs {
			if j.Name == job {
				return j.Name, j.Command, j.Timeout.Std(), nil
			}
		}
		return "", nil, 0, store.ErrNotFound
	})
}

// startFromActive resolves what to run and starts it under the application's
// lock — so that "the active deployment" is still the active deployment when
// the run is recorded — and lets go of the lock while the container runs.
func (e *Engine) startFromActive(ctx context.Context, name, kind string, resolve func(store.Deployment) (job string, command []string, timeout time.Duration, err error)) (store.JobRun, error) {
	if err := e.lock(ctx, name); err != nil {
		return store.JobRun{}, err
	}
	defer e.unlock(name)

	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return store.JobRun{}, err
	}
	if app.ActiveDeploymentID == nil {
		return store.JobRun{}, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return store.JobRun{}, err
	}
	if d.StaticDigest != "" {
		return store.JobRun{}, ErrStaticApplication
	}
	job, command, timeout, err := resolve(d)
	if err != nil {
		return store.JobRun{}, err
	}
	return e.startJob(ctx, d, job, kind, command, timeout)
}

// startJob records a run, launches its container, and collects it in the
// background. The caller holds the application's lock, which the run itself
// does not need: a job may run for an hour, and the application must stay
// deployable and stoppable meanwhile. The run counts as an operation, so Wait
// and Shutdown know about it.
func (e *Engine) startJob(ctx context.Context, d store.Deployment, job, kind string, command []string, timeout time.Duration) (store.JobRun, error) {
	// One run per job at a time; `shipwick run` commands are independent.
	exclusive := job != commandJobName
	if exclusive && !e.jobs.claim(d.Application, job) {
		return store.JobRun{}, ErrJobRunning
	}
	release := func() {
		if exclusive {
			e.jobs.release(d.Application, job)
		}
	}
	if !e.beginOp() {
		release()
		return store.JobRun{}, ErrShuttingDown
	}
	run, err := e.store.CreateJobRun(ctx, store.JobRun{
		ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: job, Kind: kind, Command: command,
	}, time.Now())
	if err == nil {
		var id string
		if id, err = e.launchJob(ctx, d, run); err == nil {
			e.log.Info("job started", "app", d.Application, "job", job, "run", run.ID, "kind", kind)
			go func() {
				defer e.opDone()
				defer release()
				code, err := e.collectJob(e.baseCtx, run, id, timeout)
				e.reportRun(d, run, code, err, timeout)
			}()
			return run, nil
		}
	}
	release()
	e.opDone()
	return store.JobRun{}, err
}

// beginOp counts an operation that takes no application lock of its own. It
// refuses once the engine is shutting down, like tryLock does.
func (e *Engine) beginOp() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.ops++
	return true
}

// reportRun tells the application's event feed how a run that went wrong
// ended. Runs that succeed record nothing: jobs run often, and the run itself
// is in the history.
func (e *Engine) reportRun(d store.Deployment, run store.JobRun, code int, err error, timeout time.Duration) {
	what := "Job " + run.Job
	if run.Job == commandJobName {
		what = "Command " + run.Command[0]
	}
	var msg string
	switch {
	case errors.Is(err, errJobTimeout):
		msg = fmt.Sprintf("%s timed out after %s", what, shortDuration(timeout))
	case err != nil && e.baseCtx.Err() != nil:
		// Shutting down: the run says "interrupted", and nobody reads the
		// events of an agent that is going away.
	case err != nil:
		msg = fmt.Sprintf("%s could not run: %v", what, err)
	case code != 0:
		msg = fmt.Sprintf("%s failed (exit %d)", what, code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if msg != "" {
		e.log.Warn(msg, "app", d.Application, "run", run.ID)
		if err := e.store.AddEvent(ctx, d.ApplicationID, nil, api.LevelWarn, api.EventJob, msg, time.Now()); err != nil {
			e.log.Warn("could not record event", "app", d.Application, "error", err)
		} else if err := e.store.PruneApplicationEvents(ctx, d.ApplicationID, appEventsKept); err != nil {
			e.log.Warn("could not prune events", "app", d.Application, "error", err)
		}
		e.notify(ctx, notify.Event{
			Kind:        notify.JobFailed,
			Application: d.Application,
			Version:     d.Version,
			Message:     fmt.Sprintf("%s: %s. Its output: shipwick jobs logs %s %s", d.Application, msg, d.Application, run.Job),
		})
	}
	if err := e.store.PruneJobRuns(ctx, d.ApplicationID, run.Job, jobRunsKept); err != nil {
		e.log.Warn("could not prune job runs", "app", d.Application, "job", run.Job, "error", err)
	}
	e.dropPrunedRunLogs(ctx, d.ApplicationID)
}

// Jobs describes the scheduled jobs of the application's active deployment:
// what they are, how their last run went, and when the next one is due.
func (e *Engine) Jobs(ctx context.Context, name string) ([]api.Job, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	if app.ActiveDeploymentID == nil {
		return nil, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return nil, err
	}
	if d.StaticDigest != "" {
		return nil, ErrStaticApplication
	}
	last, err := e.store.LastJobRuns(ctx, app.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]api.Job, 0, len(d.Spec.Jobs))
	for _, j := range d.Spec.Jobs {
		job := api.Job{Name: j.Name, Schedule: j.Schedule, Command: j.Command, Timeout: j.Timeout}
		if r, ok := last[j.Name]; ok {
			view := runView(r)
			job.LastRun = &view
		}
		// A stopped application runs no jobs; a next run would be a lie.
		if s, err := cron.Parse(j.Schedule); err == nil && app.DesiredState == api.DesiredRunning {
			next := s.Next(now)
			job.NextRunAt = &next
		}
		out = append(out, job)
	}
	return out, nil
}

// Runs lists the application's runs, newest first, without their output; job
// narrows them to one job (or to "pre-deploy" or "run").
func (e *Engine) Runs(ctx context.Context, name, job string, limit int) ([]api.Run, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	runs, err := e.store.ListJobRuns(ctx, app.ID, job, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, runView(r))
	}
	return out, nil
}

// JobRun returns one run of the application with its output.
func (e *Engine) JobRun(ctx context.Context, name string, id int64) (api.RunDetail, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.RunDetail{}, err
	}
	run, err := e.store.GetJobRun(ctx, id)
	if err != nil {
		return api.RunDetail{}, err
	}
	if run.ApplicationID != app.ID {
		return api.RunDetail{}, store.ErrNotFound
	}
	return api.RunDetail{Run: runView(run), Output: run.Output}, nil
}

func runView(r store.JobRun) api.Run {
	return api.Run{
		ID:           r.ID,
		Application:  r.Application,
		Job:          r.Job,
		Kind:         r.Kind,
		Command:      r.Command,
		Status:       r.Status,
		ExitCode:     r.ExitCode,
		DeploymentID: r.DeploymentID,
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
	}
}
