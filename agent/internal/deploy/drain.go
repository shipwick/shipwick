package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A replica that a rollout has replaced is out of rotation the moment its
// successor is in; what is left is to let it finish what it has in hand and
// remove it. That takes as long as its process takes to exit on SIGTERM — the
// whole grace period if it ignores the signal, which a process running as
// PID 1 without a handler does — and nobody needs to wait for it: the
// deployment has done what it was for. So the old replica drains in the
// background, outside the application's lock, and the deployment completes.
//
// What must not happen meanwhile is that something else mistakes the draining
// container for a leftover, or needs it gone and does not wait: the
// supervisor and a later deployment's sweep skip it (draining), and whatever
// needs it gone — the next replica of the same rollout, which would otherwise
// make it N+2 containers; a rollback, which creates a container of the same
// name; a recreate deployment, whose point is that nothing else runs; a
// delete — waits for it (awaitDrains).

// drain is one container on its way out.
type drain struct {
	app  string
	done chan struct{} // closed when the container is gone, or the agent gave up on it
}

// gracePeriod is how long a replica of the given configuration gets between
// SIGTERM and SIGKILL: its deploy.stop_timeout, or the agent's default.
func (e *Engine) gracePeriod(a spec.App) time.Duration {
	if t := a.Deploy.StopTimeout.Std(); t > 0 {
		return t
	}
	return e.opts.StopTimeout
}

// gracePeriodOf is gracePeriod for a container known only by its labels. The
// deployment it names may be gone, or belong to nothing the database knows.
func (e *Engine) gracePeriodOf(ctx context.Context, deploymentID int64) time.Duration {
	d, err := e.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return e.opts.StopTimeout
	}
	return e.gracePeriod(d.Spec)
}

// retireInBackground stops a container gracefully and removes it, without
// making the caller wait. on is the deployment that replaced it, whose
// application hears about a replica that had to be killed; nil for a container
// nobody is deploying over. It reports false when the agent is shutting down:
// the container stays, and the next start removes it as a leftover.
func (e *Engine) retireInBackground(c docker.Container, timeout time.Duration, on *store.Deployment) bool {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return false
	}
	if _, already := e.drains[c.ID]; already {
		e.mu.Unlock()
		return true
	}
	dr := &drain{app: c.App, done: make(chan struct{})}
	e.drains[c.ID] = dr
	// Counted like an operation, under the mutex that guards closed: Wait and
	// Shutdown know about it.
	e.ops++
	e.mu.Unlock()

	var notify *store.Deployment
	if on != nil {
		d := *on
		notify = &d
	}
	go func() {
		defer e.opDone()
		defer func() {
			e.mu.Lock()
			delete(e.drains, c.ID)
			e.mu.Unlock()
			close(dr.done)
		}()
		// Under the engine's context: an agent that is shutting down does not
		// sit out somebody's grace period. Docker finishes a stop it was asked
		// for whether or not the caller is still there.
		ctx, cancel := context.WithTimeout(e.baseCtx, timeout+cleanupTimeout)
		defer cancel()
		if err := e.rt.StopContainer(ctx, c.ID, timeout); err != nil && ctx.Err() == nil {
			e.log.Warn("graceful stop failed, forcing removal", "container", c.Name, "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if notify != nil {
			e.noteKilled(ctx, notify.ApplicationID, notify.Application, c, timeout, "replaced")
		}
		if err := e.removeContainer(ctx, c.ID); err != nil && ctx.Err() == nil {
			// Once it is no longer draining, the supervisor sees a leftover
			// and tries again.
			e.log.Warn("could not remove retired container", "container", c.Name, "error", err)
		}
	}()
	return true
}

// sigkilled is the exit code of a process ended by SIGKILL.
const sigkilled = 137

// noteKilled says so when a replica that was replaced or stopped used up its
// grace period and was killed: whatever it was serving at that moment was cut,
// and the application can do something about it. It goes to the application's
// own feed — the deployment that replaced the replica has completed by now,
// and its events are its story, which ended.
func (e *Engine) noteKilled(ctx context.Context, appID int64, app string, c docker.Container, timeout time.Duration, what string) {
	in, err := e.rt.InspectContainer(ctx, c.ID)
	if err != nil || in.Running || in.ExitCode != sigkilled || in.OOMKilled {
		return
	}
	msg := fmt.Sprintf("The %s container %s did not exit within %s of SIGTERM and was killed. %s",
		what, c.Name, shortDuration(timeout), e.killedAdvice(ctx, c.DeploymentID))
	e.log.Warn(msg, "app", app)
	if err := e.store.AddEvent(context.WithoutCancel(ctx), appID, nil, api.LevelWarn, api.EventApp, msg, time.Now()); err != nil {
		e.log.Warn("could not record event", "app", app, "error", err)
	}
}

// killedAdvice is what to do about a container that was killed, which depends
// on the configuration it ran with. Without an init process the likeliest
// cause is a process that runs as PID 1 and has no handler for the signal:
// the kernel delivers none to PID 1 that it did not ask for, and `init: true`
// is the whole remedy. With one, the signal arrived, and the process took
// longer than it was given.
func (e *Engine) killedAdvice(ctx context.Context, deploymentID int64) string {
	const longer = "to give it longer, set deploy.stop_timeout"
	if d, err := e.store.GetDeployment(ctx, deploymentID); err == nil && d.Spec.Init {
		return "It runs under an init process, so the signal reached it: " + longer
	}
	return "If its process has no handler for SIGTERM, set init: true and the signal ends it at once; to let it finish its requests, handle SIGTERM in the application; " + longer
}

// draining reports whether the container is being retired in the background.
func (e *Engine) draining(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.drains[id]
	return ok
}

// awaitDrains waits until none of the application's containers is draining.
// awaitAllDrains waits for every container that is being retired, whatever
// application it belongs to.
func (e *Engine) awaitAllDrains(ctx context.Context) error {
	for {
		var pending []chan struct{}
		e.mu.Lock()
		for _, dr := range e.drains {
			pending = append(pending, dr.done)
		}
		e.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		for _, done := range pending {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (e *Engine) awaitDrains(ctx context.Context, app string) error {
	for {
		var pending []chan struct{}
		e.mu.Lock()
		for _, dr := range e.drains {
			if dr.app == app {
				pending = append(pending, dr.done)
			}
		}
		e.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		for _, done := range pending {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
