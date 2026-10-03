package docker

import "github.com/moby/moby/api/types/container"

// configureInit puts the daemon's init process in front of the container's
// own when the application asks for one. Otherwise the field stays unset, and
// with it whatever the daemon is configured to do.
func configureInit(spec ContainerSpec, host *container.HostConfig) {
	if spec.Init {
		on := true
		host.Init = &on
	}
}
