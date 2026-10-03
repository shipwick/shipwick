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

// A deployment outlives the agent that began it. Upgrading Shipwick restarts
// the agent, and a deployment that was running at that moment has done nothing
// to deserve failing: so an agent that shuts down leaves it as it is, and the
// agent that starts next resumes it (Recover). A crash is the same thing
// without the warning.
//
// Nothing is written down for this beyond what a deployment records anyway.
// Where it was is in its status; what it had done is on the server:
//
//   - its containers carry its labels, and those the store knows are adopted
//     instead of created again;
//   - a replica that carries its application's names on the services network
//     was given them after it was verified, so it was serving, and serves on;
//   - a replica of the previous version whose container is gone was retired.
//
// Everything else is done again, and everything that is done again can be:
// pulling an image, creating a replica that does not exist, waiting for one
// to answer its health check, retiring one that still exists. The exception
// is the pre-deploy command. Whether a migration that was cut off can run a
// second time is for its author to say, not for Shipwick to find out: a
// deployment whose hook was running fails, and says so.

// prepare looks at what a resumed deployment left behind and takes over the
// application's routing accordingly, before anything else — the supervisor,
// a request — can look at the application: until the commit, the database
// names the previous deployment, some of whose replicas are gone.
func (r *rollout) prepare(ctx context.Context) error {
	d := r.d
	if d.Spec.Static != nil || progress[d.Status] < progress[api.StatusStarting] {
		return nil // nothing exists yet that the deployment could find
	}
	if err := r.loadPrevious(ctx); err != nil {
		return err
	}
	if err := r.survey(ctx); err != nil {
		return err
	}
	rollingBack := d.Status == api.StatusRollback || d.Status == api.StatusRestoring
	if rollingBack && r.prev == nil {
		return errors.New("it was rolling back to a version that is no longer the active one")
	}

	members, hosts := append([]routeMember(nil), r.serving...), hostnames{}
	if r.prev != nil {
		hosts = hostnamesOf(r.prev.Spec)
	}
	if in := r.adoptedInService(); len(in) > 0 {
		hosts = hostnamesOf(d.Spec)
		for _, rep := range in {
			members = append(members, routeMember{replica: rep, port: d.Spec.Port})
		}
	}
	if rollingBack {
		r.serving = members
	}
	r.e.routeVia(d.Application, routeOverride{hosts: hosts, members: members, desired: r.desired()})
	return nil
}

// survey finds what exists of the deployment and of the one it replaces. The
// caller has run loadPrevious.
func (r *rollout) survey(ctx context.Context) error {
	e, d := r.e, r.d
	containers, err := e.rt.ListContainers(ctx, d.Application)
	if err != nil {
		return err
	}
	exists := make(map[string]bool, len(containers))
	for _, c := range containers {
		exists[c.ID] = true
	}
	gone := func(rep store.Replica) error {
		return e.store.MarkReplicaRemoved(ctx, rep.ContainerID, time.Now())
	}

	// The previous version: a replica whose container is gone was retired,
	// between the removal and the note of it if the store still lists it.
	for i, rep := range r.old {
		if exists[rep.ContainerID] {
			continue
		}
		if err := gone(rep); err != nil {
			return err
		}
		delete(r.old, i)
	}
	serving := r.serving[:0:0]
	for _, m := range r.serving {
		if exists[m.replica.ContainerID] {
			serving = append(serving, m)
		}
	}
	r.serving = serving
	if r.prev != nil {
		for i := 1; i <= r.prev.Spec.Replicas; i++ {
			if _, alive := r.old[i]; !alive {
				r.retired = true
			}
		}
	}

	// The deployment's own replicas, as far as the store knows them.
	recorded, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return err
	}
	r.adopted, r.inService = map[int]store.Replica{}, map[int]bool{}
	for _, rep := range recorded {
		if !exists[rep.ContainerID] {
			if err := gone(rep); err != nil {
				return err
			}
			continue
		}
		in, err := e.rt.InspectContainer(ctx, rep.ContainerID)
		if err != nil {
			return fmt.Errorf("replica %d: %w", rep.Index, err)
		}
		r.adopted[rep.Index] = rep
		// Names are given to a replica once it has been verified, and to no
		// other: carrying them is having been in rotation.
		if in.Running && len(in.ServiceNames) > 0 {
			r.inService[rep.Index] = true
		}
	}
	r.fresh = sortedReplicas(r.adopted)

	// A container that was created and never recorded: the agent stopped
	// between the two. Nothing knows it, and its name is needed.
	for _, c := range containers {
		if c.Job != "" || c.DeploymentID != d.ID {
			continue
		}
		if rep, known := r.adopted[c.Replica]; known && rep.ContainerID == c.ID {
			continue
		}
		if err := e.rt.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	r.surveyed = true
	return nil
}

