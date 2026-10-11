package commands

import (
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
)

// stockNeighbourTable is net.ipv4.neigh.default.gc_thresh3 as the kernel
// ships it: what a server has unless somebody raised it.
const stockNeighbourTable = 1024

// wiring is what doctor says about the server's networking that no single
// request shows: a proxy that is not on all of its networks, and a kernel
// whose table of neighbours the containers are filling.
func (r *report) wiring(info api.Server) {
	if missing := info.Proxy.NetworksMissing; len(missing) > 0 {
		r.problem("The proxy is not on the %s %s: it serves some applications and not others, or cannot reach the agent. On the server: cd /opt/shipwick && docker compose up -d --force-recreate caddy",
			strings.Join(missing, " and "), pluralNoun(len(missing), "network"))
	}
	r.neighbours(info)
}

// neighbours warns when the containers approach a third of the kernel's
// table of neighbours: every container's addresses are in it, and so are the
// entries each container keeps of its peers, and when it is full containers
// stop reaching each other. From a quarter of the table on, so that the
// warning comes before the failures. An agent in a container cannot read
// the table's size; the warning then names the stock size and says so.
func (r *report) neighbours(info api.Server) {
	containers := info.Containers + 1 // the proxy
	switch {
	case info.KernelLimits != nil:
		table := info.KernelLimits.NeighbourTable
		if containers*4 >= table {
			r.hint("The server runs %d containers; the kernel's table of neighbours holds %d entries and each container takes several: raise net.ipv4.neigh.default.gc_thresh3 (and gc_thresh1/2) in /etc/sysctl.d before it fills",
				containers, table)
		}
	case containers*4 >= stockNeighbourTable:
		r.hint("The server runs %d containers; the kernel's table of neighbours holds %d entries in a stock kernel and each container takes several. The agent cannot read the setting from its container: check sysctl net.ipv4.neigh.default.gc_thresh3 on the server and raise it (and gc_thresh1/2) in /etc/sysctl.d before it fills",
			containers, stockNeighbourTable)
	}
}

// pluralNoun is the noun alone, in number: "network", "networks".
func pluralNoun(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// describeProxyNetworks is what `server status` adds to the proxy's line
// when the proxy is not on all of its networks; "" when it is.
func describeProxyNetworks(p api.ProxyStatus) string {
	if len(p.NetworksMissing) == 0 {
		return ""
	}
	return "; not on the " + strings.Join(p.NetworksMissing, " and ") + " " + pluralNoun(len(p.NetworksMissing), "network") +
		" (on the server: docker compose up -d --force-recreate caddy)"
}
