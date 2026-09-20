package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// A rollout replaces the application's replicas one at a time:
//
//	for every replica i:
//	    start new i  →  wait until it is healthy  →  route to new i instead
//	    of old i  →  retire old i
//
// At most one extra container exists at any moment (2N would be the price of
// starting the whole new version next to the old one — a price that matters on
// the small servers Shipwick is for), and serving capacity never drops below N.
//
// The cost of retiring old replicas before the commit is that a failure
// half-way leaves the old version incomplete. It is then *rolled back*: the
// retired replicas are recreated from the previous deployment's stored spec
// and traffic returns to them. A failure before anything was retired — by far
// the common case, a bad image rarely survives its first health check — needs
// no rollback: the new containers are simply discarded.
type rollout struct {
	e *Engine
	d *store.Deployment

	prev    *store.Deployment     // the deployment being replaced; nil on a first deployment
	old     map[int]store.Replica // its live replicas, by index
	fresh   []store.Replica       // replicas of d created so far
	serving []routeMember         // who receives traffic right now
	retired bool                  // an old replica has been removed: failing now means rolling back
}

func (e *Engine) run(d store.Deployment) {
	ctx, cancel := context.WithTimeout(e.baseCtx, e.opts.DeployTimeout)
	defer cancel()

	r := &rollout{e: e, d: &d, old: map[int]store.Replica{}}
	err := r.execute(ctx)
	if err == nil {
		e.log.Info("deployment succeeded", "app", d.Application, "deployment", d.ID, "version", d.Version)
		return
	}

	switch {
	case e.baseCtx.Err() != nil:
		err = errors.New("agent shut down during deployment")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("deployment timed out after %s", shortDuration(e.opts.DeployTimeout))
	}
	r.abort(err)
}

func (r *rollout) execute(ctx context.Context) error {
	e, d := r.e, r.d

	if err := e.transition(ctx, d, api.StatusBuilding); err != nil {
		return err
	}
	if err := e.pullImage(ctx, d); err != nil {
		return err
	}

	if err := e.transition(ctx, d, api.StatusStarting); err != nil {
		return err
	}
	if err := e.rt.EnsureNetwork(ctx); err != nil {
		return err
	}
	if err := r.loadPrevious(ctx); err != nil {
		return err
	}

	batches := r.batches()
	for i, batch := range batches {
		created, err := e.ensureReplicas(ctx, *d, batch)
		r.fresh = append(r.fresh, created...)
		if err != nil {
			return err
		}
		if i == 0 {
			e.step(ctx, d, "Started %s", plural(len(created), "container"))
			if err := e.transition(ctx, d, api.StatusHealthChecking); err != nil {
				return err
			}
		}
		if err := e.awaitReady(ctx, d, created); err != nil {
			return err
		}
		if err := r.swap(ctx, created); err != nil {
			return err
		}
	}
	// A deployment with fewer replicas than its predecessor: the surplus goes last.
	if err := r.swap(ctx, nil); err != nil {
		return err
	}

	if err := e.transition(ctx, d, api.StatusHealthy); err != nil {
		return err
	}
	if !CanTransition(d.Status, api.StatusActive) {
		return fmt.Errorf("illegal state transition %s → %s", d.Status, api.StatusActive)
	}
	r.announceRouting(ctx)

	previous, err := e.store.ActivateDeployment(ctx, d.ID, time.Now())
	if err != nil {
		return err
	}
	d.Status = api.StatusActive
	// The database now says what the override said; routing is unchanged.
	e.clearRouteOverride(d.Application)
	e.event(ctx, d, api.LevelInfo, api.EventState, string(api.StatusActive))

	// From here on the deployment has succeeded; sweeping up is best effort
	// and must complete even if the agent is asked to shut down.
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	e.retireOthers(sweepCtx, d, previous)
	e.step(sweepCtx, d, "Deployment successful")
	return nil
}

