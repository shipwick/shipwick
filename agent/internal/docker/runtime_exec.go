package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// execOutputBytes is how much of a command's output Exec keeps: the end of
// it, where the reason for a failure tends to be.
const execOutputBytes = 4096

// Exec runs cmd inside a running container and returns its exit code together
// with the tail of what it printed, bounded by timeout. cmd is an argv and is
// handed to the daemon as it is; no shell is involved.
func (r *Runtime) Exec(ctx context.Context, id string, cmd []string, timeout time.Duration) (exitCode int, output string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	created, err := r.cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		// A conflict is the daemon's word for a container that exists but is
		// not running, which deserves a plainer one.
		if cerrdefs.IsConflict(err) {
			return 0, "", errors.New("container is not running")
		}
		return 0, "", fmt.Errorf("exec: %w", wrapNotFound(err))
	}

	// Attaching starts the process; the stream ends when it exits.
	attached, err := r.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("exec: %w", err)
	}
	defer attached.Close()
	// The hijacked connection does not die with its context. Closing it is
	// what ends the read when the command hangs past its timeout; the process
	// itself is left to the container, since Docker has no way to kill an
	// exec.
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()

	// Without a TTY, stdout and stderr arrive multiplexed. Both land in one
	// bounded buffer: a check may print for ever, a message may not.
	tail := &tailBuffer{limit: execOutputBytes}
	if _, err := stdcopy.StdCopy(tail, tail, attached.Reader); err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		return 0, "", fmt.Errorf("exec: read output: %w", err)
	}
	info, err := r.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("exec: %w", err)
	}
	return info.ExitCode, tail.String(), nil
}

// tailBuffer is an io.Writer that keeps only the last limit bytes written.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= t.limit {
		t.buf = append(t.buf[:0], p[len(p)-t.limit:]...)
		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - t.limit; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
