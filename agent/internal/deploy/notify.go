package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// What gets a notification is deliberately short: the outcome of every
// deployment, and an application going down or coming back. Everything else —
// a single replica restarting, a health check failing once — is in the event
// feed for whoever comes looking; a message about it would be noise.

// notify hands an event to the notifier, if there is one. It never blocks
// and never fails: telling somebody is not part of the operation.
func (e *Engine) notify(ctx context.Context, ev notify.Event) {
	if e.opts.Notifier == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	e.opts.Notifier.Notify(ctx, ev)
}

// Notifications reports which channels are configured, for the server view.
func (e *Engine) Notifications() api.NotificationStatus {
	return api.NotificationStatus{Webhook: e.opts.Notifier != nil}
}

// notifySucceeded reports a deployment that is ACTIVE. A rollback on request
// is one, and is reported as the rollback it is.
func (e *Engine) notifySucceeded(ctx context.Context, d *store.Deployment, previous *store.Deployment) {
	kind, msg := notify.DeploymentSucceeded, ""
	switch d.Kind {
	case api.KindRollback:
		kind = notify.DeploymentRolledBack
		msg = fmt.Sprintf("%s rolled back to %s", d.Application, d.Version)
		if previous != nil {
			msg += " from " + previous.Version
		}
	case api.KindRedeploy:
		msg = fmt.Sprintf("%s was redeployed and is running %s", d.Application, d.Version)
	default:
		msg = fmt.Sprintf("%s is running %s", d.Application, d.Version)
		if previous != nil && previous.Version != d.Version {
			msg += ", replacing " + previous.Version
		}
	}
	e.notify(ctx, notify.Event{Kind: kind, Application: d.Application, DeploymentID: d.ID, Version: d.Version, Message: msg})
}

// notifyAborted reports a deployment that abort has finished with: FAILED,
// or ROLLED_BACK. The reason is read back from the record, where abort left
// it — including, after a rollback that failed too, both causes.
func (e *Engine) notifyAborted(r *rollout) {
	if e.opts.Notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	d, err := e.store.GetDeployment(ctx, r.d.ID)
	if err != nil {
		e.log.Warn("could not read the deployment to report it", "deployment", r.d.ID, "error", err)
		return
	}
	app := d.Application
	msg := fmt.Sprintf("%s deployment of %s failed: %s. ", app, d.Version, d.Error)
	kind := notify.DeploymentFailed
	switch {
	case d.Status == api.StatusRolledBack:
		kind = notify.DeploymentRolledBack
		msg += fmt.Sprintf("Rolled back: %s is running %s again", app, r.prev.Version)
	case r.prev == nil:
		msg += fmt.Sprintf("Nothing of %s is running. Fix the cause and deploy again; the events say more: shipwick status %s", app, app)
	case r.retired || r.recreate:
		// Part of the old version was replaced and could not be restored
		// here — the rollback failed, or the agent is shutting down. The
		// previous deployment is still the active one, and reconciliation
		// keeps working on it.
		msg += fmt.Sprintf("Shipwick is restoring %s; check with: shipwick status %s", r.prev.Version, app)
	default:
		msg += fmt.Sprintf("%s is still running %s; the failed deployment did not affect it", app, r.prev.Version)
	}
	e.notify(ctx, notify.Event{Kind: kind, Application: app, DeploymentID: d.ID, Version: d.Version, Message: msg})
}

// noteAvailability reports an application whose replicas have all stopped
// serving, and its recovery. Down is: not one ready replica. Recovered is
// stricter — every desired replica ready, none with a restart still held
// against it — so that a crash-looping replica, which runs for a moment
// between crashes, does not produce a recovery and a new outage at every
// restart. A replica is forgiven its restarts after StableAfter of running
// (see superviseRunning), and that is when the recovery is reported.
func (s *supervisor) noteAvailability(ctx context.Context, app store.Application, d store.Deployment, replicas []store.Replica, containers map[string]docker.Container) {
	if s.e.opts.Notifier == nil {
		return
	}
	ready, running, settled := 0, 0, true
	s.mu.Lock()
	for _, r := range replicas {
		c, exists := containers[r.ContainerID]
		if !exists {
			continue
		}
		health := api.HealthNone
		if st, ok := s.states[r.ContainerID]; ok {
			health = st.health
			settled = settled && st.restarts == 0
		}
		if c.Running {
			running++
		}
		if isReady(c.Running, health) {
			ready++
		}
	}
	wasDown := s.down[app.Name]
	isDown := ready == 0
	recovered := wasDown && ready >= d.Spec.Replicas && settled
	switch {
	case isDown && !wasDown:
		s.down[app.Name] = true
	case recovered:
		delete(s.down, app.Name)
	}
	s.mu.Unlock()

	switch {
	case isDown && !wasDown:
		why := fmt.Sprintf("none of its %s is running", plural(d.Spec.Replicas, "replica"))
		if running > 0 {
			why = fmt.Sprintf("%s running but failing the health check", plural(running, "replica"))
			if running == 1 {
				why = "1 replica running but failing its health check"
			}
		}
		msg := fmt.Sprintf("%s is down: %s. Shipwick restarts it as restart.policy allows; see why with: shipwick logs %s", app.Name, why, app.Name)
		s.e.notify(ctx, notify.Event{Kind: notify.ApplicationDown, Application: app.Name, DeploymentID: d.ID, Version: d.Version, Message: msg})
	case recovered:
		msg := fmt.Sprintf("%s is healthy again: %d/%d replicas running %s", app.Name, ready, d.Spec.Replicas, d.Version)
		s.e.notify(ctx, notify.Event{Kind: notify.ApplicationRecovered, Application: app.Name, DeploymentID: d.ID, Version: d.Version, Message: msg})
	}
}

// forgetDown drops the record of applications that no longer exist, so a
// later application of the same name does not start out "down".
func (s *supervisor) forgetDown(apps []store.Application) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := make(map[string]bool, len(apps))
	for _, app := range apps {
		exists[app.Name] = true
	}
	for name := range s.down {
		if !exists[name] {
			delete(s.down, name)
		}
	}
}
