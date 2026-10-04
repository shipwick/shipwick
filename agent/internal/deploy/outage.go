package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// What the agent stands on can fail under it: the Docker daemon may stop
// answering, and the disk may fill up. Neither is the agent's to repair. What
// it owes the operator is to do no harm meanwhile, to say what is wrong in
// the words of the cause, and to go on by itself when the cause is gone.

// dockerAlertAfter is how long the daemon must have been silent before the
// alert is raised. Restarting Docker — an upgrade of the package — takes a few
// seconds, and what it does to the applications is reported for each of them.
const dockerAlertAfter = 30 * time.Second

// dockerOutage is the supervisor's memory of a daemon that does not answer.
// The zero value is a daemon that does.
type dockerOutage struct {
	mu sync.Mutex
	// since is the time of the tick that first went unanswered; zero: the
	// daemon answers. asked is when that tick asked, on the wall clock.
	since time.Time
	asked time.Time
}

// lasted is how long the outage has lasted at the tick of time now. A
// question that goes unanswered takes its whole bound, and the ticks that
// fall due meanwhile are dropped or wait their turn: the time a tick carries
// falls behind by a bound per tick. The wall clock says how long it has been;
// the ticks' own times say so where the clock is the test's.
func (o *dockerOutage) lasted(now time.Time) time.Duration {
	return max(now.Sub(o.since), time.Since(o.asked)).Round(time.Second)
}

// dockerAnswers asks the daemon one question before a tick does anything
// else, and reports whether it was answered. A tick against a daemon that is
// silent would take each application's lock in turn and hold it for as long
// as the question hangs, so that the operator who tries anything is told
// "another operation is in progress" by the one component that knows better.
// Instead the tick is skipped, the outage is said once in the log and, when
// it lasts, raised as an alert; the first answer clears it.
func (e *Engine) dockerAnswers(ctx context.Context, now time.Time) bool {
	asked := time.Now()
	_, err := e.rt.ListContainers(ctx, "")
	if ctx.Err() != nil {
		return false // shutting down: the silence is the agent's own
	}
	key := alertKey{kind: api.AlertDocker}

	e.outage.mu.Lock()
	began := err != nil && e.outage.since.IsZero()
	if began {
		e.outage.since, e.outage.asked = now, asked
	}
	ongoing := !e.outage.since.IsZero()
	lasted := e.outage.lasted(now)
	if err == nil {
		e.outage.since, e.outage.asked = time.Time{}, time.Time{}
	}
	e.outage.mu.Unlock()

	if err == nil {
		if ongoing {
			e.log.Info("Docker answers again; applications are supervised again", "after", shortDuration(lasted))
			e.clearAlert(ctx, nil, key, fmt.Sprintf("Docker answers again after %s; applications are supervised again", shortDuration(lasted)), now, false)
		}
		return true
	}
	if began {
		e.log.Error("Docker does not answer; applications are not supervised until it does", "error", err)
	}
	if lasted >= dockerAlertAfter {
		e.raiseAlert(ctx, nil, key, api.SeverityCritical,
			"Docker does not answer. Applications that are running keep running, but nothing is restarted, deployed or routed until it does. On the server: systemctl status docker", now)
	}
	return false
}

// IsRuntimeUnavailable reports whether err is the Docker daemon not
// answering, whatever the engine was doing when it found out.
func IsRuntimeUnavailable(err error) bool {
	return docker.IsUnavailable(err)
}

// RuntimeUnavailableMessage is what a request is answered with when Docker
// does not answer: the cause, and where to look.
func RuntimeUnavailableMessage(err error) string {
	cause := rootCause(err).Error()
	if _, after, found := strings.Cut(err.Error(), docker.ErrUnavailable.Error()+": "); found {
		cause = after
	}
	return "Docker does not answer on the server. Applications that are running keep running; look at the daemon there with: systemctl status docker. The cause: " + cause
}

// rootCause is the innermost error of a chain: the daemon's or the client's
// own words, without the engine's account of what it was doing.
func rootCause(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		err = inner
	}
}

// IsDiskFull reports whether err is a disk with no room: the agent's
// database refusing a write, a file in the data directory that cannot grow,
// or the daemon saying so about its own disk, in the kernel's words, which is
// all it passes on.
func IsDiskFull(err error) bool {
	return err != nil && (store.IsFull(err) || errors.Is(err, syscall.ENOSPC) || strings.Contains(err.Error(), "no space left on device"))
}

// DiskFullMessage is what a request is answered with when the disk is full.
func DiskFullMessage(err error) string {
	return fmt.Sprintf("The server's disk is full (%v). Nothing was changed. Free space on the server — docker system df shows what takes it, docker image prune -a removes images nothing uses — and try again", err)
}

// writeFailure passes writes on and remembers the first one that failed. An
// upload is inspected while it is written to disk, through one reader; this
// tells a disk that would not take the bytes from bytes that are not what
// they should be.
type writeFailure struct {
	w   io.Writer
	err error
}

func (f *writeFailure) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil && f.err == nil {
		f.err = err
	}
	return n, err
}
