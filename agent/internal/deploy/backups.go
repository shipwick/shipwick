package deploy

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Backups the agent takes itself: of an application's volumes, on the
// schedule in its deploy.yaml or when asked, and of its own state, daily.
// What `shipwick backup` downloads (volumes.go) is the same archive handed to
// whoever asked; these are kept on the server and in a bucket, counted, and
// can be proven to restore.
//
// A backup holds the application's lock from its first step to its last, like
// a deployment does: the archive is read through the replica's container, and
// a deployment that replaced it half-way would leave half an archive. A
// scheduled one takes the lock the way the supervisor does — an application
// that is busy is looked at again next tick, and a user operation that
// arrives meanwhile waits for it instead of failing at once.

var (
	// ErrBackupsDisabled means the agent was started without a place to keep
	// backups.
	ErrBackupsDisabled = errors.New("backups are not configured on this agent")
	// ErrNoVolumes means a backup was asked of an application without volumes.
	ErrNoVolumes = errors.New("the application has no volumes; a backup is an archive of its volumes")
	// ErrBackupBusy means the backup is being taken, verified, restored or
	// removed right now.
	ErrBackupBusy = errors.New("the backup is in use: it is still being taken, verified or restored")
	// ErrBackupNotUsable means the backup did not succeed, so nothing was kept
	// of it.
	ErrBackupNotUsable = errors.New("that backup did not succeed; nothing was kept of it")
	// ErrNoBackup means there is no successful backup to work with.
	ErrNoBackup = errors.New("there is no successful backup yet; take one with: shipwick backups run")
	// ErrStateNotEncrypted means the agent's state cannot be backed up because
	// it would have to be written unencrypted.
	ErrStateNotEncrypted = errors.New("the agent's state is not backed up: SHIPWICK_BACKUP_PASSPHRASE is not set, and the encryption key is never written anywhere unencrypted")
)

const (
	// backupFailuresKept bounds the failed backups remembered per application;
	// they hold no files, only what went wrong.
	backupFailuresKept = 20
	// stateBackupInterval is how often the agent's own state is backed up,
	// stateBackupRetry how soon after a failure it is tried again, and
	// stateBackupsKept how many are kept.
	stateBackupInterval = 24 * time.Hour
	stateBackupRetry    = time.Hour
	stateBackupsKept    = 7

	// The files of a backup of the agent's state.
	stateDatabaseFile = "shipwick.db"
	stateKeyFile      = "encryption.key"
)

// BackupOptions say where the agent keeps backups and what it backs up of
// itself.
type BackupOptions struct {
	// Storage is where backups go. Nil: this agent takes none.
	Storage *backup.Storage
	// EncryptionKey is the key the database's secrets are encrypted with when
	// the agent starts. It is backed up next to the database, which is
	// unreadable without it, and only ever through a Storage that encrypts.
	// After a key rotation the store knows the key, and its word is taken.
	EncryptionKey []byte
}

// backupSchedule is the scheduler's memory: up to which minute it has looked
// at each application, and when the agent's own state is due.
type backupSchedule struct {
	mu       sync.Mutex
	lastTick map[string]time.Time // by application
	stateDue time.Time
	// stateRunning is set while a backup of the agent's state is in progress.
	stateRunning bool

	// prepared is set once the destinations have been found to be this
	// installation's to write to, and the run ids in use there reserved: see
	// prepareBackups. preparing serializes the finding out.
	preparing sync.Mutex
	prepared  bool
}

func newBackupSchedule() *backupSchedule {
	return &backupSchedule{lastTick: map[string]time.Time{}}
}

func (b *backupSchedule) isPrepared() bool {
	b.preparing.Lock()
	defer b.preparing.Unlock()
	return b.prepared
}

// StartBackups settles what an earlier run of the agent left unfinished and
// starts the backup scheduler, which runs until the engine shuts down. Call
// it once, after Recover.
func (e *Engine) StartBackups(ctx context.Context) error {
	storage := e.opts.Backups.Storage
	if storage == nil {
		return nil
	}
	interrupted, err := e.store.InterruptBackupRuns(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, run := range interrupted {
		e.log.Warn("found interrupted backup", "app", run.Application, "backup", run.ID)
		if err := e.removeBackupFiles(ctx, run.Application, run.ID); err != nil {
			e.log.Warn("could not remove the files of an interrupted backup", "app", run.Application, "backup", run.ID, "error", err)
		}
	}
	// A copy of the database that was being encrypted when the agent died.
	if leftovers, err := filepath.Glob(filepath.Join(storage.Dir(), stateSnapshotPattern)); err == nil {
		for _, path := range leftovers {
			os.Remove(path)
		}
	}
	// Recover retires the container of a verification that was interrupted,
	// in the background; the volumes it restored into can go once it has,
	// and not before: a volume a container still mounts is not removed.
	if sweeper, ok := e.rt.(interface {
		RemoveScratchVolumes(context.Context) (int, error)
	}); ok && e.beginOp() {
		go func() {
			defer e.opDone()
			if err := e.awaitAllDrains(e.baseCtx); err != nil {
				return
			}
			if n, err := sweeper.RemoveScratchVolumes(e.baseCtx); err != nil {
				e.log.Warn("could not remove leftover verification volumes", "error", err)
			} else if n > 0 {
				e.log.Warn("removed leftover verification volumes", "volumes", n)
			}
		}()
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
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
				e.scheduleBackups(e.baseCtx, now)
			}
		}
	}()
	return nil
}

