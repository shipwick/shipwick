package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Stop stops every replica of the application's active deployment and records
// that the application is meant to stay stopped.
func (e *Engine) Stop(ctx context.Context, name string) error {
	return e.withActive(ctx, name, func(app store.Application, replicas []store.Replica) error {
		// Recorded first, so the supervisor never sees "should be running,
		// but is not" and resurrects what is being stopped.
		if err := e.store.SetDesiredState(ctx, app.ID, api.DesiredStopped, time.Now()); err != nil {
			return err
		}
		// Take the application out of rotation before its processes get
		// SIGTERM: visitors see a clean 503, not connections dying mid-request.
		e.syncProxyBestEffort(ctx, app.Name)
		errs := parallel(replicas, func(r store.Replica) error {
			return e.rt.StopContainer(ctx, r.ContainerID, e.opts.StopTimeout)
		})
		for i, err := range errs {
			if err != nil {
				return fmt.Errorf("replica %d: %w", replicas[i].Index, err)
			}
		}
		e.appEvent(ctx, app, "Application stopped")
		return nil
	})
}

// Start starts the replicas of a stopped application.
func (e *Engine) Start(ctx context.Context, name string) error {
	return e.withActive(ctx, name, func(app store.Application, replicas []store.Replica) error {
		for _, r := range replicas {
			err := e.rt.StartContainer(ctx, r.ContainerID)
			if errors.Is(err, docker.ErrNotFound) {
				return fmt.Errorf("replica %d: container %s no longer exists; deploy the application again", r.Index, r.ContainerName)
			} else if err != nil {
				return fmt.Errorf("replica %d: %w", r.Index, err)
			}
		}
		// An explicit start is a clean slate: whatever restart history made
		// the supervisor back off no longer applies. Replicas with a health
		// check are "starting": they get traffic once they pass it.
		d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
		if err != nil {
			return err
		}
		for _, r := range replicas {
			e.sup.reset(r.ContainerID, d.Spec.Health != nil)
		}
		if err := e.store.SetDesiredState(ctx, app.ID, api.DesiredRunning, time.Now()); err != nil {
			return err
		}
		e.syncProxyBestEffort(ctx, app.Name)
		e.appEvent(ctx, app, "Application started")
		return nil
	})
}

// withActive runs fn under the application lock with the replicas of its
// active deployment.
func (e *Engine) withActive(ctx context.Context, name string, fn func(store.Application, []store.Replica) error) error {
	if err := e.lock(ctx, name); err != nil {
		return err
	}
	defer e.unlock(name)

	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return err
	}
	if app.ActiveDeploymentID == nil {
		return ErrNotDeployed
	}
	replicas, err := e.store.ListReplicas(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return err
	}
	return fn(app, replicas)
}

// Delete removes every container of the application, then the application
// itself together with its deployment history.
func (e *Engine) Delete(ctx context.Context, name string) error {
	if err := e.lock(ctx, name); err != nil {
		return err
	}
	defer e.unlock(name)

	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return err
	}
	containers, err := e.rt.ListContainers(ctx, name)
	if err != nil {
		return err
	}
	for i, err := range e.retireAll(ctx, containers) {
		if err != nil {
			return fmt.Errorf("remove container %s: %w", containers[i].Name, err)
		}
	}
	if err := e.store.DeleteApplication(ctx, app.ID); err != nil {
		return err
	}
	e.syncProxyBestEffort(ctx, name)
	e.log.Info("application deleted", "app", name, "containers", len(containers))
	return nil
}

// Recover reconciles state left behind by a crash or restart of the agent.
// Call it once at startup, before serving requests.
//
// Deployments found mid-flight can no longer complete: they are marked FAILED.
// Containers that belong to a known application but not to its active
// deployment are leftovers and are removed. Containers of applications the
// database does not know are never touched — if the database is ever lost,
// the agent must not tear down what is running.
func (e *Engine) Recover(ctx context.Context) error {
	interrupted, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Statuses: InFlightStatuses()})
	if err != nil {
		return err
	}
	for i := range interrupted {
		d := &interrupted[i]
		e.log.Warn("found interrupted deployment", "app", d.Application, "deployment", d.ID, "status", d.Status)
		if err := e.transitionWithError(ctx, d, api.StatusFailed, "agent restarted during deployment"); err != nil {
			return err
		}
	}

	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return err
	}
	active := make(map[string]int64, len(apps)) // 0 = known app without an active deployment
	for _, app := range apps {
		active[app.Name] = 0
		if app.ActiveDeploymentID != nil {
			active[app.Name] = *app.ActiveDeploymentID
		}
	}

	containers, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return err
	}
	var leftovers []docker.Container
	for _, c := range containers {
		if activeID, known := active[c.App]; known && c.DeploymentID != activeID {
			e.log.Warn("removing leftover container", "container", c.Name, "app", c.App, "deployment", c.DeploymentID)
			leftovers = append(leftovers, c)
		}
	}
	for i, err := range e.retireAll(ctx, leftovers) {
		if err != nil {
			e.log.Error("could not remove leftover container", "container", leftovers[i].Name, "error", err)
		}
	}
	// Covers both the deployments failed above and any that were ACTIVE but
	// interrupted while retiring their predecessor.
	return e.store.CompleteAllDeployments(ctx, time.Now())
}

