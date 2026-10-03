// Package cloudflare knows the addresses Cloudflare's proxy answers from. A
// hostname behind the orange cloud resolves to one of them and never to the
// server; the agent and the CLI both have to tell that case from a record that
// points at the wrong machine, and say the same thing about it.
package cloudflare

import "net/netip"

// Updated is the day the list was taken from https://www.cloudflare.com/ips-v4
// and /ips-v6. It changes rarely.
const Updated = "2026-10-03"

var ranges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

var prefixes = func() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, netip.MustParsePrefix(r))
	}
	return out
}()

// Ranges returns the list in CIDR notation, IPv4 first.
func Ranges() []string {
	return append([]string(nil), ranges...)
}

// Contains reports whether ip is one of Cloudflare's addresses. Anything that
// is not an address is not.
func Contains(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Proxied reports whether every one of addrs is Cloudflare's: what a lookup
// of a proxied record answers. One address elsewhere means a record of the
// operator's own.
func Proxied(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !Contains(a) {
			return false
		}
	}
	return true
}
