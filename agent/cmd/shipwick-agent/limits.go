package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// warnUnenforcedLimits says at start what the daemon will not hold containers
// to. Docker says nothing when such a container is created; the deployments
// that ask for a limit and `shipwick doctor` repeat it.
func warnUnenforcedLimits(ctx context.Context, rt *docker.Runtime, log *slog.Logger) {
	info, err := rt.Info(ctx)
	if err != nil {
		return
	}
	if info.Rootless {
		log.Info("the Docker daemon is rootless: it and every container run as an ordinary user of this server")
	}
	if len(info.Unenforced) > 0 {
		log.Warn("the Docker daemon does not enforce these limits: resources in deploy.yaml is accepted and applied to no container, and without any cgroup the usage it reports per container is not the container's",
			"limits", strings.Join(info.Unenforced, ", "), "rootless", info.Rootless)
	}
}