// loadPrevious finds what is being replaced and takes over its routing: from
// here until the commit, the rollout — not the database — says who serves.
func (r *rollout) loadPrevious(ctx context.Context) error {
	e, d := r.e, r.d
	app, err := e.store.GetApplication(ctx, d.Application)
	if err != nil {
		return err
	}
	if app.ActiveDeploymentID == nil {
		return nil
	}
	prev, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return err
	}
	replicas, err := e.store.ListReplicas(ctx, prev.ID)
	if err != nil {
		return err
	}
	r.prev = &prev
	for _, rep := range replicas {
		r.old[rep.Index] = rep
		r.serving = append(r.serving, routeMember{replica: rep, port: prev.Spec.Port})
	}
	// A stopped application is being revived by this deployment; its old
	// replicas are not running and must not be "served" in the meantime.
	if app.DesiredState != api.DesiredRunning {
		r.serving = nil
	}
	e.routeVia(d.Application, routeOverride{domain: prev.Spec.Domain, members: r.serving, desired: r.desired()})
	return nil
}

// desired is the serving capacity the rollout maintains throughout.
func (r *rollout) desired() int {
	if r.prev == nil {
		return r.d.Spec.Replicas
	}
	return min(r.prev.Spec.Replicas, r.d.Spec.Replicas)
}

// batches decides which replicas start together. Replicas that replace an old
// one go one by one — that is what keeps the peak at N+1. Replicas with
// nothing to replace (a first deployment, or scaling up) have no such
// constraint and start together, so that N replicas do not cost N waits.
func (r *rollout) batches() [][]int {
	var batches [][]int
	var additional []int
	for i := 1; i <= r.d.Spec.Replicas; i++ {
		if _, replaces := r.old[i]; replaces {
			batches = append(batches, []int{i})
		} else {
			additional = append(additional, i)
		}
	}
	if len(additional) > 0 {
		batches = append(batches, additional)
	}
	return batches
}

// swap puts the freshly verified replicas into rotation in place of their
// predecessors, then retires those. Called with nil after the last batch, it
// retires whatever old replicas have no successor.
func (r *rollout) swap(ctx context.Context, ready []store.Replica) error {
	e, d := r.e, r.d

	var outgoing []store.Replica
	if ready == nil {
		for _, rep := range r.old {
			outgoing = append(outgoing, rep)
		}
		if len(outgoing) == 0 {
			return nil
		}
		sort.Slice(outgoing, func(i, j int) bool { return outgoing[i].Index < outgoing[j].Index })
	}
	for _, rep := range ready {
		if old, ok := r.old[rep.Index]; ok {
			outgoing = append(outgoing, old)
		}
	}

	leaving := map[string]bool{}
	for _, rep := range outgoing {
		leaving[rep.ContainerID] = true
	}
	serving := r.serving[:0:0]
	for _, m := range r.serving {
		if !leaving[m.replica.ContainerID] {
			serving = append(serving, m)
		}
	}
	for _, rep := range ready {
		serving = append(serving, routeMember{replica: rep, port: d.Spec.Port})
	}

	// Routing first, retiring second: an old replica must be out of rotation
	// before it gets its SIGTERM.
	r.serving = serving
	e.routeVia(d.Application, routeOverride{domain: d.Spec.Domain, members: serving, desired: r.desired()})
	if err := e.SyncProxy(ctx); err != nil {
		return fmt.Errorf("could not route %s to the new version: %w", d.Spec.Domain, err)
	}

	if len(outgoing) > 0 {
		r.retired = true
		// Uncancellable: a half-retired replica helps nobody, and the rollback
		// that a cancellation triggers expects "retired" to mean gone.
		retireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		for _, rep := range outgoing {
			if err := e.retireContainer(retireCtx, rep.ContainerID); err != nil {
				e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not remove old container %s: %v", rep.ContainerName, err))
			}
			delete(r.old, rep.Index)
		}
	}

	switch {
	case r.prev == nil:
		// A first deployment has nothing to narrate replica by replica.
	case ready == nil:
		e.step(ctx, d, "Retired %s of %s that %s no longer needs", plural(len(outgoing), "replica"), r.prev.Version, d.Version)
	default:
		for _, rep := range ready {
			if len(outgoing) > 0 {
				e.step(ctx, d, "Replica %d/%d is serving %s; its %s predecessor is retired", rep.Index, d.Spec.Replicas, d.Version, r.prev.Version)
			} else {
				e.step(ctx, d, "Replica %d/%d is serving %s", rep.Index, d.Spec.Replicas, d.Version)
			}
		}
	}
	return ctx.Err()
}

