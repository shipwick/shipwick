package dockertest

import (
	"context"
	"errors"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// What containers print. A container has the log WriteLogs gave it, kept
// until the container is removed, as the daemon keeps it; one that was given
// nothing answers Logs with a single line of its own, which is all the tests
// older than this file expect, and ReadLogs with none.

// logState is the part of the fake's state that belongs to logs, guarded by
// the fake's mutex.
type logState struct {
	lines   map[string][]docker.LogEntry // by container ID
	last    time.Time                    // the latest time handed out by stamp
	reads   []LogRead
	onStart map[string][]string // by image: see PrintOnStart
	err     error
}

// LogRead is one ReadLogs call.
type LogRead struct {
	Container string // its name
	Window    docker.LogWindow
}

// ErrNoLogs is what ReadLogs answers for a container whose logging driver
// keeps nothing to read.
var ErrNoLogs = errors.New("configured logging driver does not support reading")

func (f *Fake) logState() *logState {
	if f.logs == nil {
		f.logs = &logState{lines: map[string][]docker.LogEntry{}}
	}
	return f.logs
}

// stamp is the time of the next thing that happens to a container: the
// clock's, and later than everything stamped before, so that lines and stops
// keep their order however fast a test goes. The caller holds f.mu.
func (f *Fake) stamp() time.Time {
	l := f.logState()
	t := f.now().UTC()
	if !t.After(l.last) {
		t = l.last.Add(time.Microsecond)
	}
	l.last = t
	return t
}

// finish records that the container stopped now. The caller holds f.mu.
func (f *Fake) finish(c *docker.Container) {
	t := f.stamp()
	c.FinishedAt = &t
}

// WriteLogs makes the container print lines on its standard output, each
// stamped a moment after the one before.
func (f *Fake) WriteLogs(id string, lines ...string) {
	f.write(id, "stdout", lines)
}

// WriteErrorLogs is WriteLogs for the standard error.
func (f *Fake) WriteErrorLogs(id string, lines ...string) {
	f.write(id, "stderr", lines)
}

func (f *Fake) write(id, stream string, lines []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.logState()
	for _, line := range lines {
		l.lines[id] = append(l.lines[id], docker.LogEntry{Stream: stream, Time: f.stamp(), Message: line})
	}
}

// FailLogReads makes ReadLogs fail with err from now on; nil lets it work
// again.
func (f *Fake) FailLogReads(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logState().err = err
}

// LogReads lists the ReadLogs calls so far, in order.
func (f *Fake) LogReads() []LogRead {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LogRead(nil), f.logState().reads...)
}

// ReadLogs emits the window of what WriteLogs gave the container, cut the
// way the daemon cuts it: the last Tail lines, and of those the ones between
// Since and Until.
func (f *Fake) ReadLogs(_ context.Context, id string, w docker.LogWindow, emit func(docker.LogEntry)) error {
	f.mu.Lock()
	c, ok := f.containers[id]
	if !ok {
		f.mu.Unlock()
		return docker.ErrNotFound
	}
	l := f.logState()
	l.reads = append(l.reads, LogRead{Container: c.Name, Window: w})
	if l.err != nil {
		f.mu.Unlock()
		return l.err
	}
	lines := append([]docker.LogEntry(nil), l.lines[id]...)
	f.mu.Unlock()

	if w.Tail > 0 && len(lines) > w.Tail {
		lines = lines[len(lines)-w.Tail:]
	}
	for _, line := range lines {
		if !w.Since.IsZero() && line.Time.Before(w.Since) {
			continue
		}
		if !w.Until.IsZero() && line.Time.After(w.Until) {
			continue
		}
		emit(line)
	}
	return nil
}

// written is the tail of what WriteLogs gave the container, and whether it
// was given anything. The caller holds f.mu.
func (f *Fake) written(id string, tail int) ([]docker.LogEntry, bool) {
	lines := f.logState().lines[id]
	if len(lines) == 0 {
		return nil, false
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return append([]docker.LogEntry(nil), lines...), true
}

// PrintOnStart makes every container of the image print lines each time it
// is started, before anything else happens to it: what a process says on its
// way up, or on its way down again.
func (f *Fake) PrintOnStart(image string, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.logState()
	if l.onStart == nil {
		l.onStart = map[string][]string{}
	}
	l.onStart[image] = lines
}

// started prints what the container's image prints on start. The caller
// holds f.mu.
func (f *Fake) started(c *docker.Container) {
	l := f.logState()
	for _, line := range l.onStart[c.Image] {
		l.lines[c.ID] = append(l.lines[c.ID], docker.LogEntry{Stream: "stdout", Time: f.stamp(), Message: line})
	}
}

// KillForMemory simulates the kernel ending the container's process for
// exceeding its memory limit.
func (f *Fake) KillForMemory(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.Running, c.State, c.ExitCode, c.OOMKilled, c.IP = false, "exited", 137, true, ""
		f.finish(c)
		f.signalStopped(id)
	}
}
