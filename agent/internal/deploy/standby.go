package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
)

// A standby is a second server that holds what the first one runs, stopped.
// Shipwick does not fail over: nothing here watches the first server or
// decides that it is gone. What a standby has is the last export, imported
// with every application deployed and not started, and one operation — a
// promotion — that starts them in order and says which DNS records to
// change. A person decides, and the time it takes is the time DNS takes.

var (
	// ErrNoStandbySource means the agent has no bucket to fetch exports from.
	ErrNoStandbySource = errors.New("this agent has no bucket to fetch exports from: set SHIPWICK_BACKUP_S3_* and SHIPWICK_BACKUP_PASSPHRASE to those of the server it stands by for, and SHIPWICK_STANDBY_SCHEDULE")
	// ErrNoExport means the bucket holds no export.
	ErrNoExport = errors.New("the bucket holds no export yet: write one on the first server with shipwick export --to-backups, or set SHIPWICK_EXPORT_SCHEDULE there")
)

// executeDormant is the rollout of a deployment that is to exist without
// running: an import that leaves the application stopped. The image is
// obtained and the containers are created and recorded, as createReplicas
// does it for a volume restore; nothing is started, checked or routed, and
// the pre-deploy command does not run. The application is recorded as
// stopped before the first container exists, so the supervisor never takes
// it for one that should be running.
func (r *rollout) executeDormant(ctx context.Context) error {
	e, d := r.e, r.d
	if d.Spec.Static != nil {
		// A folder has no process to keep from starting: it is put into the
		// proxy like any other, and taken out of routing by being stopped.
		if err := r.executeStatic(ctx); err != nil {
			return err
		}
		return r.keepStopped(ctx, "The files are in the proxy and not served")
	}

	if err := r.reach(ctx, api.StatusBuilding); err != nil {
		return err
	}
	if d.Status == api.StatusBuilding {
		if err := e.pullImage(ctx, d); err != nil {
			return err
		}
		if d.Spec.PreDeploy != nil {
			e.event(ctx, d, api.LevelWarn, api.EventStep, "The pre-deploy command was not run: the application is deployed stopped. It does not run when the application is started either; run it with shipwick run if it is needed")
		}
	}
	if err := r.reach(ctx, api.StatusStarting); err != nil {
		return err
	}
	if err := e.rt.EnsureNetwork(ctx); err != nil {
		return err
	}
	app, err := e.store.GetApplication(ctx, d.Application)
	if err != nil {
		return err
	}
	if err := e.store.SetDesiredState(ctx, app.ID, api.DesiredStopped, time.Now()); err != nil {
		return err
	}
	if app.ActiveDeploymentID != nil {
		if prev, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID); err == nil {
			r.prev = &prev
		}
	}
	// Whatever runs of an earlier version stops being served and goes: two
	// versions of a stopped application are one too many, and their names
	// and volumes are needed.
	e.syncProxyBestEffort(ctx, d.Application)
	listed, err := e.rt.ListContainers(ctx, d.Application)
	if err != nil {
		return err
	}
	have := map[int]bool{}
	var old []docker.Container
	for _, c := range listed {
		switch {
		case c.Job != "" || e.draining(c.ID):
		case c.DeploymentID == d.ID:
			have[c.Replica] = true // created before the agent restarted
		default:
			old = append(old, c)
		}
	}
	for i, err := range e.retireAll(ctx, old) {
		if err != nil {
			return fmt.Errorf("remove container %s: %w", old[i].Name, err)
		}
	}
	if err := e.awaitDrains(ctx, d.Application); err != nil {
		return err
	}

	var missing []int
	for i := 1; i <= d.Spec.Replicas; i++ {
		if !have[i] {
			missing = append(missing, i)
		}
	}
	created, err := e.createReplicas(ctx, *d, missing)
	r.fresh = append(r.fresh, created...)
	if err != nil {
		return err
	}
	e.step(ctx, d, "Created %s, not started", plural(d.Spec.Replicas, "container"))

	// The statuses a deployment passes through on its way to ACTIVE; the
	// step above says what they mean here.
	if err := r.reach(ctx, api.StatusHealthChecking); err != nil {
		return err
	}
	if err := r.reach(ctx, api.StatusHealthy); err != nil {
		return err
	}
	previous, err := e.store.ActivateDeployment(ctx, d.ID, time.Now())
	if err != nil {
		return err
	}
	d.Status = api.StatusActive
	e.event(ctx, d, api.LevelInfo, api.EventState, string(api.StatusActive))

	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := r.keepStopped(sweepCtx, "Deployed stopped: nothing was started or health-checked"); err != nil {
		return err
	}
	e.retireOthers(sweepCtx, d, previous)
	if candidates, err := e.pruneCandidates(sweepCtx, d.Application, false); err == nil {
		e.pruneImages(sweepCtx, candidates)
	}
	e.retireStaticLeftovers(sweepCtx, d)
	return nil
}

