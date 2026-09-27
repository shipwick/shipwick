package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The compose project the agent ships in pins its name, and the proxy is its
// `caddy` service; Compose labels every container it creates with both.
const (
	composeProject = "shipwick"
	composeProxy   = "caddy"
)

// ErrNoProxyContainer means the reverse proxy is not a container this daemon
// runs — a host process, or another compose project — and the agent has no
// way to put files in front of it.
var ErrNoProxyContainer = errors.New("the proxy runs outside Docker; static applications need the compose setup")

// ProxyContainer finds the reverse proxy's container: the `caddy` service of
// the compose project the agent runs in. It must be running: the files of a
// static application are put there and checked with commands run inside it.
func (r *Runtime) ProxyContainer(ctx context.Context) (string, error) {
	filters := client.Filters{}
	filters.Add("label", "com.docker.compose.project="+composeProject)
	filters.Add("label", "com.docker.compose.service="+composeProxy)
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return "", fmt.Errorf("find the proxy container: %w", err)
	}
	for _, item := range res.Items {
		if item.State == container.StateRunning {
			return item.ID, nil
		}
	}
	if len(res.Items) > 0 {
		return "", errors.New("the proxy container is not running")
	}
	return "", ErrNoProxyContainer
}