// scheduleBackups is the backup scheduler's tick. Like scheduleJobs, it
// starts what fell due since the minute it last looked at an application,
// once, and keeps the window when the application is busy: a backup due
// during a deployment is taken when the deployment is over.
func (e *Engine) scheduleBackups(ctx context.Context, now time.Time) {
	if e.opts.Backups.Storage == nil {
		return
	}
	minute := now.UTC().Truncate(time.Minute)
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		e.log.Error("backup scheduler: list applications", "error", err)
		return
	}
	seen := make(map[string]bool, len(apps))
	for _, app := range apps {
		seen[app.Name] = true
		// A stopped application is not backed up on schedule: nothing writes
		// to its volumes, and `before` has no process to run in.
		if app.ActiveDeploymentID == nil || app.DesiredState != api.DesiredRunning {
			continue
		}
		e.backups.mu.Lock()
		last, known := e.backups.lastTick[app.Name]
		e.backups.mu.Unlock()
		if !known {
			// As with jobs: what fell due while the agent was down is not
			// caught up.
			last = minute.Add(-time.Minute)
		}
		if !minute.After(last) {
			continue
		}
		// Looked at without the lock first: most applications have nothing
		// due most minutes, and need not be taken from the supervisor to
		// learn that.
		if e.backupDue(ctx, app, last, minute) {
			if !e.lockForSchedule(ctx, app.Name) {
				continue
			}
			if !e.scheduleAppBackup(ctx, app, last, minute) {
				e.unlock(app.Name)
			}
		}
		e.backups.mu.Lock()
		e.backups.lastTick[app.Name] = minute
		e.backups.mu.Unlock()
	}

	e.backups.mu.Lock()
	for name := range e.backups.lastTick {
		if !seen[name] {
			delete(e.backups.lastTick, name)
		}
	}
	e.backups.mu.Unlock()

	e.scheduleStateBackup(ctx, now)
}

// lockForSchedule takes the application's lock for the backup scheduler, the
// way the supervisor holds it. An application a user operation holds is left
// for the next tick. One the supervisor holds is waited for, briefly: the
// supervisor ticks at the same pace as this scheduler and holds every
// application for a moment each time, so "busy right now" would be the answer
// at every tick.
func (e *Engine) lockForSchedule(ctx context.Context, app string) bool {
	patience := time.NewTimer(e.opts.SuperviseInterval)
	defer patience.Stop()
	for {
		e.mu.Lock()
		held, busy := e.locks[app]
		e.mu.Unlock()
		if !busy {
			wait, err := e.tryLock(app, true)
			switch {
			case wait == nil && err == nil:
				return true
			case !errors.Is(err, ErrBusy):
				return false
			}
			continue // taken in between; look again at who has it
		}
		if !held.bySupervisor {
			return false
		}
		select {
		case <-held.released:
		case <-patience.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// backupDue reports whether the application's active deployment has a backup
// scheduled in (last, minute].
func (e *Engine) backupDue(ctx context.Context, app store.Application, last, minute time.Time) bool {
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return false // replaced or deleted in between; the next tick sees what is there
	}
	return scheduledBetween(d.Spec.Backups, last, minute)
}

func scheduledBetween(b *spec.Backups, last, minute time.Time) bool {
	if b == nil {
		return false
	}
	schedule, err := cron.Parse(b.Schedule)
	// The schedule was validated when it was deployed.
	return err == nil && !schedule.Next(last).After(minute)
}

// scheduleAppBackup starts the application's backup if its schedule fired in
// (last, minute]. The caller holds the application's lock; true means the
// backup has taken it over.
func (e *Engine) scheduleAppBackup(ctx context.Context, app store.Application, last, minute time.Time) bool {
	// Read again, now that nothing can change it: the deployment that was
	// active a moment ago may have been replaced, or the application stopped.
	name := app.Name
	app, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, ErrNotDeployed) {
			e.log.Error("backup scheduler: load active deployment", "app", name, "error", err)
		}
		return false
	}
	if app.DesiredState != api.DesiredRunning || !scheduledBetween(d.Spec.Backups, last, minute) {
		return false
	}
	if _, err := e.beginBackup(ctx, d, api.BackupTriggerSchedule); err != nil {
		if ctx.Err() == nil {
			e.log.Error("could not start the scheduled backup", "app", name, "error", err)
		}
		return false
	}
	return true
}

// StartBackup takes a backup of the application's volumes now and returns its
// record as soon as it exists; the archives are written in the background,
// and callers poll the run until it has completed.
func (e *Engine) StartBackup(ctx context.Context, name string) (api.BackupRun, error) {
	if e.opts.Backups.Storage == nil {
		return api.BackupRun{}, ErrBackupsDisabled
	}
	if err := e.lock(ctx, name); err != nil {
		return api.BackupRun{}, err
	}
	_, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		e.unlock(name)
		return api.BackupRun{}, err
	}
	run, err := e.beginBackup(ctx, d, api.BackupTriggerManual)
	if err != nil {
		e.unlock(name)
		return api.BackupRun{}, err
	}
	return backupView(run), nil
}

// activeDeployment loads an application and the deployment it runs.
func (e *Engine) activeDeployment(ctx context.Context, name string) (store.Application, store.Deployment, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return store.Application{}, store.Deployment{}, err
	}
	if app.ActiveDeploymentID == nil {
		return store.Application{}, store.Deployment{}, ErrNotDeployed
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return store.Application{}, store.Deployment{}, err
	}
	return app, d, nil
}

// prepareBackups finds out, once per run of the agent, whether the
// destinations are this installation's to write to, and moves the run ids
// past the ones already in use there: see backup.Storage.Prepare. Until it
// has succeeded it is tried again with every backup.
func (e *Engine) prepareBackups(ctx context.Context) error {
	b := e.backups
	b.preparing.Lock()
	defer b.preparing.Unlock()
	if b.prepared {
		return nil
	}
	installation, err := e.store.BackupInstallation(ctx)
	if err != nil {
		return err
	}
	highest, err := e.opts.Backups.Storage.Prepare(ctx, installation)
	if err != nil {
		return err
	}
	if err := e.store.ReserveBackupRunIDs(ctx, highest); err != nil {
		return err
	}
	b.prepared = true
	// Here, and only here: every upload of this agent waits for this function
	// to have succeeded once, so an upload the bucket holds unfinished now is
	// one an earlier agent died over, and its parts are paid for until
	// somebody aborts it. A bucket that will not say is no reason to refuse
	// backups.
	if n, err := e.opts.Backups.Storage.AbortLeftovers(ctx); err != nil {
		e.log.Warn("could not look for unfinished uploads in the bucket; the parts of an interrupted upload, if there is one, stay there", "error", err)
	} else if n > 0 {
		e.log.Warn("aborted unfinished uploads that an earlier run of the agent left in the bucket", "uploads", n)
	}
	return nil
}

