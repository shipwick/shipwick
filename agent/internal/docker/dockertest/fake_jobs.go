package dockertest

import (
	"context"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// WaitContainer blocks until the container stops and returns its exit code.
//
// A job container that is still running finishes right here with its
// JobExits code — tests must not have to wait for anything — unless HoldJobs
// is set, in which case it runs until ReleaseJob, Crash, StopContainer or
// RemoveContainer, or until ctx ends.
func (f *Fake) WaitContainer(ctx context.Context, id string) (int, error) {
	f.mu.Lock()
	c, ok := f.containers[id]
	if !ok {
		f.mu.Unlock()
		return 0, docker.ErrNotFound
	}
	if !c.Running {
		f.mu.Unlock()
		return c.ExitCode, nil
	}
	if c.Job != "" && !f.HoldJobs {
		c.Running, c.State, c.ExitCode, c.IP = false, "exited", f.JobExits[c.Job], ""
		f.finish(c)
		f.signalStopped(id)
		f.mu.Unlock()
		return c.ExitCode, nil
	}
	done := f.stoppedChan(id)
	f.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok = f.containers[id]
	if !ok {
		return 0, docker.ErrNotFound
	}
	return c.ExitCode, nil
}

// ReleaseJob ends the running container of a held job with the given exit
// code. It reports whether there was one.
func (f *Fake) ReleaseJob(job string, exitCode int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, c := range f.containers {
		if c.Job == job && c.Running {
			c.Running, c.State, c.ExitCode, c.IP = false, "exited", exitCode, ""
			f.finish(c)
			f.signalStopped(id)
			return true
		}
	}
	return false
}

// JobContainers lists the containers of one-off jobs, running or not.
func (f *Fake) JobContainers() []docker.Container {
	var out []docker.Container
	for _, c := range f.Containers() {
		if c.Job != "" {
			out = append(out, c)
		}
	}
	return out
}

// stoppedChan returns the channel closed when the container stops. The caller
// holds f.mu.
func (f *Fake) stoppedChan(id string) chan struct{} {
	ch, ok := f.stopped[id]
	if !ok {
		ch = make(chan struct{})
		f.stopped[id] = ch
	}
	return ch
}

// signalStopped wakes every WaitContainer on the container. The caller holds
// f.mu.
func (f *Fake) signalStopped(id string) {
	if ch, ok := f.stopped[id]; ok {
		close(ch)
		delete(f.stopped, id)
	}
}
