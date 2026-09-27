package docker

import (
	"context"
	"fmt"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// configureJob turns the container into a one-off job container: its own
// name, and the job labels in place of the replica index, so that everything
// that manages replicas can tell it apart (see Container.Job). It returns the
// container's name.
func configureJob(spec ContainerSpec, cfg *container.Config) string {
	delete(cfg.Labels, LabelReplica)
	cfg.Labels[LabelJob] = spec.Job.Name
	cfg.Labels[LabelRun] = strconv.FormatInt(spec.Job.RunID, 10)
	return JobContainerName(spec.App, spec.Job.Name, spec.Job.RunID)
}

// WaitContainer blocks until the container stops and returns its exit code.
// It returns when ctx ends, too, with ctx's error: the caller decides what to
// do with a container that is still running.
func (r *Runtime) WaitContainer(ctx context.Context, id string) (int, error) {
	res := r.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case out := <-res.Result:
		if out.Error != nil {
			return int(out.StatusCode), fmt.Errorf("wait for container: %s", out.Error.Message)
		}
		return int(out.StatusCode), nil
	case err := <-res.Error:
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("wait for container: %w", wrapNotFound(err))
	}
}
