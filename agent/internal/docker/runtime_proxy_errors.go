package docker

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// FollowErrors streams the lines a container writes to standard error from
// `since` on, until it stops. The proxy's own messages are read with it —
// why a certificate did not come (deploy/certfailures.go) — as its access
// log is read from standard output with FollowOutput: the same call to the
// Engine API, asked for the other stream.
func (r *Runtime) FollowErrors(ctx context.Context, id string, since time.Time, emit func(line []byte)) error {
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStderr: true,
		Follow:     true,
		Since:      sinceParam(since),
	})
	if err != nil {
		return fmt.Errorf("follow errors: %w", wrapNotFound(err))
	}
	defer rc.Close()

	out := &rawLineWriter{emit: emit}
	_, err = stdcopy.StdCopy(io.Discard, out, rc)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("follow errors: %w", err)
	}
	return nil
}
