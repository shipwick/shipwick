package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The proxy is the `caddy` service of the compose project the agent runs in;
// Compose labels every container it creates with both. The project is
// `shipwick` in the files Shipwick ships, and whatever `-p` or
// COMPOSE_PROJECT_NAME made it in an installation that was started otherwise:
// the agent reads it off its own container rather than assume it.
const (
	defaultComposeProject = "shipwick"
	composeProxy          = "caddy"
	composeProjectLabel   = "com.docker.compose.project"
	composeServiceLabel   = "com.docker.compose.service"
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
	filters.Add("label", composeProjectLabel+"="+r.composeProject(ctx))
	filters.Add("label", composeServiceLabel+"="+composeProxy)
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

// ownProject remembers the compose project of the agent's own container. A
// container's labels never change, so one successful look is enough.
type ownProject struct {
	mu    sync.Mutex
	name  string
	known bool
}

// composeProject is the compose project the agent's own container belongs to.
// An agent that is not a container of this daemon — a plain binary, a test —
// or one that Compose did not start gets the name the shipped files pin. A
// daemon that cannot be asked right now gets the same answer, and is asked
// again the next time.
func (r *Runtime) composeProject(ctx context.Context) string {
	r.project.mu.Lock()
	defer r.project.mu.Unlock()
	if r.project.known {
		return r.project.name
	}
	labels, err := r.ownLabels(ctx)
	if err != nil {
		return defaultComposeProject
	}
	r.project.name, r.project.known = projectOf(labels), true
	return r.project.name
}

// projectOf reads the compose project off a container's labels.
func projectOf(labels map[string]string) string {
	if name := labels[composeProjectLabel]; name != "" {
		return name
	}
	return defaultComposeProject
}

// ownLabels are the labels of the container the agent runs in; none, without
// error, when it does not run in one this daemon knows.
func (r *Runtime) ownLabels(ctx context.Context) (map[string]string, error) {
	// Inside a container the hostname is, unless overridden, the short
	// container ID.
	hostname, err := os.Hostname()
	if err != nil || len(hostname) < 12 {
		return nil, nil
	}
	res, err := r.cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect own container: %w", err)
	}
	// A name lookup can match by coincidence; an ID prefix cannot.
	if !strings.HasPrefix(res.Container.ID, hostname) || res.Container.Config == nil {
		return nil, nil
	}
	return res.Container.Config.Labels, nil
}
