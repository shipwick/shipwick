package docker

import "github.com/moby/moby/api/types/container"

// configureProcess applies the entrypoint, command and user overrides. What
// is not set stays unset, so the image's own applies.
func configureProcess(spec ContainerSpec, cfg *container.Config) {
	if len(spec.Entrypoint) > 0 {
		cfg.Entrypoint = spec.Entrypoint
	}
	if len(spec.Command) > 0 {
		cfg.Cmd = spec.Command
	}
	if spec.User != "" {
		cfg.User = spec.User
	}
}