func (r *rollout) announceRouting(ctx context.Context) {
	e, d := r.e, r.d
	switch {
	case d.Spec.Domain == "":
	case e.opts.Proxy == nil:
		e.event(ctx, d, api.LevelWarn, api.EventStep,
			fmt.Sprintf("No reverse proxy is configured, so %s is not being served. Set SHIPWICK_CADDY_ADMIN on the agent", d.Spec.Domain))
	default:
		e.step(ctx, d, "Routed https://%s to %s", d.Spec.Domain, plural(len(r.serving), "replica"))
	}
}

// abort ends a rollout that cannot succeed. It runs on a fresh context, since
// the rollout's own is typically dead (cancelled, timed out).
func (r *rollout) abort(cause error) {
	e, d := r.e, r.d
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	e.log.Error("deployment failed", "app", d.Application, "deployment", d.ID, "error", cause)
	if err := e.transitionWithError(ctx, d, api.StatusFailed, cause.Error()); err != nil {
		e.log.Error("could not mark deployment as failed", "deployment", d.ID, "error", err)
	}

	if !r.retired || r.prev == nil || e.baseCtx.Err() != nil {
		// Nothing of the old version was lost — or the agent is going down
		// and must not start a restore it cannot finish; at the next start,
		// reconciliation completes the old version, which is still the
		// active one in the database.
		r.discard(ctx, r.fresh)
		return
	}
	r.rollBack(ctx, cause)
}

// discard drops the rollout's grip on routing and removes new replicas.
func (r *rollout) discard(ctx context.Context, replicas []store.Replica) {
	e := r.e
	e.clearRouteOverride(r.d.Application)
	if err := e.SyncProxy(ctx); err != nil {
		e.log.Error("could not restore routing after a failed deployment", "app", r.d.Application, "error", err)
	}
	for _, rep := range replicas {
		if err := e.removeContainer(ctx, rep.ContainerID); err != nil {
			e.log.Error("could not remove container of failed deployment", "container", rep.ContainerName, "error", err)
		}
	}
}

// rollBack completes the previous version again: FAILED → ROLLBACK →
// RESTORING → ROLLED_BACK. The new replicas that already serve keep serving
// until the restored ones are verified, so capacity does not dip twice.
func (r *rollout) rollBack(ctx context.Context, cause error) {
	e, d, prev := r.e, r.d, r.prev

	if err := e.transition(ctx, d, api.StatusRollback); err != nil {
		e.log.Error("could not start rollback", "deployment", d.ID, "error", err)
		r.discard(ctx, r.fresh)
		return
	}
	// First, the replicas that never made it into rotation: they are the
	// failure, and they hold the one slot of headroom the restore needs.
	inRotation := map[string]bool{}
	for _, m := range r.serving {
		inRotation[m.replica.ContainerID] = true
	}
	var stillServing []store.Replica
	for _, rep := range r.fresh {
		if inRotation[rep.ContainerID] {
			stillServing = append(stillServing, rep)
		} else if err := e.removeContainer(ctx, rep.ContainerID); err != nil {
			e.log.Error("could not remove failed replica", "container", rep.ContainerName, "error", err)
		}
	}

	var missing []int
	for i := 1; i <= prev.Spec.Replicas; i++ {
		if _, alive := r.old[i]; !alive {
			missing = append(missing, i)
		}
	}
	e.step(ctx, d, "Rolling back: restoring %s of %s", plural(len(missing), "replica"), prev.Version)
	if err := e.transition(ctx, d, api.StatusRestoring); err != nil {
		e.log.Error("could not enter RESTORING", "deployment", d.ID, "error", err)
	}

	restored, err := e.ensureReplicas(ctx, *prev, missing)
	if err == nil {
		// Verified against the previous version's spec (its health check, its
		// port), but narrated in this deployment's event log: the restore is
		// part of this deployment's story, and the previous one is immutable.
		asPrev := *prev
		asPrev.ID = d.ID
		err = e.awaitReady(ctx, &asPrev, restored)
	}
	if err != nil {
		// The old version could not be completed either. Keep what serves,
		// drop the rollout's claim on routing, and let reconciliation keep
		// trying: the previous deployment is still the active one.
		msg := fmt.Sprintf("%v; the rollback to %s then failed too: %v", cause, prev.Version, err)
		if terr := e.transitionWithError(ctx, d, api.StatusFailed, msg); terr != nil {
			e.log.Error("could not mark rollback as failed", "deployment", d.ID, "error", terr)
		}
		for _, rep := range restored {
			e.removeContainer(ctx, rep.ContainerID)
		}
		r.discard(ctx, stillServing)
		return
	}

	// The previous deployment is whole again, and the database never stopped
	// calling it active: dropping the override routes to exactly its replicas.
	r.discard(ctx, stillServing)
	if err := e.transition(ctx, d, api.StatusRolledBack); err != nil {
		e.log.Error("could not mark deployment as rolled back", "deployment", d.ID, "error", err)
	}
	e.step(ctx, d, "Rolled back: %s is running %s again", d.Application, prev.Version)
}