// beginBackup records a backup of d's volumes and runs it in the background.
// The caller holds the application's lock, and on success no longer does: the
// backup keeps it until the archives are written.
func (e *Engine) beginBackup(ctx context.Context, d store.Deployment, trigger string) (store.BackupRun, error) {
	if d.StaticDigest != "" || len(d.Spec.Volumes) == 0 {
		return store.BackupRun{}, ErrNoVolumes
	}
	// Before the backup is recorded, because it decides the record's id. A
	// destination that is not ours to write to fails the backup; it is
	// recorded all the same, so that the failure is seen and told.
	refused := e.prepareBackups(ctx)
	run, err := e.store.CreateBackupRun(ctx, d.Application, trigger, time.Now())
	if err != nil {
		return store.BackupRun{}, err
	}
	e.log.Info("backup started", "app", d.Application, "backup", run.ID, "trigger", trigger)
	// What the caller is answered with: run is the goroutine's from here on.
	started := run
	go func() {
		defer e.opDone()
		err := refused
		if err == nil {
			err = e.takeBackup(e.baseCtx, d, &run)
		}
		// As with a deployment: completed_at is what clients wait for before
		// their next operation, so the lock is free before it shows.
		e.release(d.Application)
		e.finishBackup(d, run, err)
	}()
	return started, nil
}

// takeBackup writes one archive per volume. Whatever `before` and `stop` ask
// for happens first; an application that was stopped is started again when
// the function returns, however it returns.
func (e *Engine) takeBackup(ctx context.Context, d store.Deployment, run *store.BackupRun) error {
	replicas, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return err
	}
	if len(replicas) == 0 {
		return fmt.Errorf("%s has no container to read its volumes through; deploy it again", d.Application)
	}
	// An application with volumes has one replica; the volumes are read
	// through it.
	source := replicas[0].ContainerID

	if b := d.Spec.Backups; b != nil {
		if len(b.Before) > 0 {
			if err := e.runBefore(ctx, d, source, run, b); err != nil {
				return err
			}
		}
		if b.Stop {
			restart, err := e.stopForBackup(ctx, d, replicas)
			defer restart()
			if err != nil {
				return err
			}
		}
	}

	storage := e.opts.Backups.Storage
	for _, v := range d.Spec.Volumes {
		archive, err := e.rt.ExportPath(ctx, source, v.Path)
		if err != nil {
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
		n, err := storage.Write(ctx, d.Application, run.ID, v.Name+".tar", archive)
		archive.Close()
		if err != nil {
			return fmt.Errorf("volume %s: %w", v.Name, err)
		}
		run.Volumes = append(run.Volumes, api.BackupVolume{Volume: v.Name, SizeBytes: n})
	}
	return nil
}

// backupBefore runs `backups.before` inside the replica. Anything but exit 0
// fails the backup: an archive without the dump it was meant to hold is not
// the backup that was asked for. The command has backups.before_timeout: a
// dump of a large database takes its time, a command that hangs must not hold
// the application's lock for ever. At the limit the backup is given up; the
// command itself is the container's, since Docker cannot end what it started
// there (Runtime.Exec); `before_in: container` runs it where it can be ended
// (backups_before.go).
func (e *Engine) backupBefore(ctx context.Context, containerID string, b *spec.Backups) error {
	limit := b.BeforeLimit()
	code, output, err := e.rt.Exec(ctx, containerID, b.Before, limit)
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf("backups.before did not finish within %s; the backup was given up and nothing was archived. Give the command longer with backups.before_timeout in deploy.yaml (up to %s)",
			shortDuration(limit), shortDuration(spec.MaxBackupBeforeTimeout))
	case err != nil:
		return fmt.Errorf("backups.before could not run: %w; nothing was archived", err)
	case code != 0:
		if line := lastLine(output); line != "" {
			return fmt.Errorf("backups.before exited %d: %s; nothing was archived", code, line)
		}
		return fmt.Errorf("backups.before exited %d; nothing was archived", code)
	}
	return nil
}

// stopForBackup stops the replicas that run and returns the function that
// starts them again. The application's desired state is not touched: it is
// meant to run, and if the agent dies in between, the supervisor of the next
// one starts what it finds stopped.
func (e *Engine) stopForBackup(ctx context.Context, d store.Deployment, replicas []store.Replica) (restart func(), err error) {
	var running []store.Replica
	for _, r := range replicas {
		if c, err := e.rt.InspectContainer(ctx, r.ContainerID); err == nil && c.Running {
			running = append(running, r)
		}
	}
	stoppedAt := time.Now()
	restart = func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		for _, r := range running {
			if err := e.startNameless(ctx, r.ContainerID); err != nil {
				e.backupEvent(d, api.LevelError, fmt.Sprintf("Replica %d could not be started again after the backup: %v", r.Index, err))
				continue
			}
			// Like any replica started on request: with a health check, no
			// traffic until it has passed it.
			e.sup.reset(r.ContainerID, d.Spec.Health != nil)
		}
		e.syncProxyBestEffort(ctx, d.Application)
		if len(running) > 0 {
			// Said in the event feed: whoever wonders why the application
			// was away for a moment at three in the morning looks there.
			e.backupEvent(d, api.LevelInfo, fmt.Sprintf("Stopped for a backup (backups.stop) and started again after %s", shortDuration(time.Since(stoppedAt).Round(time.Second))))
		}
	}
	errs := parallel(running, func(r store.Replica) error {
		return e.rt.StopContainer(ctx, r.ContainerID, e.opts.StopTimeout)
	})
	e.syncProxyBestEffort(ctx, d.Application)
	for i, err := range errs {
		if err != nil {
			return restart, fmt.Errorf("stop replica %d: %w", running[i].Index, err)
		}
	}
	return restart, nil
}

