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
		grace := e.gracePeriodOf(ctx, *app.ActiveDeploymentID)
		errs := parallel(replicas, func(r store.Replica) error {
			return e.rt.StopContainer(ctx, r.ContainerID, grace)
		})
		for i, err := range errs {
			if err != nil {
				return fmt.Errorf("replica %d: %w", replicas[i].Index, err)
			}
		}
		for _, r := range replicas {
			c := docker.Container{ID: r.ContainerID, Name: r.ContainerName, App: app.Name, DeploymentID: r.DeploymentID, Replica: r.Index}
			e.noteKilled(ctx, app.ID, app.Name, c, grace, "stopped")
			e.archiveInBackground(r.ContainerID, logEnd{reason: api.LogReasonStopped})
		}
		e.appEvent(ctx, app, byActor(ctx, "Application stopped"))
		return nil
	})
}

// Start starts the replicas of a stopped application.
func (e *Engine) Start(ctx context.Context, name string) error {
	return e.withActive(ctx, name, func(app store.Application, replicas []store.Replica) error {
		for _, r := range replicas {
			err := e.startNameless(ctx, r.ContainerID)
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
		e.appEvent(ctx, app, byActor(ctx, "Application started"))
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
	listed, err := e.rt.ListContainers(ctx, name)
	if err != nil {
		return err
	}
	// A replica that the last deployment replaced may still be on its way
	// out: it is waited for, not retired a second time.
	var containers []docker.Container
	for _, c := range listed {
		if !e.draining(c.ID) {
			containers = append(containers, c)
		}
	}
	for i, err := range e.retireAll(ctx, containers) {
		if err != nil {
			return fmt.Errorf("remove container %s: %w", containers[i].Name, err)
		}
	}
	if err := e.awaitDrains(ctx, name); err != nil {
		return err
	}
	// Its images go too, unless another application keeps them. Listed
	// before the records are deleted; the volumes stay, on purpose.
	images, _ := e.pruneCandidates(ctx, name, true)
	e.removeStaticFiles(ctx, name)
	if err := e.store.DeleteApplication(ctx, app.ID); err != nil {
		return err
	}
	e.traffic.forget(app.ID)
	e.purgeLogs(app.ID)
	e.syncProxyBestEffort(ctx, name)
	removed := e.pruneImages(ctx, images)
	// Its history goes with it, so the log is the only record of who did this.
	e.log.Info("application deleted", "app", name, "by", actorFrom(ctx), "containers", len(listed), "images", removed)
	return nil
}

// Recover reconciles state left behind by a crash or restart of the agent.
// Call it once at startup, before serving requests.
//
// Deployments found mid-flight are resumed (see resume.go): each takes its
// application's lock here, before anything else can, and goes on in the
// background from the state it was left in. One that cannot be resumed is
// marked FAILED. Containers that belong to a known application but neither to
// its active deployment nor to one that resumes are leftovers and are
// removed, in the background as well: starting must not wait for anybody's
// grace period. Containers of applications the database does not know are
// never touched — if the database is ever lost, the agent must not tear down
// what is running.
func (e *Engine) Recover(ctx context.Context) error {
	interrupted, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Statuses: InFlightStatuses()})
	if err != nil {
		return err
	}
	// The jobs first: whoever was waiting on them is gone, and so are their
	// containers below. A deployment whose hook was among them finds it
	// marked, and does not run it again.
	if n, err := e.store.MarkJobRunsInterrupted(ctx, time.Now()); err != nil {
		return err
	} else if n > 0 {
		e.log.Warn("found interrupted job runs", "runs", n)
	}

	inFlight := map[string]int{}
	for _, d := range interrupted {
		inFlight[d.Application]++
	}
	var resumed []*rollout
	resuming := map[int64]bool{}
	for _, d := range interrupted {
		e.log.Warn("found interrupted deployment", "app", d.Application, "deployment", d.ID, "status", d.Status)
		ok, why := resumable(d, inFlight[d.Application])
		if ok {
			stale, err := e.superseded(ctx, d)
			if err != nil {
				return err
			}
			if stale {
				ok, why = false, "its end was never recorded, and the application has been deployed again since"
			}
		}
		if !ok {
			if err := e.failInterrupted(ctx, &d, why); err != nil {
				return err
			}
			continue
		}
		// Nothing else runs yet, so the lock is free; it is held from here
		// until the deployment has ended, as if it had never been let go.
		if wait, err := e.tryLock(d.Application, false); wait != nil || err != nil {
			return fmt.Errorf("resume deployment %d of %s: %w", d.ID, d.Application, ErrBusy)
		}
		r := e.newRollout(d)
		r.resumed = true
		// What an import deployed stopped stays stopped when it is resumed.
		if r.dormant, err = e.store.DeploymentDormant(ctx, d.ID); err != nil {
			return err
		}
		if err := r.prepare(ctx); err != nil {
			e.clearRouteOverride(d.Application)
			e.unlock(d.Application)
			if IsRuntimeUnavailable(err) {
				// Nothing was learned about the deployment: it stays as it
				// is, for the start that finds Docker answering.
				return fmt.Errorf("resume deployment %d of %s: %w", d.ID, d.Application, err)
			}
			if err := e.failInterrupted(ctx, r.d, err.Error()); err != nil {
				return err
			}
			continue
		}
		resumed = append(resumed, r)
		resuming[d.ID] = true
	}

	leftovers, err := e.recoverLeftovers(ctx, resuming)
	if err != nil {
		return err
	}
	for _, c := range leftovers {
		e.retireInBackground(c, e.gracePeriodOf(ctx, c.DeploymentID), nil)
	}
	// Covers the deployments failed above and any that were settled — ACTIVE,
	// or FAILED — but interrupted while cleaning up; reconciliation finishes
	// that. The resumed ones complete when they are done.
	ids := make([]int64, 0, len(resumed))
	for _, r := range resumed {
		ids = append(ids, r.d.ID)
	}
	if err := e.store.CompleteDeploymentsExcept(ctx, ids, time.Now()); err != nil {
		return err
	}
	for _, r := range resumed {
		e.log.Info("resuming deployment", "app", r.d.Application, "deployment", r.d.ID, "status", r.d.Status)
		e.launch(r)
	}
	return e.recoverTransfers(ctx)
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
		o := origin{spec: active.Spec, kind: api.KindRedeploy, sourceID: &active.ID, static: staticOf(active)}
		if image != "" {
			if active.Spec.Static != nil {
				return origin{}, &InvalidImageError{Reason: "a static application has no image; to serve other files, deploy the folder again"}
			}
			// The same rule deploy.yaml validation applies: next to build,
			// only an image the server was sent.
			if active.Spec.Build != nil && !spec.IsLocalImage(image) {
				return origin{}, &InvalidImageError{Reason: "this application is built by shipwick deploy; run it from the project to deploy a new image, or remove build from deploy.yaml"}
			}
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
		return origin{spec: target.Spec, kind: api.KindRollback, sourceID: &target.ID, static: staticOf(target)}, nil
	})
}
