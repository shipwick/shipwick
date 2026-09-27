package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/spec"
)

// probeReplica runs the deployment's health check once against one of its
// replicas. An HTTP failure comes back bare ("HTTP 503", "connection
// refused"), as it always has; the other kinds say what was tried, because a
// refused connection means nothing without the port it was refused on.
func (e *Engine) probeReplica(ctx context.Context, d *store.Deployment, c docker.Container) error {
	h := d.Spec.Health
	timeout := h.Timeout.Std()
	switch h.Kind() {
	case spec.HealthTCP:
		if err := e.opts.ProbeTCP(ctx, c.IP, h.TCP, timeout); err != nil {
			return fmt.Errorf("TCP connect to :%d: %w", h.TCP, err)
		}
		return nil
	case spec.HealthCommand:
		return e.probeCommand(ctx, c.ID, h.Command, timeout)
	}
	return e.opts.Probe(ctx, c.IP, d.Spec.Port, h.Path, timeout)
}

// probeCommand runs the check inside the replica; exit 0 is healthy. A
// failure carries the exit code and the last line the command printed — the
// line a tool like pg_isready puts its verdict on — and never more: output is
// unbounded, an event is not.
func (e *Engine) probeCommand(ctx context.Context, containerID string, cmd []string, timeout time.Duration) error {
	code, output, err := e.rt.Exec(ctx, containerID, cmd, timeout)
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf("command did not finish within %s", timeout)
	case err != nil:
		return fmt.Errorf("command could not run: %w", err)
	case code != 0:
		if line := lastLine(output); line != "" {
			return fmt.Errorf("command exited %d: %s", code, line)
		}
		return fmt.Errorf("command exited %d", code)
	}
	return nil
}

// lastLine is the last non-blank line of output, cut to fit an event.
func lastLine(output string) string {
	const maxLen = 200
	output = strings.TrimRight(output, " \t\r\n")
	if i := strings.LastIndexByte(output, '\n'); i >= 0 {
		output = output[i+1:]
	}
	output = strings.TrimSpace(output)
	if len(output) > maxLen {
		output = output[:maxLen] + "…"
	}
	return output
}

// withCheck names the check behind an HTTP probe's bare error, for the one
// message that stands on its own: why the deployment failed.
func withCheck(h *spec.Health, port int, err error) error {
	if h.Kind() == spec.HealthHTTP {
		return fmt.Errorf("GET %s on port %d: %w", h.Path, port, err)
	}
	return err
}