// ensureReplicas creates and starts containers for the given replica indexes
// of deployment d. It is the single way replicas come to exist: rollouts,
// rollbacks and the supervisor's reconciliation all go through it. The
// replicas created so far are returned even on error, for the caller to
// clean up.
func (e *Engine) ensureReplicas(ctx context.Context, d store.Deployment, indexes []int) ([]store.Replica, error) {
	var created []store.Replica
	for _, i := range indexes {
		spec := docker.ContainerSpec{
			App:          d.Application,
			DeploymentID: d.ID,
			Sequence:     d.Sequence,
			Replica:      i,
			Image:        d.Spec.Image,
			Env:          d.Spec.Env,
			NanoCPUs:     d.Spec.Resources.NanoCPUs(),
			MemoryBytes:  d.Spec.Resources.MemoryBytes,
		}
		id, name, err := e.rt.CreateContainer(ctx, spec)
		if err != nil {
			// The image may have been pruned since it was deployed.
			if exists, ierr := e.rt.ImageExists(ctx, d.Spec.Image); ierr == nil && !exists {
				if perr := e.rt.PullImage(ctx, d.Spec.Image); perr != nil {
					return created, fmt.Errorf("replica %d: image %s is gone and could not be pulled again: %w", i, d.Spec.Image, perr)
				}
				id, name, err = e.rt.CreateContainer(ctx, spec)
			}
			if err != nil {
				return created, fmt.Errorf("replica %d: %w", i, err)
			}
		}
		// Record immediately: cleanup after a failure relies on this row.
		rep := store.Replica{DeploymentID: d.ID, Index: i, ContainerID: id, ContainerName: name}
		if err := e.store.AddReplica(ctx, rep, time.Now()); err != nil {
			e.removeContainer(context.WithoutCancel(ctx), id)
			return created, err
		}
		created = append(created, rep)
	}
	for _, rep := range created {
		if err := e.rt.StartContainer(ctx, rep.ContainerID); err != nil {
			return created, fmt.Errorf("replica %d: %w", rep.Index, err)
		}
	}
	return created, nil
}

// awaitReady waits until replicas may receive traffic: healthy if d defines a
// health check, otherwise still running after the stabilization window.
func (e *Engine) awaitReady(ctx context.Context, d *store.Deployment, replicas []store.Replica) error {
	if len(replicas) == 0 {
		return nil
	}
	if d.Spec.Health != nil {
		if err := e.awaitHealthy(ctx, d, replicas); err != nil {
			return err
		}
		e.step(ctx, d, "%s passed health checks", describeReplicas(replicas))
		return nil
	}
	if err := e.awaitStable(ctx, d, replicas); err != nil {
		return err
	}
	e.step(ctx, d, "%s running and stable", describeReplicas(replicas))
	return nil
}

// describeReplicas names a single replica by its number, and counts several.
func describeReplicas(replicas []store.Replica) string {
	if len(replicas) == 1 {
		return fmt.Sprintf("Replica %d", replicas[0].Index)
	}
	return plural(len(replicas), "replica")
}
