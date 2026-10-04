package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Where backups.before runs.
//
// Inside the replica (the default) it is a process the daemon started in a
// running container, and the daemon has no way to end such a process: at
// before_timeout the backup is given up and the command stays until it exits
// by itself. Ending it from outside takes the host's process namespace, which
// the agent does not have and will not ask for; starting it with a terminal,
// so that hanging up ends it, changes what commands do (psql waits in a
// pager).
//
// With `before_in: container` it is a container of its own, and a container
// can be stopped. It is made to see what the command usually needs of the
// replica: the same image, environment and user, the application's volumes at
// their paths, and the replica's network namespace, so that localhost is the
// replica. What it does not see is the rest of the replica's filesystem and
// its processes: a socket under /var/run or /tmp of the replica is not there,
// which is where pg_dump and mysqldump look unless they are given a host.
// That is why the replica stays the default: a command that works there
// today may not work here unchanged.

// beforeJobName labels the container. No job of a deploy.yaml can have the
// name — an underscore is not allowed in one — so the container's name
// cannot be a job's.
const beforeJobName = "backup_before"

// runBefore runs backups.before where the application asks for it.
func (e *Engine) runBefore(ctx context.Context, d store.Deployment, replica string, run *store.BackupRun, b *spec.Backups) error {
	if b.BeforeIn == spec.BeforeInContainer {
		return e.backupBeforeBeside(ctx, d, replica, run.ID, b)
	}
	return e.backupBefore(ctx, replica, b)
}

// backupBeforeBeside runs backups.before in a container next to the replica
// and ends it at backups.before_timeout. It holds the outcomes of
// backupBefore to the same words; the one that differs is the timeout, after
// which nothing of the command is left.
func (e *Engine) backupBeforeBeside(ctx context.Context, d store.Deployment, replica string, runID int64, b *spec.Backups) error {
	limit := b.BeforeLimit()
	cspec := containerFor(d)
	cspec.Command = b.Before
	cspec.Job = &docker.JobSpec{Name: beforeJobName, RunID: runID}
	cspec.Beside = &docker.BesideSpec{Container: replica}
	// An init process in front, whatever the application has: a command
	// that is its container's first process does not see SIGTERM unless
	// it handles it, and would be waited for until it is killed.
	cspec.Init = true
	// The replica writes these volumes too, and that is the point: the
	// command is the application's own way of making them fit to be copied.
	for _, v := range d.Spec.Volumes {
		cspec.Mounts = append(cspec.Mounts, docker.Mount{Volume: v.Name, Path: v.Path})
	}

	// The container goes and is stopped first whatever state ctx is in: an
	// agent that shuts down under it must not leave the command behind.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	id, name, err := e.createContainer(ctx, cspec)
	if err != nil {
		return fmt.Errorf("backups.before could not run: %w; nothing was archived", err)
	}
	defer func() {
		if err := e.rt.RemoveContainer(cleanup, id); err != nil {
			e.log.Warn("could not remove the container of backups.before", "container", name, "error", err)
		}
	}()
	if err := e.rt.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("backups.before could not run: %w; nothing was archived", err)
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, limit)
	defer cancelWait()
	code, err := e.rt.WaitContainer(waitCtx, id)
	if err != nil {
		if stopErr := e.rt.StopContainer(cleanup, id, e.opts.StopTimeout); stopErr != nil {
			e.log.Warn("could not stop the container of backups.before", "container", name, "error", stopErr)
		}
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("backups.before did not finish within %s and was stopped; nothing was archived. Give the command longer with backups.before_timeout in deploy.yaml (up to %s)",
				shortDuration(limit), shortDuration(spec.MaxBackupBeforeTimeout))
		}
		return fmt.Errorf("backups.before could not run: %w; nothing was archived", err)
	}
	if code != 0 {
		if line := e.beforeLastLine(cleanup, id); line != "" {
			return fmt.Errorf("backups.before exited %d: %s; nothing was archived", code, line)
		}
		return fmt.Errorf("backups.before exited %d; nothing was archived", code)
	}
	return nil
}

// beforeLastLine is the last line the command printed, for the error.
func (e *Engine) beforeLastLine(ctx context.Context, id string) string {
	entries, err := e.rt.Logs(ctx, id, 20)
	if err != nil {
		return ""
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		lines[i] = entry.Message
	}
	return lastLine(strings.Join(lines, "\n"))
}
