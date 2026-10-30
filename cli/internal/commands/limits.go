package commands

import (
	"slices"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// unenforced reports whether the server's Docker takes a limit and does not
// apply it. An agent older than 0.8 does not say, which is not a yes.
func unenforced(info api.Server, limit string) bool {
	return info.Docker != nil && slices.Contains(info.Docker.UnenforcedLimits, limit)
}

// limitNames is "memory", "CPU" or "memory and CPU", for a sentence.
func limitNames(limits []string) string {
	var names []string
	for _, limit := range limits {
		switch limit {
		case api.LimitMemory:
			names = append(names, "memory")
		case api.LimitCPU:
			names = append(names, "CPU")
		}
	}
	return strings.Join(names, " and ")
}

// describeDocker is the daemon's version, how the agent reaches it, and what
// about it changes what a deployment gets.
func describeDocker(info api.Server) string {
	out := info.DockerVersion
	if info.Docker == nil {
		return out
	}
	if info.Docker.Rootless {
		out += ", rootless"
	}
	switch info.Docker.Socket {
	case api.SocketProxy:
		out += ", behind the socket proxy"
	case api.SocketDirect:
		out += ", holding the Docker socket"
	}
	if names := limitNames(info.Docker.UnenforcedLimits); names != "" {
		out += "; " + names + " limits are not enforced"
	}
	return out
}

// socket says when the agent holds the Docker socket itself. The default
// installation has it reach Docker through a filtered socket, which is worth
// a line and not a problem: the package arranges it so, and so does the
// overlay for whoever chose it. An agent that does not say is not read as
// either.
func (r *report) socket(info api.Server) {
	if info.Docker == nil || info.Docker.Socket != api.SocketDirect {
		return
	}
	r.hint("The agent holds the Docker socket itself, and whoever takes it over has the whole Engine API. " +
		"The default installation puts a filtered socket between the agent and Docker: on a server the installer set up, " +
		"take compose.direct-socket.yml out of compose.override.yml and run docker compose up -d there; " +
		"an agent installed from a package holds the socket by design (handbook: The Docker socket)")
}

// limits says when `resources` in deploy.yaml is applied to no replica:
// Docker takes the limits, reports them back and applies none.
func (r *report) limits(info api.Server) {
	if info.Docker == nil {
		return
	}
	names := limitNames(info.Docker.UnenforcedLimits)
	if names == "" {
		return
	}
	keys := make([]string, 0, 2)
	for _, limit := range info.Docker.UnenforcedLimits {
		keys = append(keys, "resources."+limit)
	}
	if info.Docker.Rootless {
		r.hint("Docker on the server is rootless and does not enforce %s limits: no replica is held to %s of its deploy.yaml, and the usage shown for a replica is not its own. "+
			"Delegate the cpu and memory cgroup controllers to the user who runs Docker, on a server with systemd (handbook: Rootless Docker)",
			names, strings.Join(keys, " and "))
		return
	}
	r.hint("Docker on the server does not enforce %s limits: no replica is held to %s of its deploy.yaml. "+
		"Its kernel offers Docker no cgroup controller for them; docker info on the server says which",
		names, strings.Join(keys, " and "))
}

// notEnforced is what follows an application's limits where the server's
// Docker applies none of them, or not all: "" where every limit it has holds.
func notEnforced(r spec.Resources, unenforced []string) string {
	var mine []string
	if r.MemoryBytes > 0 && slices.Contains(unenforced, api.LimitMemory) {
		mine = append(mine, api.LimitMemory)
	}
	if r.CPU > 0 && slices.Contains(unenforced, api.LimitCPU) {
		mine = append(mine, api.LimitCPU)
	}
	if len(mine) == 0 {
		return ""
	}
	noun := " limit"
	if len(mine) > 1 {
		noun = " limits"
	}
	return "  (Docker on this server does not enforce the " + limitNames(mine) + noun + "; see shipwick doctor)"
}
