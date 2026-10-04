// Package memory reads what the kernel says about the server's memory.
package memory

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// swapTotal finds SwapTotal in the text of /proc/meminfo, in bytes. ok is
// false when the line is missing or is not what the kernel writes.
func swapTotal(meminfo io.Reader) (bytes int64, ok bool) {
	lines := bufio.NewScanner(meminfo)
	for lines.Scan() {
		rest, found := strings.CutPrefix(lines.Text(), "SwapTotal:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
