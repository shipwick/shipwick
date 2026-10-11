// Package kernel reads what the kernel says about the limits a server full
// of containers runs into.
package kernel

import (
	"io"
	"strconv"
	"strings"
)

// neighbourTableFile holds net.ipv4.neigh.default.gc_thresh3: the most
// neighbours the kernel keeps, 1024 in a stock kernel. Every address a
// container has on one of the server's networks is one of them, and the
// proxy's and the agent's as well; when the table is full, containers stop
// reaching each other.
//
// The kernel registers the `default` entry in the initial network namespace
// only (neigh_sysctl_register in net/core/neighbour.c, called from arp_init
// without a device): a container has a namespace of its own, and the file is
// not there. So an agent that is a process of the host reads it, and an
// agent in a container does not.
const neighbourTableFile = "/proc/sys/net/ipv4/neigh/default/gc_thresh3"

// parseEntries reads the one number such a file holds. ok is false when the
// kernel wrote something else.
func parseEntries(r io.Reader) (entries int, ok bool) {
	text, err := io.ReadAll(io.LimitReader(r, 64))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
