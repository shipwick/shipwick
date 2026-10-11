//go:build !linux

package kernel

// NeighbourTable reports how many neighbours the kernel keeps. Off Linux,
// where only the development build runs, it is unknown.
func NeighbourTable() (int, bool) { return 0, false }
