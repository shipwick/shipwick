package docker

import (
	"slices"
	"testing"

	"github.com/moby/moby/api/types/system"
)

func TestADaemonWithoutTheControllersEnforcesNoLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		info system.Info
		want []string
	}{
		{"every controller", system.Info{MemoryLimit: true, CPUCfsPeriod: true, CPUCfsQuota: true}, nil},
		// What a rootless daemon without cgroup delegation reports.
		{"none", system.Info{}, []string{LimitMemory, LimitCPU}},
		{"no memory controller", system.Info{CPUCfsPeriod: true, CPUCfsQuota: true}, []string{LimitMemory}},
		{"no CPU quota", system.Info{MemoryLimit: true, CPUCfsPeriod: true}, []string{LimitCPU}},
	} {
		if got := unenforcedLimits(tc.info); !slices.Equal(got, tc.want) {
			t.Errorf("%s: unenforced %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestARootlessDaemonIsKnownByItsSecurityOptions(t *testing.T) {
	if !rootless(system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless", "name=cgroupns"}}) {
		t.Error("a daemon that lists name=rootless is rootless")
	}
	if rootless(system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=cgroupns"}}) {
		t.Error("a daemon that does not list it is not")
	}
}
