package deploy

import (
	"context"
	"errors"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
)

// scheduleJobs is the scheduler's tick, driven by the supervisor's ticker.
// For every running application it starts each job whose schedule fired since
// the minute it last looked at that application — once per job, however many
// minutes went by: a job due every minute that missed ten of them while the
// server was suspended runs once, not ten times.
//
// It takes the application's lock the way the supervisor does: briefly, and
// without waiting. An application that is busy — being deployed, say — is
// looked at again next tick, with the same window, so a minute that fell due
// during a deployment is not lost but run once the deployment is over.
func (e *Engine) scheduleJobs(ctx context.Context, now time.Time) {
	minute := now.UTC().Truncate(time.Minute)
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		e.log.Error("scheduler: list applications", "error", err)
		return
	}
	seen := make(map[string]bool, len(apps))
	for _, app := range apps {
		seen[app.Name] = true
		if app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
			continue
		}
		e.jobs.mu.Lock()
		last, known := e.jobs.lastTick[app.Name]
		e.jobs.mu.Unlock()
		if !known {
			// First sight considers the current minute and nothing before
			// it. What fell due while the agent was down is not caught up: a
			// job nobody expects to run right now is worse than one that is
			// missed, and the next scheduled time is never far.
			last = minute.Add(-time.Minute)
		}
		if !minute.After(last) {
			continue
		}
		if wait, err := e.tryLock(app.Name, true); wait != nil || err != nil {
			continue
		}
		e.scheduleApp(ctx, app, last, minute)
		e.unlock(app.Name)
		e.jobs.mu.Lock()
		e.jobs.lastTick[app.Name] = minute
		e.jobs.mu.Unlock()
	}

	e.jobs.mu.Lock()
	for name := range e.jobs.lastTick {
		if !seen[name] {
			delete(e.jobs.lastTick, name)
		}
	}
	e.jobs.mu.Unlock()
}

// scheduleApp starts the application's jobs due in (last, minute]. The caller
// holds the application's lock.
func (e *Engine) scheduleApp(ctx context.Context, app store.Application, last, minute time.Time) {
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		e.log.Error("scheduler: load active deployment", "app", app.Name, "error", err)
		return
	}
	for _, job := range d.Spec.Jobs {
		schedule, err := cron.Parse(job.Schedule)
		if err != nil {
			continue // validated when it was deployed
		}
		if schedule.Next(last).After(minute) {
			continue
		}
		_, err = e.startJob(ctx, d, job.Name, api.RunKindScheduled, job.Command, job.Timeout.Std())
		switch {
		case errors.Is(err, ErrJobRunning):
			e.log.Info("job skipped: its previous run is still in progress", "app", app.Name, "job", job.Name)
		case err != nil && ctx.Err() == nil:
			e.log.Error("could not start job", "app", app.Name, "job", job.Name, "error", err)
		}
	}
}