func (e *Engine) appEvent(ctx context.Context, app store.Application, message string) {
	e.log.Info(message, "app", app.Name)
	if err := e.store.AddEvent(context.WithoutCancel(ctx), app.ID, nil, api.LevelInfo, api.EventApp, message, time.Now()); err != nil {
		e.log.Warn("could not record event", "app", app.Name, "error", err)
	}
}

// syncProxyBestEffort updates routing after an operation that has already
// succeeded. A proxy hiccup must not turn that success into an error; the
// supervisor syncs again within a second anyway.
func (e *Engine) syncProxyBestEffort(ctx context.Context, app string) {
	if err := e.SyncProxy(context.WithoutCancel(ctx)); err != nil {
		e.log.Warn("could not update the proxy; the supervisor will retry", "app", app, "error", err)
	}
}

// ErrNoRollbackTarget means there is no earlier successful deployment to go
// back to.
var ErrNoRollbackTarget = errors.New("no earlier successful deployment to roll back to")

// InvalidImageError is returned by Redeploy for a malformed image reference.
type InvalidImageError struct{ Reason string }

func (e *InvalidImageError) Error() string { return e.Reason }

// Redeploy deploys the active configuration again, optionally with another
// image. It exists because clients cannot do this themselves: the API only
// ever hands out configurations with their env values masked, so the real
// values never leave the server.
func (e *Engine) Redeploy(ctx context.Context, name, image string) (store.Deployment, error) {
	if image != "" {
		if err := spec.ValidateImage(image); err != nil {
			return store.Deployment{}, &InvalidImageError{Reason: err.Error()}
		}
	}
	return e.start(ctx, name, func(ctx context.Context) (origin, error) {
		app, err := e.store.GetApplication(ctx, name)
		if err != nil {
			return origin{}, err
		}
		if app.ActiveDeploymentID == nil {
			return origin{}, ErrNotDeployed
		}
		active, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
		if err != nil {
			return origin{}, err
		}
		o := origin{spec: active.Spec, kind: api.KindRedeploy, sourceID: &active.ID}
		if image != "" {
			o.spec.Image = image
		}
		return o, nil
	})
}

// Rollback deploys the configuration of an earlier successful deployment: the
// given one, or with targetID == 0 the most recent one before the active.
//
// A rollback is not a special mechanism. It is a deployment whose spec comes
// from the history instead of from a file, and it goes through the same
// engine: rolling replacement, health checks, and — should the old version
// fail to come up today — automatic rollback of the rollback.
func (e *Engine) Rollback(ctx context.Context, name string, targetID int64) (store.Deployment, error) {
	return e.start(ctx, name, func(ctx context.Context) (origin, error) {
		app, err := e.store.GetApplication(ctx, name)
		if err != nil {
			return origin{}, err
		}
		if app.ActiveDeploymentID == nil {
			return origin{}, ErrNotDeployed
		}

		var target store.Deployment
		if targetID != 0 {
			target, err = e.store.GetDeployment(ctx, targetID)
			if err != nil {
				return origin{}, err
			}
			// Only versions that once ran successfully are fair targets: a
			// FAILED deployment's spec is, by the evidence, not one to return to.
			if target.ApplicationID != app.ID || target.Status != api.StatusSuperseded {
				return origin{}, ErrNoRollbackTarget
			}
		} else {
			previous, err := e.store.ListDeployments(ctx, store.DeploymentFilter{
				Application: name,
				Statuses:    []api.DeploymentStatus{api.StatusSuperseded},
				Limit:       1,
			})
			if err != nil {
				return origin{}, err
			}
			if len(previous) == 0 {
				return origin{}, ErrNoRollbackTarget
			}
			target = previous[0]
		}
		return origin{spec: target.Spec, kind: api.KindRollback, sourceID: &target.ID}, nil
	})
}