// keepStopped records the application as stopped once more — the commit of a
// deployment says "running" — and says in the deployment's events how it is
// started.
func (r *rollout) keepStopped(ctx context.Context, what string) error {
	e, d := r.e, r.d
	app, err := e.store.GetApplication(ctx, d.Application)
	if err != nil {
		return err
	}
	if err := e.store.SetDesiredState(ctx, app.ID, api.DesiredStopped, time.Now()); err != nil {
		return err
	}
	e.syncProxyBestEffort(ctx, d.Application)
	how := "shipwick start " + d.Application
	if d.Kind == api.KindStandby {
		how = "shipwick standby promote"
	}
	e.step(ctx, d, "%s. Start it with: %s", what, how)
	return nil
}

// standbyApplications are the applications a promotion starts: imported
// stopped, and still stopped, in the order they are started in.
func (e *Engine) standbyApplications(ctx context.Context) ([]store.Deployment, error) {
	apps, active, err := e.deployedApplications(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Deployment
	for _, a := range apps {
		if d := active[a.ID]; d.Kind == api.KindStandby && a.DesiredState == api.DesiredStopped {
			out = append(out, d)
		}
	}
	// An import deploys in the order to start in, so the order of the
	// deployments it made is that order, whatever the applications' own ages
	// on this server.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// dnsRecords are the records that make the hostnames of the given
// deployments reach this server: one per hostname and address the agent
// knows of itself, or one without a value when it knows none.
func (e *Engine) dnsRecords(deployments []store.Deployment) []api.DNSRecord {
	records := []api.DNSRecord{}
	seen := map[string]bool{}
	for _, d := range deployments {
		for _, host := range hostnamesOf(d.Spec).list() {
			if seen[host] {
				continue
			}
			seen[host] = true
			if len(e.opts.ServerAddresses) == 0 {
				records = append(records, api.DNSRecord{Hostname: host, Type: "A"})
				continue
			}
			for _, addr := range e.opts.ServerAddresses {
				typ := "A"
				if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
					typ = "AAAA"
				}
				records = append(records, api.DNSRecord{Hostname: host, Type: typ, Value: addr})
			}
		}
	}
	return records
}

// Standby reports what this server holds for a promotion.
func (e *Engine) Standby(ctx context.Context) (api.Standby, error) {
	waiting, err := e.standbyApplications(ctx)
	if err != nil {
		return api.Standby{}, err
	}
	out := api.Standby{Applications: []api.StandbyApplication{}, Records: e.dnsRecords(waiting)}
	for _, d := range waiting {
		hosts := hostnamesOf(d.Spec).list()
		if hosts == nil {
			hosts = []string{}
		}
		out.Applications = append(out.Applications, api.StandbyApplication{Name: d.Application, Version: d.Version, Hostnames: hosts, ImportedAt: d.StartedAt})
	}
	if s := e.opts.Transfer.StandbySchedule; s != nil {
		t := e.transfer
		t.mu.Lock()
		pull := t.pull
		t.mu.Unlock()
		pull.Schedule = s.String()
		out.Pull = &pull
	}
	return out, nil
}

// Promote starts every application that was imported stopped, in the order
// an import deploys them, and answers with what became of each and with the
// DNS records to change. An application is waited for until it is ready, or
// until its startup budget is spent: what others reach by name comes first,
// and should be there when they start.
//
// An application that does not come up does not stop the promotion. The
// supervisor has it from the moment it is started, and the answer says so.
func (e *Engine) Promote(ctx context.Context) (api.Promotion, error) {
	waiting, err := e.standbyApplications(ctx)
	if err != nil {
		return api.Promotion{}, err
	}
	out := api.Promotion{Applications: []api.PromotedApplication{}, Records: e.dnsRecords(waiting)}
	e.log.Info("standby promoted", "by", actorFrom(ctx), "applications", len(waiting))
	for _, d := range waiting {
		p := api.PromotedApplication{Name: d.Application, Status: api.PromotedRunning}
		if err := e.Start(ctx, d.Application); err != nil {
			p.Status, p.Message = api.PromotedFailed, err.Error()
			if errors.Is(err, ErrBusy) {
				p.Message = "another operation is in progress for it; start it with: shipwick start " + d.Application
			}
			out.Applications = append(out.Applications, p)
			continue
		}
		if err := e.awaitStarted(ctx, d); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			p.Status = api.PromotedStarted
			p.Message = fmt.Sprintf("started, and not ready yet: %v. It is restarted until it is; watch it with: shipwick status %s", err, d.Application)
		}
		out.Applications = append(out.Applications, p)
	}
	return out, nil
}

