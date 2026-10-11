//go:build linux

package kernel

import "os"

// NeighbourTable reports how many neighbours the kernel keeps
// (net.ipv4.neigh.default.gc_thresh3). ok is false where the file is not
// there to read: in a container, which has a network namespace of its own.
func NeighbourTable() (entries int, ok bool) {
	f, err := os.Open(neighbourTableFile)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return parseEntries(f)
}
