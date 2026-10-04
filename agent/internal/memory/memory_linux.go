//go:build linux

package memory

import "os"

// Swap reports how much swap the server has. /proc/meminfo is the host's in
// a container too. ok is false when the kernel would not say.
func Swap() (bytes int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return swapTotal(f)
}
