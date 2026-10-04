package deploy

import (
	"context"
	"slices"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// A daemon without the cgroup controller for a limit takes the limit with a
// container, reports it back, and holds the container to nothing: rootless
// Docker whose user was delegated no controllers, a kernel started without
// the memory controller. Docker does not say so when the container is
// created, so the agent does: on the deployment that asks for the limit, in
// the server's status, and by not raising an alert about a limit that does
// not exist.

// dockerStatus is the daemon's account of itself, for GET /server.
func dockerStatus(info docker.Info) *api.DockerStatus {
	limits := info.Unenforced
	if limits == nil {
		limits = []string{}
	}
	return &api.DockerStatus{Rootless: info.Rootless, UnenforcedLimits: limits}
}

// unenforcedLimits asks the daemon which limits it does not apply. A daemon
// that cannot be asked right now is taken to apply them all: saying nothing
// is what happened before anyone asked.
func (e *Engine) unenforcedLimits(ctx context.Context) []string {
	info, err := e.rt.Info(ctx)
	if err != nil {
		return nil
	}
	return info.Unenforced
}

func (e *Engine) memoryUnenforced(ctx context.Context) bool {
	return slices.Contains(e.unenforcedLimits(ctx), docker.LimitMemory)
}

// warnUnenforced says on a deployment which of the limits it asks for its
// replicas will run without.
func (e *Engine) warnUnenforced(ctx context.Context, d *store.Deployment) {
	if d.Spec.Resources.MemoryBytes <= 0 && d.Spec.Resources.CPU <= 0 {
		return
	}
	unenforced := e.unenforcedLimits(ctx)
	var keys []string
	if d.Spec.Resources.MemoryBytes > 0 && slices.Contains(unenforced, docker.LimitMemory) {
		keys = append(keys, "resources.memory")
	}
	if d.Spec.Resources.CPU > 0 && slices.Contains(unenforced, docker.LimitCPU) {
		keys = append(keys, "resources.cpu")
	}
	if len(keys) == 0 {
		return
	}
	e.event(ctx, d, api.LevelWarn, api.EventStep,
		"Docker on this server does not enforce "+strings.Join(keys, " and ")+": the replicas run without a limit. shipwick doctor says what the server lacks")
}
