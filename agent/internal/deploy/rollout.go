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
	"github.com/shipwick/shipwick/pkg/spec"
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

	// recreate: the old version was stopped before the new one started (see
	// spec.StrategyRecreate). stopped are its replicas, kept for a rollback
	// until the new version is in service.
	recreate bool
	stopped  []store.Replica

	// staticPart is the directory a static rollout is extracting into, until
	// it is checked and renamed; removed if the rollout fails (see static.go).
	staticPart string

	// resumed: the deployment was begun by an agent that has since stopped,
	// and is taken from the state it was left in (see resume.go). surveyed
	// says that what exists of it has been looked at: adopted are the replicas
	// of d found alive, by index, and inService those of them that were in
	// rotation already.
	resumed   bool
	surveyed  bool
	adopted   map[int]store.Replica
	inService map[int]bool
	// unverified are replicas of the previous version that a resumed rollback
	// finds alive without knowing whether they were ever checked.
	unverified []store.Replica

	// dormant: the containers are created and nothing is started (see
	// executeDormant). A standby deployment is dormant by its kind, which is
	// in its record and so survives a restart of the agent.
	dormant bool
}

// progress orders the statuses a deployment passes through, on its way up
// and, after FAILED, on its way back.
var progress = map[api.DeploymentStatus]int{
	api.StatusPending:        0,
	api.StatusBuilding:       1,
	api.StatusStarting:       2,
	api.StatusHealthChecking: 3,
	api.StatusHealthy:        4,
	api.StatusFailed:         5,
	api.StatusRollback:       6,
	api.StatusRestoring:      7,
}

// reach moves the deployment on to the given status, unless it is there
// already or past it: a resumed deployment starts wherever it was left.
func (r *rollout) reach(ctx context.Context, to api.DeploymentStatus) error {
	if progress[r.d.Status] >= progress[to] {
		return nil
	}
	return r.e.transition(ctx, r.d, to)
}

// bring makes the given replicas of d run: those a resumed rollout found
// alive are adopted, started if they were not running, and the others come to
// exist the one way replicas do. It returns them in order, and how many it
// created.
func (r *rollout) bring(ctx context.Context, indexes []int) (replicas []store.Replica, created int, err error) {
	e, d := r.e, r.d
	var missing []int
	for _, i := range indexes {
		rep, alive := r.adopted[i]
		if !alive {
			missing = append(missing, i)
			continue
		}
		c, err := e.rt.InspectContainer(ctx, rep.ContainerID)
		if err != nil {
			return nil, 0, fmt.Errorf("replica %d: %w", i, err)
		}
		if !c.Running {
			if err := e.rt.StartContainer(ctx, rep.ContainerID); err != nil {
				return nil, 0, fmt.Errorf("replica %d: %w", i, err)
			}
		}
		replicas = append(replicas, rep)
	}
	fresh, err := e.ensureReplicas(ctx, *d, missing)
	r.fresh = append(r.fresh, fresh...)
	if err != nil {
		return nil, 0, err
	}
	replicas = append(replicas, fresh...)
	sort.Slice(replicas, func(i, j int) bool { return replicas[i].Index < replicas[j].Index })
	return replicas, len(fresh), nil
}