// finishBackup records how a backup ended. A failed one keeps nothing: an
// archive of two volumes out of three, or one that never reached the bucket,
// would be counted as a backup and is not one.
func (e *Engine) finishBackup(d store.Deployment, run store.BackupRun, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	storage := e.opts.Backups.Storage

	if failure == nil {
		run.Status, run.Destinations, run.Encrypted = api.BackupSucceeded, storage.Destinations(), storage.Encrypted()
	} else {
		run.Status, run.Volumes, run.Error = api.BackupFailed, []api.BackupVolume{}, failure.Error()
		if e.baseCtx.Err() != nil {
			run.Error = "the agent was shut down while the backup was being taken"
		}
		// A backup that was refused before it began wrote nothing, and its
		// id says nothing about whose files sit under it.
		if e.backups.isPrepared() {
			if err := e.removeBackupFiles(ctx, run.Application, run.ID); err != nil {
				e.log.Warn("could not remove the files of a failed backup", "app", run.Application, "backup", run.ID, "error", err)
			}
		}
	}
	if err := e.store.FinishBackupRun(ctx, run, time.Now()); err != nil {
		e.log.Error("could not record the outcome of a backup", "app", run.Application, "backup", run.ID, "error", err)
	}

	keep := spec.DefaultBackupKeep
	if d.Spec.Backups != nil {
		keep = d.Spec.Backups.Keep
	}
	switch {
	case failure == nil:
		e.log.Info("backup finished", "app", run.Application, "backup", run.ID, "bytes", backupSize(run))
	case e.baseCtx.Err() != nil:
		// Shutting down: the record says so, and nobody reads the events of
		// an agent that is going away.
	default:
		e.backupEvent(d, api.LevelWarn, fmt.Sprintf("Backup #%d failed: %s", run.ID, run.Error))
		if run.Trigger == api.BackupTriggerSchedule {
			e.notify(ctx, notify.Event{
				Kind:        notify.BackupFailed,
				Application: run.Application,
				Version:     d.Version,
				Message:     fmt.Sprintf("%s: the scheduled backup failed: %s. See its backups with: shipwick backups %s", run.Application, run.Error, run.Application),
			})
		}
	}
	e.pruneBackups(ctx, run.Application, keep)
}

func backupSize(run store.BackupRun) int64 {
	var n int64
	for _, v := range run.Volumes {
		n += v.SizeBytes
	}
	return n
}

// pruneBackups applies retention to one owner's backups: the newest `keep`
// successful ones stay, and the last few failures as a record of what went
// wrong. Only successes count towards keep, so a week of failures never
// pushes out the one backup that worked. A backup that is being verified or
// restored is left for the next pass.
func (e *Engine) pruneBackups(ctx context.Context, owner string, keep int) {
	runs, err := e.store.ListBackupRuns(ctx, owner, 0)
	if err != nil {
		e.log.Warn("could not list backups to prune", "app", owner, "error", err)
		return
	}
	succeeded, failed := 0, 0
	for _, r := range runs {
		switch r.Status {
		case api.BackupSucceeded:
			if succeeded++; succeeded <= keep {
				continue
			}
		case api.BackupFailed:
			if failed++; failed <= backupFailuresKept {
				continue
			}
		default:
			continue
		}
		if r.Activity != "" {
			continue
		}
		if err := e.removeBackup(ctx, r); err != nil {
			e.log.Warn("could not remove an old backup; it is tried again after the next one", "app", owner, "backup", r.ID, "error", err)
		}
	}
}

// removeBackup removes a backup's files, then its record. In that order: a
// record without files would be a backup that cannot be restored, files
// without a record merely take space until the next attempt.
func (e *Engine) removeBackup(ctx context.Context, run store.BackupRun) error {
	// A failed backup kept nothing; only its record is left to remove.
	if run.Status != api.BackupFailed {
		if err := e.removeBackupFiles(ctx, run.Application, run.ID); err != nil {
			return err
		}
	}
	return e.store.DeleteBackupRun(ctx, run.ID)
}

// backupEvent records what a backup did in the application's event feed.
func (e *Engine) backupEvent(d store.Deployment, level, message string) {
	e.log.Log(context.Background(), slogLevel(level), message, "app", d.Application)
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := e.store.AddEvent(ctx, d.ApplicationID, nil, level, api.EventBackup, message, time.Now()); err != nil {
		e.log.Warn("could not record event", "app", d.Application, "error", err)
	} else if err := e.store.PruneApplicationEvents(ctx, d.ApplicationID, appEventsKept); err != nil {
		e.log.Warn("could not prune events", "app", d.Application, "error", err)
	}
}

// Backups lists the application's backups, newest first.
func (e *Engine) Backups(ctx context.Context, name string, limit int) ([]api.BackupRun, error) {
	if _, err := e.store.GetApplication(ctx, name); err != nil {
		return nil, err
	}
	return e.listBackups(ctx, name, limit)
}

func (e *Engine) listBackups(ctx context.Context, owner string, limit int) ([]api.BackupRun, error) {
	runs, err := e.store.ListBackupRuns(ctx, owner, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.BackupRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, backupView(r))
	}
	return out, nil
}

// BackupRun returns one backup of the application, with the output of its
// last verification.
func (e *Engine) BackupRun(ctx context.Context, name string, id int64) (api.BackupRunDetail, error) {
	if _, err := e.store.GetApplication(ctx, name); err != nil {
		return api.BackupRunDetail{}, err
	}
	run, err := e.store.GetBackupRun(ctx, name, id)
	if err != nil {
		return api.BackupRunDetail{}, err
	}
	return api.BackupRunDetail{BackupRun: backupView(run), VerifyOutput: run.VerifyOutput}, nil
}