// adoptedInService lists the adopted replicas that were in rotation, in order.
func (r *rollout) adoptedInService() []store.Replica {
	var out []store.Replica
	for i := range r.inService {
		out = append(out, r.adopted[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// runHookOnce runs the pre-deploy command, unless a resumed deployment has
// run it before. Finished, it is not repeated; cut off, it is not repeated
// either, and the deployment fails.
func (r *rollout) runHookOnce(ctx context.Context) error {
	e, d := r.e, r.d
	if !r.resumed {
		return e.runHook(ctx, d)
	}
	runs, err := e.store.ListJobRuns(ctx, d.ApplicationID, hookJobName, jobRunsKept)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.DeploymentID == nil || *run.DeploymentID != d.ID {
			continue
		}
		switch run.Status {
		case api.RunSucceeded:
			e.step(ctx, d, "Pre-deploy command had finished")
			return nil
		case api.RunInterrupted, api.RunRunning:
			return errors.New("the pre-deploy command was interrupted when the agent restarted, and is not run a second time: check what it left behind, then deploy again")
		}
		return errors.New("the pre-deploy command had failed when the agent restarted; its output: shipwick runs " + d.Application)
	}
	return e.runHook(ctx, d)
}

// resumeRollback finishes a rollback that the agent's stop cut off: the
// previous version is completed again and verified, and what is left of the
// failed one removed. Like abort, it is not for a shutdown to interrupt.
func (r *rollout) resumeRollback() {
	e, d, prev := r.e, r.d, r.prev
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	e.step(ctx, d, "Resumed after the agent restarted")
	cause := errors.New(d.Error)
	if d.Spec.Deploy.Strategy == spec.StrategyRecreate {
		r.recreate = true
		r.stopped = sortedReplicas(r.old)
		r.rollBackRecreate(ctx, cause)
		return
	}
	// A replica of the old version that is not running was on its way out
	// when the rollout failed; it is the quickest one to restore.
	for _, rep := range sortedReplicas(r.old) {
		c, err := e.rt.InspectContainer(ctx, rep.ContainerID)
		if err != nil {
			continue
		}
		if !c.Running {
			if err := e.startNameless(ctx, rep.ContainerID); err != nil {
				e.log.Warn("could not start a replica of the previous version again", "container", rep.ContainerName, "error", err)
				continue
			}
			e.sup.reset(rep.ContainerID, prev.Spec.Health != nil)
		}
		r.unverified = append(r.unverified, rep)
	}
	r.rollBack(ctx, cause)
}

// resumable reports whether a deployment found in flight at startup can be
// taken up again, and if not, why.
func resumable(d store.Deployment, inFlight int) (bool, string) {
	switch {
	case inFlight > 1:
		return false, "several deployments of the application were in flight at once"
	case d.CompletedAt != nil:
		return false, "its record says it had completed"
	}
	return true, ""
}

// failInterrupted settles a deployment that cannot be resumed the way every
// interrupted deployment was settled before any could: FAILED, and what it
// had started removed as leftovers.
func (e *Engine) failInterrupted(ctx context.Context, d *store.Deployment, why string) error {
	msg := "agent restarted during deployment"
	if why != "" {
		msg += ", and it could not be resumed: " + why
	}
	e.log.Warn("interrupted deployment cannot be resumed", "app", d.Application, "deployment", d.ID, "status", d.Status, "reason", why)
	return e.transitionWithError(ctx, d, api.StatusFailed, msg)
}

// recoverLeftovers lists the containers that belong to a known application
// and to nothing that is running or resuming, and restores the engine's
// memory of the names the others carry.
func (e *Engine) recoverLeftovers(ctx context.Context, resuming map[int64]bool) ([]docker.Container, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var leftovers []docker.Container
	for _, c := range containers {
		activeID, known := active[c.App]
		if known && (c.Job != "" || (c.DeploymentID != activeID && !resuming[c.DeploymentID])) {
			e.log.Warn("removing leftover container", "container", c.Name, "app", c.App, "deployment", c.DeploymentID)
			leftovers = append(leftovers, c)
			continue
		}
		// What each replica answers to on the services network is known to
		// Docker alone. A replica created before that network existed answers
		// to nothing, and the first SyncProxy gives it its names — before the
		// proxy is told to look for them.
		if in, err := e.rt.InspectContainer(ctx, c.ID); err == nil && len(in.ServiceNames) > 0 {
			e.mu.Lock()
			e.names[c.ID] = in.ServiceNames
			e.mu.Unlock()
		}
	}
	return leftovers, nil
}
