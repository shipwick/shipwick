package docker

import "github.com/moby/moby/api/types/container"

// logCaps bound the drivers that write to the server's disk, so that a chatty
// application cannot fill it. They apply unless deploy.yaml sets its own.
var logCaps = map[string]string{"max-size": "10m", "max-file": "3"}

// configureLogging replaces the default log driver with the one the
// application asked for.
func configureLogging(spec ContainerSpec, host *container.HostConfig) {
	if spec.LogDriver == "" {
		return
	}
	// A copy: the caps must not leak back into the stored spec.
	opts := make(map[string]string, len(spec.LogOptions)+len(logCaps))
	for k, v := range spec.LogOptions {
		opts[k] = v
	}
	if spec.LogDriver == "json-file" || spec.LogDriver == "local" {
		for k, v := range logCaps {
			if _, ok := opts[k]; !ok {
				opts[k] = v
			}
		}
	}
	host.LogConfig = container.LogConfig{Type: spec.LogDriver, Config: opts}
}