// DeleteBackup removes one backup of the application: its files in every
// destination, and its record.
func (e *Engine) DeleteBackup(ctx context.Context, name string, id int64) error {
	if e.opts.Backups.Storage == nil {
		return ErrBackupsDisabled
	}
	if _, err := e.store.GetApplication(ctx, name); err != nil {
		return err
	}
	run, err := e.store.GetBackupRun(ctx, name, id)
	if err != nil {
		return err
	}
	// Claimed like a verification, so that neither starts on a backup whose
	// files are going.
	if err := e.store.ClaimBackupRun(ctx, run.ID, backupActivityRemove); err != nil {
		return backupBusy(err)
	}
	if err := e.removeBackup(ctx, run); err != nil {
		if rerr := e.store.ReleaseBackupRun(context.WithoutCancel(ctx), run.ID); rerr != nil {
			e.log.Warn("could not release a backup", "app", name, "backup", run.ID, "error", rerr)
		}
		return err
	}
	e.log.Info("backup removed", "app", name, "backup", run.ID, "by", actorFrom(ctx))
	return nil
}

// backupActivityRemove marks a backup whose files are being removed. It is
// never seen through the API: the record goes with the files.
const backupActivityRemove = "remove"

func backupBusy(err error) error {
	if errors.Is(err, store.ErrBackupBusy) {
		return ErrBackupBusy
	}
	return err
}

// usableBackup resolves the backup a restore, a verification or a download
// works on: the given one, or with id 0 the application's latest that
// succeeded.
func (e *Engine) usableBackup(ctx context.Context, name string, id int64) (store.BackupRun, error) {
	if id == 0 {
		run, err := e.store.LastBackupRun(ctx, name, api.BackupSucceeded)
		if errors.Is(err, store.ErrNotFound) {
			return store.BackupRun{}, ErrNoBackup
		}
		return run, err
	}
	run, err := e.store.GetBackupRun(ctx, name, id)
	if err != nil {
		return store.BackupRun{}, err
	}
	switch run.Status {
	case api.BackupRunning:
		return store.BackupRun{}, ErrBackupBusy
	case api.BackupFailed:
		return store.BackupRun{}, ErrBackupNotUsable
	}
	return run, nil
}

// OpenBackupArchive streams one volume's archive of a backup, decrypted: the
// tar file `shipwick restore` takes. It also returns the archive's size, so
// that whoever receives it can tell a whole one from one that was cut short.
func (e *Engine) OpenBackupArchive(ctx context.Context, name string, id int64, volume string) (io.ReadCloser, int64, error) {
	storage := e.opts.Backups.Storage
	if storage == nil {
		return nil, 0, ErrBackupsDisabled
	}
	if _, err := e.store.GetApplication(ctx, name); err != nil {
		return nil, 0, err
	}
	run, err := e.usableBackup(ctx, name, id)
	if err != nil {
		return nil, 0, err
	}
	for _, v := range run.Volumes {
		if v.Volume == volume {
			archive, err := storage.Open(ctx, name, run.ID, volume+".tar", run.Encrypted)
			return archive, v.SizeBytes, err
		}
	}
	return nil, 0, ErrVolumeNotFound
}

// VerifyBackup proves that a backup restores: its archives are extracted into
// scratch volumes, and one container of the application's current image is
// started on them and held to the application's health check, as a deployment
// would hold a replica. id 0 means the latest successful backup. The answer
// is the backup, now being verified; callers poll it until its activity is
// empty again.
//
// The container is not a replica. It is a job as far as the rest of the
// engine is concerned — no route, no name on the services network, skipped by
// the supervisor — and it runs next to the application, which never notices:
// the lock is held only to decide what to verify with.
func (e *Engine) VerifyBackup(ctx context.Context, name string, id int64) (api.BackupRun, error) {
	if e.opts.Backups.Storage == nil {
		return api.BackupRun{}, ErrBackupsDisabled
	}
	if err := e.lock(ctx, name); err != nil {
		return api.BackupRun{}, err
	}
	defer e.unlock(name)

	_, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		return api.BackupRun{}, err
	}
	run, err := e.usableBackup(ctx, name, id)
	if err != nil {
		return api.BackupRun{}, err
	}
	if !e.beginOp() {
		return api.BackupRun{}, ErrShuttingDown
	}
	if err := e.store.ClaimBackupRun(ctx, run.ID, api.BackupActivityVerify); err != nil {
		e.opDone()
		return api.BackupRun{}, backupBusy(err)
	}
	run.Activity = api.BackupActivityVerify
	go func() {
		defer e.opDone()
		failure, output := e.verifyBackup(e.baseCtx, d, run)
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := e.store.FinishBackupVerify(ctx, run.ID, failure, output, time.Now()); err != nil {
			e.log.Error("could not record the outcome of a verification", "app", name, "backup", run.ID, "error", err)
		}
		switch {
		case failure == "":
			e.backupEvent(d, api.LevelInfo, fmt.Sprintf("Backup #%d verified: it restores, and %s", run.ID, verifiedHow(d, e.opts.StabilizeWindow)))
		case e.baseCtx.Err() == nil:
			e.backupEvent(d, api.LevelWarn, fmt.Sprintf("Backup #%d did not verify: %s", run.ID, failure))
		}
	}()
	return backupView(run), nil
}

// verifiedHow says what a successful verification established, which depends
// on whether the application has a health check to hold the container to.
func verifiedHow(d store.Deployment, window time.Duration) string {
	if d.Spec.Health != nil {
		return fmt.Sprintf("%s passed its health check on the restored data", d.Version)
	}
	return fmt.Sprintf("%s started on the restored data and stayed up for %s (it has no health check)", d.Version, shortDuration(window))
}

