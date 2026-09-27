package dockertest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// ExecResult is what a container answers to Exec. Err, when set, stands for a
// command that could not be run at all.
type ExecResult struct {
	ExitCode int
	Output   string
	Err      error
}

// ExecCall records one Exec, by container name.
type ExecCall struct {
	Container string
	Cmd       []string
	Timeout   time.Duration
}

// SetExecResult changes what Exec answers for key — a container name or a
// command joined with spaces — while the engine runs.
func (f *Fake) SetExecResult(key string, r ExecResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ExecResults[key] = r
}

// ExecCalls returns every Exec so far, oldest first.
func (f *Fake) ExecCalls() []ExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExecCall(nil), f.execCalls...)
}

// Exec runs a command inside a running container.
func (f *Fake) Exec(ctx context.Context, id string, cmd []string, timeout time.Duration) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return 0, "", docker.ErrNotFound
	}
	f.execCalls = append(f.execCalls, ExecCall{Container: c.Name, Cmd: append([]string(nil), cmd...), Timeout: timeout})
	if !c.Running {
		return 0, "", fmt.Errorf("container %s is not running", c.Name)
	}
	r, ok := f.ExecResults[c.Name]
	if !ok {
		r = f.ExecResults[strings.Join(cmd, " ")]
	}
	if r.Err != nil {
		return 0, "", r.Err
	}
	return r.ExitCode, r.Output, nil
}
