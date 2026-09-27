package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// A hostname enters the proxy configuration only once it points at this
// server. Caddy asks for a certificate the moment it is told about a hostname,
// and Let's Encrypt allows five failed authorizations per hostname per hour: a
// domain deployed before its DNS record exists would burn them within minutes
// and stay without a certificate for the rest of the hour, however quickly the
// record is fixed.
const (
	// hostnameReadyFor is how long a hostname that points here is believed
	// to, before it is looked up again.
	hostnameReadyFor = 30 * time.Second
	// hostnameRetryAfter is how soon a hostname that does not point here yet
	// is asked about again: the operator is fixing a DNS record and waiting.
	hostnameRetryAfter = 10 * time.Second
	// lookupTimeout bounds one lookup. A resolver that does not answer must
	// not hold up a rollout for long, or the supervisor's tick at all.
	lookupTimeout = 3 * time.Second
)

// hostnameCache remembers the last verdict on every hostname routing was asked
// to serve, so that a lookup happens every hostnameReadyFor or
// hostnameRetryAfter rather than on every sync.
type hostnameCache struct {
	now func() time.Time // tests set the clock

	mu      sync.Mutex
	entries map[string]hostnameVerdict
}

type hostnameVerdict struct {
	ready     bool
	why       string // for the not-ready case; a short sentence
	checkedAt time.Time
}

func newHostnameCache() *hostnameCache {
	return &hostnameCache{now: time.Now, entries: map[string]hostnameVerdict{}}
}

// stale reports whether host is due for a lookup.
func (c *hostnameCache) stale(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[host]
	if !ok {
		return true
	}
	ttl := hostnameRetryAfter
	if v.ready {
		ttl = hostnameReadyFor
	}
	return c.now().Sub(v.checkedAt) >= ttl
}

func (c *hostnameCache) record(host string, ready bool, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[host] = hostnameVerdict{ready: ready, why: why, checkedAt: c.now()}
}

func (c *hostnameCache) get(host string) (hostnameVerdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[host]
	return v, ok
}

// hostnameReady says whether host may be handed to the proxy, reading only
// what refreshHostnames found. It is safe under e.routing; the lookups are not
// done here, so no caller of it ever waits on a resolver.
func (e *Engine) hostnameReady(host string) (ready bool, why string) {
	v, ok := e.dns.get(host)
	if !ok {
		return false, "has not been checked yet"
	}
	return v.ready, v.why
}

// refreshHostnames looks up every host whose verdict is stale, one at a time,
// each bounded by lookupTimeout. It is called without e.routing held: a
// resolver that does not answer must not stop replicas from being renamed.
func (e *Engine) refreshHostnames(ctx context.Context, hosts []string) {
	for _, h := range hosts {
		if !e.dns.stale(h) {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		addrs, err := e.opts.LookupHost(lctx, h)
		cancel()
		if ctx.Err() != nil {
			// Shutting down: a lookup cut short says nothing about the hostname.
			return
		}
		ready, why := e.verdict(h, addrs, err)
		e.dns.record(h, ready, why)
	}
}

// verdict turns a lookup's result into ready, or a reason that says what to
// do next: the record to create, with the server's own addresses, or — when
// the hostname resolves to Cloudflare — the proxy switch to turn off. With no
// ServerAddresses known, resolving at all is enough, and no record can be
// suggested.
func (e *Engine) verdict(host string, addrs []string, err error) (ready bool, why string) {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, err == nil && len(addrs) == 0:
		return false, "does not resolve yet" + e.recordAdvice(host, "add", "an")
	case err != nil:
		return false, "could not be resolved: " + err.Error()
	case len(e.opts.ServerAddresses) == 0 || pointsAt(addrs, e.opts.ServerAddresses):
		return true, ""
	case behindCloudflare(addrs):
		return false, fmt.Sprintf("resolves to Cloudflare's proxy (%s), not to this server: turn the proxy off for this record (DNS only), or wait for Cloudflare support in a later release",
			strings.Join(addrs, ", "))
	}
	return false, fmt.Sprintf("resolves to %s, not to this server%s", strings.Join(addrs, ", "), e.recordAdvice(host, "change", "the"))
}

// recordAdvice spells out the records host needs: an A record for the
// server's IPv4 address and, when it has one, an AAAA record for its IPv6
// address — the first of each. Empty when the addresses are not known. The
// operator is typing this into a DNS console, so it reads like one row of it.
func (e *Engine) recordAdvice(host, verb, article string) string {
	var v4, v6 string
	for _, s := range e.opts.ServerAddresses {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		switch {
		case a.Is4() && v4 == "":
			v4 = a.String()
		case a.Is6() && v6 == "":
			v6 = a.String()
		}
	}
	var records []string
	if v4 != "" {
		records = append(records, fmt.Sprintf("%s A record: %s → %s", article, host, v4))
	}
	if v6 != "" {
		records = append(records, fmt.Sprintf("%s AAAA record: %s → %s", article, host, v6))
	}
	if len(records) == 0 {
		return ""
	}
	return fmt.Sprintf("; %s %s (DNS only, not proxied)", verb, strings.Join(records, " and "))
}

// cloudflareRanges are the addresses Cloudflare's proxy answers from, as
// published at https://www.cloudflare.com/ips-v4 and /ips-v6 on 2026-09-27;
// the list changes rarely. A hostname behind the orange cloud resolves to one
// of them and never to the server, and the fix is a switch in Cloudflare's
// dashboard, not a record — the message has to say which.
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// behindCloudflare reports whether every one of addrs is Cloudflare's. One
// address elsewhere means a record of the operator's own, and the advice for
// that case applies.
func behindCloudflare(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, s := range addrs {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return false
		}
		a = a.Unmap()
		inRange := false
		for _, p := range cloudflareRanges {
			if p.Contains(a) {
				inRange = true
				break
			}
		}
		if !inRange {
			return false
		}
	}
	return true
}

// pointsAt reports whether any of addrs is one of server's. Addresses are
// compared parsed, so that an IPv4 address mapped into IPv6 still matches.
func pointsAt(addrs, server []string) bool {
	for _, a := range addrs {
		for _, s := range server {
			if sameAddress(a, s) {
				return true
			}
		}
	}
	return false
}

func sameAddress(a, b string) bool {
	pa, err := netip.ParseAddr(a)
	if err != nil {
		return a == b
	}
	pb, err := netip.ParseAddr(b)
	if err != nil {
		return false
	}
	return pa.Unmap() == pb.Unmap()
}