// verifyBackup runs one verification to its end and cleans up after it,
// whatever happened. It returns why the backup did not verify — empty when it
// did — and the last output of the container.
func (e *Engine) verifyBackup(ctx context.Context, d store.Deployment, run store.BackupRun) (failure, output string) {
	storage := e.opts.Backups.Storage
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	cspec := containerFor(d)
	cspec.Entrypoint, cspec.Command = d.Spec.Entrypoint, d.Spec.Command
	// No published ports: those are the running replica's.
	cspec.Job = &docker.JobSpec{Name: docker.VerifyJob, RunID: run.ID}
	paths := map[string]string{}
	for _, v := range d.Spec.Volumes {
		paths[v.Name] = v.Path
		cspec.Mounts = append(cspec.Mounts, docker.Mount{Volume: docker.ScratchVolume(v.Name, run.ID), Path: v.Path})
	}
	for _, v := range run.Volumes {
		if paths[v.Volume] == "" {
			return fmt.Sprintf("the backup holds volume %s, which the application's deploy.yaml no longer has", v.Volume), ""
		}
	}

	// Deferred first, so that it runs last: a volume cannot go while the
	// container that mounts it exists.
	defer func() {
		for _, m := range cspec.Mounts {
			if err := e.rt.RemoveVolume(cleanup, d.Application, m.Volume); err != nil {
				e.log.Warn("could not remove a verification volume", "app", d.Application, "volume", docker.VolumeName(d.Application, m.Volume), "error", err)
			}
		}
	}()
	id, _, err := e.rt.CreateContainer(ctx, cspec)
	if err != nil {
		return "the container could not be created: " + err.Error(), ""
	}
	defer func() {
		if err := e.rt.StopContainer(cleanup, id, e.opts.StopTimeout); err != nil {
			e.log.Warn("could not stop the verification container", "container", id, "error", err)
		}
		if err := e.rt.RemoveContainer(cleanup, id); err != nil {
			e.log.Warn("could not remove the verification container", "container", id, "error", err)
		}
	}()

	// Extracted before the process ever runs, as a restore does it.
	for _, v := range run.Volumes {
		archive, err := storage.Open(ctx, d.Application, run.ID, v.Volume+".tar", run.Encrypted)
		if err != nil {
			return fmt.Sprintf("volume %s could not be read from the backup: %v", v.Volume, err), ""
		}
		err = e.rt.ImportPath(ctx, id, paths[v.Volume], archive)
		archive.Close()
		if err != nil {
			return fmt.Sprintf("volume %s could not be restored: %v", v.Volume, err), ""
		}
	}
	if err := e.rt.StartContainer(ctx, id); err != nil {
		return "the container could not be started: " + err.Error(), ""
	}
	err = e.awaitVerified(ctx, &d, id)
	output = e.jobOutput(cleanup, id)
	if err != nil {
		if ctx.Err() != nil {
			return "the agent was shut down during the verification", output
		}
		return err.Error(), output
	}
	return "", output
}