// sortedReplicas lists replicas by index.
func sortedReplicas(byIndex map[int]store.Replica) []store.Replica {
	out := make([]store.Replica, 0, len(byIndex))
	for _, rep := range byIndex {
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func (e *Engine) newRollout(d store.Deployment) *rollout {
	return &rollout{e: e, d: &d, old: map[int]store.Replica{}}
}

// run carries a deployment to its end: ACTIVE, or FAILED and whatever the
// failure calls for. It reports true when the agent shut down under it
// instead. The deployment is then left exactly as it is — its record in
// flight, its containers in place — for the agent that starts next to resume.
func (e *Engine) run(r *rollout) (interrupted bool) {
	d := r.d
	if r.resumed && (d.Status == api.StatusRollback || d.Status == api.StatusRestoring) {
		r.resumeRollback()
		e.removeFailedImage(d)
		e.notifyAborted(r)
		return false
	}

	timeout := e.opts.DeployTimeout
	if d.Spec.PreDeploy != nil {
		// The hook has its own budget, on top of the deployment's.
		timeout += d.Spec.PreDeploy.Timeout.Std()
	}
	ctx, cancel := context.WithTimeout(e.baseCtx, timeout)
	defer cancel()

	err := r.execute(ctx)
	if err == nil {
		e.log.Info("deployment succeeded", "app", d.Application, "deployment", d.ID, "version", d.Version)
		return false
	}

	switch {
	case e.baseCtx.Err() != nil:
		// Not a failure of the deployment: an upgrade of Shipwick restarts the
		// agent, and whatever was being deployed at that moment goes on after it.
		e.log.Info("deployment interrupted by the agent shutting down; it resumes at the next start",
			"app", d.Application, "deployment", d.ID, "status", d.Status)
		return true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("deployment timed out after %s", shortDuration(timeout))
	}
	r.abort(err)
	e.removeFailedImage(d)
	e.notifyAborted(r)
	return false
}

func (r *rollout) execute(ctx context.Context) error {
	e, d := r.e, r.d
	if r.resumed {
		e.step(ctx, d, "Resumed after the agent restarted")
	}
	if r.dormant || d.Kind == api.KindStandby {
		return r.executeDormant(ctx)
	}
	if d.Spec.Static != nil {
		return r.executeStatic(ctx)
	}

	if err := r.reach(ctx, api.StatusBuilding); err != nil {
		return err
	}
	if d.Status == api.StatusBuilding {
		if err := e.pullImage(ctx, d); err != nil {
			return err
		}
		if d.Spec.PreDeploy != nil {
			if err := r.runHookOnce(ctx); err != nil {
				return err
			}
		}
	}

	if err := r.reach(ctx, api.StatusStarting); err != nil {
		return err
	}
	if err := e.rt.EnsureNetwork(ctx); err != nil {
		return err
	}
	if !r.surveyed {
		if err := r.loadPrevious(ctx); err != nil {
			return err
		}
	}
	if d.Spec.Deploy.Strategy == spec.StrategyRecreate && r.prev != nil {
		switch {
		case len(r.inService) > 0 || (r.resumed && len(r.old) == 0 && r.retired):
			// The switch had happened when the agent stopped: the old version
			// is down, and stopping it "again" would only take the new one out
			// of the proxy for a moment.
			r.recreate = true
			r.stopped = sortedReplicas(r.old)
		case len(r.old) > 0:
			if err := r.stopPrevious(ctx); err != nil {
				return err
			}
		}
	}
	if len(r.inService) > 0 {
		// Back to where the rollout was: these serve, and what they replaced
		// goes, if it has not gone already.
		if err := r.swap(ctx, r.adoptedInService()); err != nil {
			return err
		}
	}

	batches := r.batches()
	for i, batch := range batches {
		// One extra container at a time: the replica replaced a moment ago —
		// by this rollout, or by the deployment before it — is gone before
		// the next one starts.
		waiting := time.Now()
		if err := e.awaitDrains(ctx, d.Application); err != nil {
			return err
		}
		if waited := time.Since(waiting); waited >= time.Second {
			// Said because it is where the time went, and stop_timeout is
			// what decides it.
			e.step(ctx, d, "Waited %s for a replaced container to stop", shortDuration(waited.Round(time.Second)))
		}
		started, created, err := r.bring(ctx, batch)
		if err != nil {
			return err
		}
		if i == 0 {
			if created > 0 {
				e.step(ctx, d, "Started %s", plural(created, "container"))
			}
			if err := r.reach(ctx, api.StatusHealthChecking); err != nil {
				return err
			}
		}
		if err := e.awaitReady(ctx, d, started); err != nil {
			return err
		}
		if err := r.swap(ctx, started); err != nil {
			return err
		}
	}
	// A deployment with fewer replicas than its predecessor: the surplus goes last.
	if err := r.swap(ctx, nil); err != nil {
		return err
	}

	// Reached here as well by a resumed deployment that had nothing left to
	// start: the status then names the last thing it knows to be done.
	if err := r.reach(ctx, api.StatusHealthChecking); err != nil {
		return err
	}
	if err := r.reach(ctx, api.StatusHealthy); err != nil {
		return err
	}
	if !CanTransition(d.Status, api.StatusActive) {
		return fmt.Errorf("illegal state transition %s → %s", d.Status, api.StatusActive)
	}
	r.announceRouting(ctx, plural(len(r.serving), "replica"))

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
	if candidates, err := e.pruneCandidates(sweepCtx, d.Application, false); err == nil {
		if n := e.pruneImages(sweepCtx, candidates); n > 0 {
			e.step(sweepCtx, d, "Removed %s of older versions", plural(n, "image"))
		}
	}
	e.retireStaticLeftovers(sweepCtx, d)
	e.step(sweepCtx, d, "Deployment successful")
	e.notifySucceeded(sweepCtx, d, previous)
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
	override := routeOverride{hosts: hostnamesOf(prev.Spec), members: r.serving, desired: r.desired()}
	if prev.StaticDigest != "" && app.DesiredState == api.DesiredRunning {
		// A folder is becoming a container application: its files keep
		// serving until the first replica is ready.
		override.staticRoot = staticDir(prev.Application, prev.StaticDigest)
	}
	e.routeVia(d.Application, override)
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
	if r.recreate {
		// Nothing runs that the newcomers could disturb: all at once.
		all := make([]int, 0, r.d.Spec.Replicas)
		for i := 1; i <= r.d.Spec.Replicas; i++ {
			if !r.inService[i] {
				all = append(all, i)
			}
		}
		if len(all) == 0 {
			return nil
		}
		return [][]int{all}
	}
	var batches [][]int
	var additional []int
	for i := 1; i <= r.d.Spec.Replicas; i++ {
		if r.inService[i] {
			continue // a resumed rollout had this one serving already
		}
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

	// Routing first, retiring second. The newcomers get the application's
	// names; the outgoing replicas keep theirs until they stop, when Docker's
	// DNS drops them by itself. Taking the names away first would mean taking
	// them off the network, and cut the requests they are serving.
	r.serving = serving
	e.routeVia(d.Application, routeOverride{hosts: hostnamesOf(d.Spec), members: serving, desired: r.desired()})
	if err := e.SyncProxy(ctx); err != nil {
		what := d.Application
		if d.Spec.Domain != "" {
			what = d.Spec.Domain
		}
		return fmt.Errorf("could not route %s to the new version: %w", what, err)
	}
	if len(ready) > 0 && len(outgoing) > 0 && !r.recreate {
		// Let the proxy's next lookup find the newcomers before the replicas
		// it knows stop answering. Whenever there are newcomers, not only when
		// this call named them: the supervisor's tick syncs routing too, and
		// may have been the one to do it a moment ago. (Under recreate the
		// outgoing replicas stopped answering long ago.)
		select {
		case <-time.After(e.opts.NameSettle):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if len(outgoing) > 0 {
		r.retired = true
		// Uncancellable: a half-retired replica helps nobody.
		retireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		grace := e.gracePeriod(r.prev.Spec)
		for _, rep := range outgoing {
			delete(r.old, rep.Index)
			if !r.recreate {
				// It is out of rotation; how long it takes to exit is nothing
				// the deployment has to wait for (see drain.go). Whoever needs
				// it gone — the next batch, a rollback — waits for the drain.
				// An agent that is shutting down leaves it where it is; the
				// rollout that resumes finds it and retires it then.
				c := docker.Container{ID: rep.ContainerID, Name: rep.ContainerName, App: d.Application, DeploymentID: rep.DeploymentID, Replica: rep.Index}
				e.retireInBackground(c, grace, d)
				continue
			}
			// Under recreate it stopped before the new version started, and
			// removing it takes no time.
			if err := e.retireContainer(retireCtx, rep.ContainerID, grace); err != nil {
				e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not remove old container %s: %v", rep.ContainerName, err))
			}
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

// announceRouting tells the user where their application is reachable — or
// why it is not yet: a hostname whose DNS does not point here is kept out of
// the proxy until it does (see dns.go), and "Routed" would be a lie. to says
// what the domain is routed to: "2 replicas", "the uploaded files".
func (r *rollout) announceRouting(ctx context.Context, to string) {
	e, d := r.e, r.d
	switch {
	case d.Spec.Domain == "":
	case e.opts.Proxy == nil:
		e.event(ctx, d, api.LevelWarn, api.EventStep,
			fmt.Sprintf("No reverse proxy is configured, so %s is not being served. Set SHIPWICK_CADDY_ADMIN on the agent", d.Spec.Domain))
	default:
		hosts := hostnamesOf(d.Spec)
		e.refreshHostnames(ctx, hosts.list())
		if ready, why := e.hostnameReady(hosts.domain); ready {
			e.step(ctx, d, "Routed https://%s%s to %s%s", d.Spec.Domain, d.Spec.Path, to, e.certificateNote(hosts.domain))
		} else {
			e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Routing https://%s%s is waiting for DNS: %s. It is served, and its certificate obtained, once the record points at this server", d.Spec.Domain, d.Spec.Path, why))
		}
		for _, h := range hosts.all() {
			if h.host == hosts.domain {
				continue // said above, one way or the other
			}
			if ready, why := e.hostnameReady(h.host); !ready {
				e.event(ctx, d, api.LevelWarn, api.EventStep,
					fmt.Sprintf("%s %s. It is served, and its certificate obtained, once its DNS points at this server", h.host, why))
			}
		}
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

	if e.baseCtx.Err() != nil || r.prev == nil || (!r.retired && !r.recreate) {
		// Nothing of the old version was lost — or the agent is going down
		// and must not start a restore it cannot finish; at the next start,
		// reconciliation completes the old version, which is still the
		// active one in the database.
		r.discard(ctx, r.fresh)
		return
	}
	if r.recreate {
		r.rollBackRecreate(ctx, cause)
		return
	}
	r.rollBack(ctx, cause)
}

// stopPrevious takes the running version out of service and stops it, for a
// deployment whose two versions cannot run side by side. Its containers are
// kept: they are the rollback.
func (r *rollout) stopPrevious(ctx context.Context) error {
	e, d, prev := r.e, r.d, r.prev
	r.recreate = true

	// Out of the proxy first, so that nobody is sent to a replica that is
	// about to stop.
	r.serving = nil
	e.routeVia(d.Application, routeOverride{hosts: hostnamesOf(prev.Spec), desired: r.desired()})
	if err := e.SyncProxy(ctx); err != nil {
		return fmt.Errorf("could not take %s out of service: %w", d.Application, err)
	}

	for _, rep := range sortedReplicas(r.old) {
		if err := e.rt.StopContainer(ctx, rep.ContainerID, e.gracePeriod(prev.Spec)); err != nil {
			return fmt.Errorf("stop replica %d of %s: %w", rep.Index, prev.Version, err)
		}
		r.stopped = append(r.stopped, rep)
	}
	e.step(ctx, d, "Stopped %s: %s cannot run next to it", prev.Version, d.Version)
	return nil
}

// rollBackRecreate undoes a recreate deployment: the new version is removed
// first — it must be gone before the old one touches the volumes again — and
// the old version is started again, from the containers it kept if it can.
func (r *rollout) rollBackRecreate(ctx context.Context, cause error) {
	e, d, prev := r.e, r.d, r.prev

	if err := r.reach(ctx, api.StatusRollback); err != nil {
		e.log.Error("could not start rollback", "deployment", d.ID, "error", err)
		r.discard(ctx, r.fresh)
		return
	}
	r.serving = nil
	e.routeVia(d.Application, routeOverride{hosts: hostnamesOf(prev.Spec), desired: r.desired()})
	e.syncProxyBestEffort(ctx, d.Application)
	for _, rep := range r.fresh {
		if err := e.retireContainer(ctx, rep.ContainerID, e.gracePeriod(d.Spec)); err != nil {
			e.log.Error("could not remove failed replica", "container", rep.ContainerName, "error", err)
		}
	}
	r.fresh = nil

	e.step(ctx, d, "Rolling back: starting %s again", prev.Version)
	if err := r.reach(ctx, api.StatusRestoring); err != nil {
		e.log.Error("could not enter RESTORING", "deployment", d.ID, "error", err)
	}

	// The containers the old version kept are started again. Those removed at
	// the switch are created anew: the volumes were not removed, and new
	// containers of the old version find their data there.
	var restored []store.Replica
	var err error
	for _, rep := range r.stopped {
		if _, kept := r.old[rep.Index]; !kept {
			continue
		}
		if err = e.startNameless(ctx, rep.ContainerID); err != nil {
			break
		}
		e.sup.reset(rep.ContainerID, prev.Spec.Health != nil)
		restored = append(restored, rep)
	}
	if err == nil && r.retired {
		var missing []int
		for i := 1; i <= prev.Spec.Replicas; i++ {
			if _, kept := r.old[i]; !kept {
				missing = append(missing, i)
			}
		}
		var created []store.Replica
		created, err = e.ensureReplicas(ctx, *prev, missing)
		restored = append(restored, created...)
	}
	if err == nil {
		asPrev := *prev
		asPrev.ID = d.ID
		err = e.awaitReady(ctx, &asPrev, restored)
	}
	if err != nil {
		msg := fmt.Sprintf("%v; the rollback to %s then failed too: %v", cause, prev.Version, err)
		if terr := e.transitionWithError(ctx, d, api.StatusFailed, msg); terr != nil {
			e.log.Error("could not mark rollback as failed", "deployment", d.ID, "error", terr)
		}
		// Whatever exists stays: the previous deployment is still the active
		// one, and the supervisor keeps trying to bring its replicas up.
		r.discard(ctx, nil)
		return
	}

	r.discard(ctx, nil)
	if err := e.transition(ctx, d, api.StatusRolledBack); err != nil {
		e.log.Error("could not mark deployment as rolled back", "deployment", d.ID, "error", err)
	}
	e.step(ctx, d, "Rolled back: %s is running %s again", d.Application, prev.Version)
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

	if err := r.reach(ctx, api.StatusRollback); err != nil {
		e.log.Error("could not start rollback", "deployment", d.ID, "error", err)
		r.discard(ctx, r.fresh)
		return
	}
	// The replicas this rollout replaced may still be on their way out, and
	// the restore creates containers of the very same names.
	if err := e.awaitDrains(ctx, d.Application); err != nil {
		e.log.Error("retired replicas are still stopping", "deployment", d.ID, "error", err)
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
	if len(missing) > 0 || !r.resumed {
		e.step(ctx, d, "Rolling back: restoring %s of %s", plural(len(missing), "replica"), prev.Version)
	}
	if err := r.reach(ctx, api.StatusRestoring); err != nil {
		e.log.Error("could not enter RESTORING", "deployment", d.ID, "error", err)
	}

	restored, err := e.ensureReplicas(ctx, *prev, missing)
	if err == nil {
		// Verified against the previous version's spec (its health check, its
		// port), but narrated in this deployment's event log: the restore is
		// part of this deployment's story, and the previous one is immutable.
		asPrev := *prev
		asPrev.ID = d.ID
		// A resumed rollback cannot tell which of the old version's replicas
		// it had restored and not yet verified: it verifies them all.
		err = e.awaitReady(ctx, &asPrev, append(r.unverified, restored...))
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
	created, err := e.createReplicas(ctx, d, indexes)
	if err != nil {
		return created, err
	}
	for _, rep := range created {
		if err := e.rt.StartContainer(ctx, rep.ContainerID); err != nil {
			return created, fmt.Errorf("replica %d: %w", rep.Index, err)
		}
	}
	return created, nil
}

// createReplicas is the creating half of ensureReplicas: containers that
// exist and are recorded, but do not run yet. A volume restore uses it alone,
// for a container whose volume must be filled before its process ever sees it.
func (e *Engine) createReplicas(ctx context.Context, d store.Deployment, indexes []int) ([]store.Replica, error) {
	var created []store.Replica
	for _, i := range indexes {
		cspec := docker.ContainerSpec{
			App:          d.Application,
			DeploymentID: d.ID,
			Sequence:     d.Sequence,
			Replica:      i,
			Image:        d.Spec.Image,
			Env:          d.Spec.Env,
			NanoCPUs:     d.Spec.Resources.NanoCPUs(),
			MemoryBytes:  d.Spec.Resources.MemoryBytes,
		}
		for _, v := range d.Spec.Volumes {
			cspec.Mounts = append(cspec.Mounts, docker.Mount{Volume: v.Name, Path: v.Path})
		}
		for _, p := range d.Spec.Publish {
			cspec.Publish = append(cspec.Publish, docker.PortBinding{Port: p.Port, HostPort: p.Host, Address: p.Address, Protocol: p.Protocol})
		}
		cspec.Entrypoint, cspec.Command, cspec.User = d.Spec.Entrypoint, d.Spec.Command, d.Spec.User
		if l := d.Spec.Logging; l != nil {
			cspec.LogDriver, cspec.LogOptions = l.Driver, l.Options
		}
		cspec.Init = d.Spec.Init
		id, name, err := e.createContainer(ctx, cspec)
		if err != nil {
			return created, fmt.Errorf("replica %d: %w", i, err)
		}
		// Record immediately: cleanup after a failure relies on this row.
		rep := store.Replica{DeploymentID: d.ID, Index: i, ContainerID: id, ContainerName: name}
		if err := e.store.AddReplica(ctx, rep, time.Now()); err != nil {
			e.removeContainer(context.WithoutCancel(ctx), id)
			return created, err
		}
		created = append(created, rep)
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
