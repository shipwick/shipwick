package dockertest

import (
	"context"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// LogFollow is one FollowLogsSince call.
type LogFollow struct {
	Container string // its name
	Since     time.Time
}

// FollowLogsSince emits what WriteLogs gave the container from `since` on,
// cut the way the daemon cuts it — lines at or after the time — and then,
// like a real follow, stays open until the caller cancels. A container that
// was given nothing has the one line Logs makes up, stamped now.
func (f *Fake) FollowLogsSince(ctx context.Context, id string, since time.Time, emit func(docker.LogEntry)) error {
	f.mu.Lock()
	c, ok := f.containers[id]
	if !ok {
		f.mu.Unlock()
		return docker.ErrNotFound
	}
	l := f.logState()
	l.follows = append(l.follows, LogFollow{Container: c.Name, Since: since})
	lines, written := f.written(id, 0)
	if !written {
		lines = []docker.LogEntry{{Stream: "stdout", Time: time.Now().UTC(), Message: "log line from " + c.Name}}
	}
	f.mu.Unlock()

	for _, line := range lines {
		if !line.Time.Before(since) {
			emit(line)
		}
	}
	<-ctx.Done()
	return nil
}

// LogFollows lists the FollowLogsSince calls so far, in order.
func (f *Fake) LogFollows() []LogFollow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LogFollow(nil), f.logState().follows...)
}