// awaitVerified holds the verification container to what a deployment holds a
// new replica to: its health check within the startup budget, or, without
// one, staying up for the stabilization window.
func (e *Engine) awaitVerified(ctx context.Context, d *store.Deployment, id string) error {
	h := d.Spec.Health
	budget, pace := e.opts.StabilizeWindow, max(e.opts.StabilizeWindow/10, 10*time.Millisecond)
	if h != nil {
		budget, pace = StartupBudget(h), e.opts.StartupPollInterval
	}
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	poll := time.NewTicker(pace)
	defer poll.Stop()

	var last error
	for {
		c, err := e.rt.InspectContainer(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case !c.Running && c.OOMKilled:
			return errors.New("the container was killed for exceeding its memory limit on the restored data")
		case !c.Running:
			return fmt.Errorf("the container exited with code %d on the restored data", c.ExitCode)
		case h == nil:
		case c.IP == "":
			last = errors.New("container has no address on the Shipwick network yet")
		default:
			if last = e.probeReplica(ctx, d, c); last == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		select {
		case <-deadline.C:
			if h == nil {
				return nil
			}
			return fmt.Errorf("the container did not become healthy on the restored data within %s: %v", shortDuration(budget), withCheck(h, d.Spec.Port, last))
		case <-poll.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// RestoreBackup replaces the application's volumes with a backup's archives.
// The application must be stopped, as for Restore, and stays stopped. The
// answer is the backup, now being restored; callers poll it until its
// activity is empty again.
func (e *Engine) RestoreBackup(ctx context.Context, name string, id int64) (api.BackupRun, error) {
	if e.opts.Backups.Storage == nil {
		return api.BackupRun{}, ErrBackupsDisabled
	}
	if err := e.lock(ctx, name); err != nil {
		return api.BackupRun{}, err
	}
	run, err := e.claimRestore(ctx, name, id)
	// Released before the restore starts: Restore takes the lock itself, once
	// per volume.
	e.unlock(name)
	if err != nil {
		return api.BackupRun{}, err
	}
	// Detached from the request, which ends long before the restore does, but
	// still done in the caller's name.
	actor := actorFrom(ctx)
	go func() {
		defer e.opDone()
		failure := e.restoreBackup(WithActor(e.baseCtx, actor), name, run)
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := e.store.FinishBackupRestore(ctx, run.ID, failure, time.Now()); err != nil {
			e.log.Error("could not record the outcome of a restore", "app", name, "backup", run.ID, "error", err)
		}
	}()
	return backupView(run), nil
}

// claimRestore checks, under the application's lock, that the backup can be
// restored into the application as it stands, and claims it. On success an
// operation has begun, which the caller ends.
func (e *Engine) claimRestore(ctx context.Context, name string, id int64) (store.BackupRun, error) {
	app, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		return store.BackupRun{}, err
	}
	run, err := e.usableBackup(ctx, name, id)
	if err != nil {
		return store.BackupRun{}, err
	}
	if app.DesiredState != api.DesiredStopped {
		return store.BackupRun{}, ErrNotStopped
	}
	mounted := map[string]bool{}
	for _, v := range d.Spec.Volumes {
		mounted[v.Name] = true
	}
	for _, v := range run.Volumes {
		if !mounted[v.Volume] {
			return store.BackupRun{}, ErrVolumeNotFound
		}
	}
	if !e.beginOp() {
		return store.BackupRun{}, ErrShuttingDown
	}
	if err := e.store.ClaimBackupRun(ctx, run.ID, api.BackupActivityRestore); err != nil {
		e.opDone()
		return store.BackupRun{}, backupBusy(err)
	}
	run.Activity = api.BackupActivityRestore
	return run, nil
}

// restoreBackup restores the backup's volumes one after the other and
// returns why it stopped, empty when all of them are back.
func (e *Engine) restoreBackup(ctx context.Context, name string, run store.BackupRun) string {
	storage := e.opts.Backups.Storage
	for _, v := range run.Volumes {
		archive, err := storage.Open(ctx, name, run.ID, v.Volume+".tar", run.Encrypted)
		if err != nil {
			return fmt.Sprintf("volume %s: %v", v.Volume, err)
		}
		// Looked at before Restore does: it would call a backup that does not
		// decrypt "not a tar file", which is true and no help.
		head := bufio.NewReaderSize(archive, 64<<10)
		if _, err := head.Peek(1); err != nil && !errors.Is(err, io.EOF) {
			archive.Close()
			return fmt.Sprintf("volume %s: %v", v.Volume, err)
		}
		err = e.Restore(ctx, name, v.Volume, head)
		archive.Close()
		if err != nil {
			return fmt.Sprintf("volume %s: %v", v.Volume, err)
		}
	}
	return ""
}

func backupView(r store.BackupRun) api.BackupRun {
	activity := r.Activity
	if activity == backupActivityRemove {
		activity = ""
	}
	return api.BackupRun{
		ID:           r.ID,
		Trigger:      r.Trigger,
		Status:       r.Status,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
		Volumes:      r.Volumes,
		Destinations: r.Destinations,
		Encrypted:    r.Encrypted,
		Error:        r.Error,
		Activity:     activity,
		VerifiedAt:   r.VerifiedAt,
		VerifyError:  r.VerifyError,
		RestoredAt:   r.RestoredAt,
		RestoreError: r.RestoreError,
	}
}

// The agent's own state: the database and the key that encrypts the secrets
// in it. Losing the key loses every secret, and a database copied while it
// is in use may not open; so the agent takes a consistent copy of the one and
// the other once a day, and keeps them like any backup — but only ever
// encrypted. An unencrypted copy of the key next to the database would undo
// what the key is for.

// scheduleStateBackup takes the daily backup of the agent's state when it is
// due: a day after the last one, an hour after one that failed.
func (e *Engine) scheduleStateBackup(ctx context.Context, now time.Time) {
	if !e.opts.Backups.Storage.Encrypted() {
		return
	}
	b := e.backups
	b.mu.Lock()
	wait := b.stateRunning || now.Before(b.stateDue)
	b.mu.Unlock()
	if wait {
		return
	}
	last, err := e.store.ListBackupRuns(ctx, backup.StateOwner, 1)
	if err != nil {
		e.log.Error("backup scheduler: list backups of the agent's state", "error", err)
		return
	}
	if len(last) > 0 {
		due := last[0].StartedAt.Add(stateBackupInterval)
		if last[0].Status == api.BackupFailed {
			due = last[0].StartedAt.Add(stateBackupRetry)
		}
		if now.Before(due) {
			b.mu.Lock()
			b.stateDue = due
			b.mu.Unlock()
			return
		}
	}
	if _, err := e.StartStateBackup(ctx, api.BackupTriggerSchedule); err != nil && ctx.Err() == nil {
		e.log.Error("could not start the backup of the agent's state", "error", err)
	}
}

// StartStateBackup backs up the agent's database and encryption key now. Like
// StartBackup it returns the record at once; the files are written in the
// background.
func (e *Engine) StartStateBackup(ctx context.Context, trigger string) (api.BackupRun, error) {
	storage := e.opts.Backups.Storage
	switch {
	case storage == nil:
		return api.BackupRun{}, ErrBackupsDisabled
	case !storage.Encrypted():
		return api.BackupRun{}, ErrStateNotEncrypted
	}
	b := e.backups
	b.mu.Lock()
	if b.stateRunning {
		b.mu.Unlock()
		return api.BackupRun{}, ErrBackupBusy
	}
	b.stateRunning = true
	b.mu.Unlock()
	done := func() {
		b.mu.Lock()
		b.stateRunning = false
		b.mu.Unlock()
	}
	if !e.beginOp() {
		done()
		return api.BackupRun{}, ErrShuttingDown
	}
	// The database is copied before this backup is recorded in it. A copy that
	// held its own backup as "running" would, restored, take that backup for
	// one an agent died over — and remove its files, the very files it was
	// restored from.
	failure := e.prepareBackups(ctx)
	var snapshot string
	var key []byte
	if failure == nil {
		snapshot, key, failure = e.snapshotState(ctx)
	}
	run, err := e.store.CreateBackupRun(ctx, backup.StateOwner, trigger, time.Now())
	if err != nil {
		if snapshot != "" {
			os.Remove(snapshot)
		}
		e.opDone()
		done()
		return api.BackupRun{}, err
	}
	started := backupView(run)
	go func() {
		defer e.opDone()
		defer done()
		err := failure
		if err == nil {
			err = e.takeStateBackup(e.baseCtx, &run, snapshot, key)
			os.Remove(snapshot)
		}
		e.finishStateBackup(run, err)
	}()
	return started, nil
}

// stateSnapshotPattern matches the copies snapshotState makes.
const stateSnapshotPattern = ".state-*.db"

// snapshotState writes a consistent copy of the database next to the backups
// and returns its path, and the key its secrets are sealed with. The copy
// holds what the database holds; it exists for as long as it takes to encrypt
// it.
func (e *Engine) snapshotState(ctx context.Context) (string, []byte, error) {
	dir := e.opts.Backups.Storage.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create backup directory: %w", err)
	}
	// A name nobody else has, and no file under it: the copy must be the one
	// to create it.
	placeholder, err := os.CreateTemp(dir, stateSnapshotPattern)
	if err != nil {
		return "", nil, fmt.Errorf("copy the database: %w", err)
	}
	path := placeholder.Name()
	placeholder.Close()
	if err := os.Remove(path); err != nil {
		return "", nil, fmt.Errorf("copy the database: %w", err)
	}
	key, err := e.store.Snapshot(ctx, path)
	if err != nil {
		os.Remove(path)
		return "", nil, err
	}
	if len(key) == 0 {
		key = e.opts.Backups.EncryptionKey
	}
	return path, key, nil
}

func (e *Engine) takeStateBackup(ctx context.Context, run *store.BackupRun, snapshot string, encryptionKey []byte) error {
	storage := e.opts.Backups.Storage
	if len(encryptionKey) == 0 {
		return errors.New("the agent has no encryption key to back up")
	}
	f, err := os.Open(snapshot)
	if err != nil {
		return fmt.Errorf("read the database copy: %w", err)
	}
	n, err := storage.Write(ctx, backup.StateOwner, run.ID, stateDatabaseFile, f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", stateDatabaseFile, err)
	}
	run.Volumes = append(run.Volumes, api.BackupVolume{Volume: stateDatabaseFile, SizeBytes: n})

	// In the form the key file has, so that restoring is putting a file back.
	key := hex.EncodeToString(encryptionKey) + "\n"
	n, err = storage.Write(ctx, backup.StateOwner, run.ID, stateKeyFile, strings.NewReader(key))
	if err != nil {
		return fmt.Errorf("%s: %w", stateKeyFile, err)
	}
	run.Volumes = append(run.Volumes, api.BackupVolume{Volume: stateKeyFile, SizeBytes: n})
	return nil
}

func (e *Engine) finishStateBackup(run store.BackupRun, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	storage := e.opts.Backups.Storage

	if failure == nil {
		run.Status, run.Destinations, run.Encrypted = api.BackupSucceeded, storage.Destinations(), true
	} else {
		run.Status, run.Volumes, run.Error = api.BackupFailed, []api.BackupVolume{}, failure.Error()
		if e.baseCtx.Err() != nil {
			run.Error = "the agent was shut down while the backup was being taken"
		}
		if e.backups.isPrepared() {
			if err := e.removeBackupFiles(ctx, run.Application, run.ID); err != nil {
				e.log.Warn("could not remove the files of a failed backup of the agent's state", "backup", run.ID, "error", err)
			}
		}
	}
	if err := e.store.FinishBackupRun(ctx, run, time.Now()); err != nil {
		e.log.Error("could not record the outcome of a backup of the agent's state", "backup", run.ID, "error", err)
	}
	switch {
	case failure == nil:
		e.log.Info("agent state backed up", "backup", run.ID, "bytes", backupSize(run), "destinations", strings.Join(run.Destinations, ","))
	case e.baseCtx.Err() == nil:
		e.log.Error("the backup of the agent's state failed", "backup", run.ID, "error", run.Error)
		if run.Trigger == api.BackupTriggerSchedule {
			e.notify(ctx, notify.Event{
				Kind:    notify.BackupFailed,
				Message: fmt.Sprintf("The backup of the agent's own state failed: %s. The encryption key exists only on the server until one succeeds; see: shipwick doctor", run.Error),
			})
		}
	}
	e.pruneBackups(ctx, backup.StateOwner, stateBackupsKept)
}

// StateBackups lists the backups of the agent's state, newest first.
func (e *Engine) StateBackups(ctx context.Context, limit int) ([]api.BackupRun, error) {
	return e.listBackups(ctx, backup.StateOwner, limit)
}

// StateBackup returns one backup of the agent's state.
func (e *Engine) StateBackup(ctx context.Context, id int64) (api.BackupRun, error) {
	run, err := e.store.GetBackupRun(ctx, backup.StateOwner, id)
	if err != nil {
		return api.BackupRun{}, err
	}
	return backupView(run), nil
}

// BackupStatus says where backups go and how the agent's own state is doing,
// for the server view.
func (e *Engine) BackupStatus(ctx context.Context) *api.BackupStatus {
	storage := e.opts.Backups.Storage
	if storage == nil {
		return &api.BackupStatus{Destination: api.BackupDestinationNone, StateError: ErrBackupsDisabled.Error()}
	}
	destinations := storage.Destinations()
	status := &api.BackupStatus{Destination: destinations[len(destinations)-1], Encrypted: storage.Encrypted()}
	if !storage.Encrypted() {
		status.StateError = ErrStateNotEncrypted.Error()
		return status
	}
	if last, err := e.store.LastBackupRun(ctx, backup.StateOwner, api.BackupSucceeded); err == nil {
		status.StateLastAt = last.CompletedAt
	}
	if latest, err := e.store.ListBackupRuns(ctx, backup.StateOwner, 1); err == nil && len(latest) > 0 && latest[0].Status == api.BackupFailed {
		status.StateError = latest[0].Error
	}
	return status
}

// removeBackupFiles removes a run's files from every destination — once the
// destinations are known to be this installation's. Until then a run id says
// nothing about whose files sit under it: in a bucket that belongs to another
// installation, run 1 is that installation's run 1.
func (e *Engine) removeBackupFiles(ctx context.Context, owner string, run int64) error {
	if err := e.prepareBackups(ctx); err != nil {
		return err
	}
	return e.opts.Backups.Storage.Remove(ctx, owner, run)
}
