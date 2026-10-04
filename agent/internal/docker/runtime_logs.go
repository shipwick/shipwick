package docker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// LogWindow is the part of a container's log that ReadLogs returns. The
// daemon takes the last Tail lines first and applies Since and Until to
// those: a window that lies before the last Tail lines comes back empty.
type LogWindow struct {
	// Since and Until bound the lines by their time, both inclusive; the
	// zero time leaves that side open.
	Since, Until time.Time
	// Tail is how many lines from the end are looked at; zero: all of them.
	Tail int
}

// ReadLogs calls emit for every line of the window, in the order the
// container wrote them, and returns when the log has been read: it does not
// follow. Unlike Logs it holds one line at a time, so the caller decides how
// much is kept of a log of any size. A container whose logging driver keeps
// nothing the daemon can read (`none`, or a remote driver with its cache
// disabled) is an error.
func (r *Runtime) ReadLogs(ctx context.Context, id string, w LogWindow, emit func(LogEntry)) error {
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: "all"}
	if w.Tail > 0 {
		opts.Tail = strconv.Itoa(w.Tail)
	}
	if !w.Since.IsZero() {
		opts.Since = sinceParam(w.Since)
	}
	if !w.Until.IsZero() {
		opts.Until = sinceParam(w.Until)
	}
	rc, err := r.cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return fmt.Errorf("read logs: %w", wrapNotFound(err))
	}
	defer rc.Close()

	stdout := &lineWriter{stream: "stdout", emit: emit}
	stderr := &lineWriter{stream: "stderr", emit: emit}
	_, err = stdcopy.StdCopy(stdout, stderr, rc)
	stdout.flush()
	stderr.flush()
	if err != nil {
		return fmt.Errorf("read logs: %w", err)
	}
	return nil
}

// finishedAt parses State.FinishedAt, which is the zero time for a container
// that never stopped.
func finishedAt(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return nil
	}
	t = t.UTC()
	return &t
}
