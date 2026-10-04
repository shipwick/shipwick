package docker

import (
	"slices"

	"github.com/moby/moby/api/types/system"
)

// The limits of deploy.yaml's resources block, as Info.Unenforced names them.
const (
	LimitMemory = "memory"
	LimitCPU    = "cpu"
)

// unenforcedLimits names the limits this daemon takes with a container and
// does not apply. A daemon without the cgroup controller for a limit — a
// rootless one whose user was delegated none, a kernel started without the
// memory controller — creates the container all the same, without a warning,
// and reports the limit back when asked about the container; only its account
// of itself says that nothing holds the container to it.
func unenforcedLimits(info system.Info) []string {
	var limits []string
	if !info.MemoryLimit {
		limits = append(limits, LimitMemory)
	}
	if !info.CPUCfsQuota || !info.CPUCfsPeriod {
		limits = append(limits, LimitCPU)
	}
	return limits
}

// rootless reports whether the daemon runs as an ordinary user of the server.
func rootless(info system.Info) bool {
	return slices.Contains(info.SecurityOptions, "name=rootless")
}
