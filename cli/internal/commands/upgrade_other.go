//go:build !windows

package commands

import (
	"os"
	"runtime"
)

// replaceExecutable puts staged in place of current. A rename is atomic here
// and works while the old binary is running.
func replaceExecutable(current, staged string) error {
	return os.Rename(staged, current)
}

// removeStaleExecutable has nothing to do outside Windows: the old binary is
// gone the moment it is replaced.
func removeStaleExecutable() {}

// machineArch is the architecture of the machine: outside Windows, the one
// this binary was built for.
func machineArch() string { return runtime.GOARCH }
