//go:build !linux

package memory

// Swap reports how much swap the server has. Off Linux, where only the
// development build runs, it is unknown.
func Swap() (int64, bool) { return 0, false }
