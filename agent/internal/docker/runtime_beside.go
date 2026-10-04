package docker

import "github.com/moby/moby/api/types/container"

// BesideSpec describes a container that runs a command next to a running
// container rather than inside it. The daemon can stop and remove a
// container; a command it started inside one (Exec) it can only wait for.
type BesideSpec struct {
	// Container is the running container whose network namespace is shared:
	// localhost in the new container is localhost in that one, and it has no
	// address, name or network of its own.
	Container string
}

// configureBeside makes the container share the other's network namespace
// and run its command as it is, without the image's entrypoint: the command
// stands for one that Exec would have started, and Exec knows no entrypoint.
func configureBeside(spec ContainerSpec, cfg *container.Config, host *container.HostConfig) {
	if spec.Beside == nil {
		return
	}
	host.NetworkMode = container.NetworkMode("container:" + spec.Beside.Container)
	// Empty, not nil: nil keeps the image's.
	cfg.Entrypoint = []string{}
}