// awaitStarted holds a started application to what a deployment holds a new
// replica to. A deployment's events are for the deployment, so nothing is
// recorded here; the caller words the outcome.
func (e *Engine) awaitStarted(ctx context.Context, d store.Deployment) error {
	if d.StaticDigest != "" {
		return nil
	}
	replicas, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return err
	}
	if d.Spec.Health != nil {
		return e.awaitHealthy(ctx, &d, replicas)
	}
	return e.awaitStable(ctx, &d, replicas)
}

// StartStandbyPull fetches the newest export from the bucket and imports it
// with every application left stopped, replacing the stopped ones that are
// there. The import runs in the background; the answer is its record as it
// begins, and ImportStatus follows it.
func (e *Engine) StartStandbyPull(ctx context.Context) (api.Import, error) {
	return e.pullStandby(ctx, false)
}

func (e *Engine) pullStandby(ctx context.Context, onlyNew bool) (api.Import, error) {
	source := e.opts.Transfer.StandbySource
	if source == nil {
		return api.Import{}, ErrNoStandbySource
	}
	newest, err := source.Newest(ctx, ExportOwner, exportFile)
	if err != nil {
		return api.Import{}, err
	}
	if newest == 0 {
		return api.Import{}, ErrNoExport
	}
	t := e.transfer
	t.mu.Lock()
	seen := t.pull.LastExport == newest && t.pull.LastError == ""
	t.mu.Unlock()
	if onlyNew && seen {
		return api.Import{}, nil
	}

	im, err := e.beginImport(ImportOptions{Stopped: true, Overwrite: true, Source: fmt.Sprintf("export #%d from the bucket", newest)})
	if err != nil {
		return api.Import{}, err
	}
	archive, err := source.Open(ctx, ExportOwner, newest, exportFile, true)
	if err != nil {
		// Recorded as an import that failed, so that it is seen.
		final, _ := im.run(ctx, failingReader{err})
		e.notePull(newest, final)
		return final, err
	}
	begun := *copyImport(im.rec)
	actor := actorFrom(ctx)
	go func() {
		defer archive.Close()
		final, _ := im.run(WithActor(e.baseCtx, actor), archive)
		e.notePull(newest, final)
	}()
	return begun, nil
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func (e *Engine) notePull(export int64, final api.Import) {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pull.LastAt, t.pull.LastExport, t.pull.LastError = final.CompletedAt, export, ""
	switch {
	case final.Error != "":
		t.pull.LastError = final.Error
	case final.Status == api.ImportFailed:
		for _, a := range final.Applications {
			if a.Status == api.ImportAppFailed {
				t.pull.LastError = a.Name + ": " + a.Message
				break
			}
		}
	}
}

// StartTransfers starts the loop that writes exports and fetches them on
// their schedules; it runs until the engine shuts down. Call it once, after
// StartBackups.
func (e *Engine) StartTransfers() {
	o := e.opts.Transfer
	if o.ExportSchedule == nil && o.StandbySchedule == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.bg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.bg.Done()
		ticker := time.NewTicker(e.opts.SuperviseInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.scheduleTransfers(e.baseCtx, now)
			}
		}
	}()
}

// scheduleTransfers is the tick of that loop. Like the job scheduler it
// starts what fell due since the minute it last looked, once, and its first
// look considers only the current minute.
func (e *Engine) scheduleTransfers(ctx context.Context, now time.Time) {
	o := e.opts.Transfer
	t := e.transfer
	minute := now.UTC().Truncate(time.Minute)

	t.mu.Lock()
	exportDue := due(o.ExportSchedule, &t.exportTick, minute)
	pullDue := due(o.StandbySchedule, &t.standbyTick, minute)
	t.mu.Unlock()

	if exportDue {
		if _, err := e.StartExport(ctx, api.BackupTriggerSchedule); err != nil {
			e.log.Warn("the scheduled export was not written", "error", err)
		}
	}
	if pullDue {
		if _, err := e.pullStandby(ctx, true); err != nil {
			e.log.Warn("the scheduled import from the bucket did not run", "error", err)
			t.mu.Lock()
			at := now.UTC()
			t.pull.LastAt, t.pull.LastError = &at, err.Error()
			t.mu.Unlock()
		}
	}
}

// due reports whether the schedule fires in (*last, minute], and moves *last
// on.
func due(s *cron.Schedule, last *time.Time, minute time.Time) bool {
	if s == nil {
		return false
	}
	from := *last
	if from.IsZero() {
		from = minute.Add(-time.Minute)
	}
	if !minute.After(from) {
		return false
	}
	*last = minute
	return !s.Next(from).After(minute)
}
