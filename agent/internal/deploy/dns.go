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
		ready, why := e.verdict(addrs, err)
		e.dns.record(h, ready, why)
	}
}

// verdict turns a lookup's result into ready or a reason. With no
// ServerAddresses known, resolving at all is enough.
func (e *Engine) verdict(addrs []string, err error) (ready bool, why string) {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, err == nil && len(addrs) == 0:
		return false, "does not resolve yet"
	case err != nil:
		return false, "could not be resolved: " + err.Error()
	case len(e.opts.ServerAddresses) == 0 || pointsAt(addrs, e.opts.ServerAddresses):
		return true, ""
	}
	return false, fmt.Sprintf("resolves to %s, not to this server (%s)",
		strings.Join(addrs, ", "), strings.Join(e.opts.ServerAddresses, ", "))
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
