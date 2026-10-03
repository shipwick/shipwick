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

	"github.com/shipwick/shipwick/pkg/cloudflare"
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

// cloudflareTokenVariable is the agent's setting that lets a hostname stay
// behind Cloudflare's proxy; messages name it.
const cloudflareTokenVariable = "SHIPWICK_CLOUDFLARE_API_TOKEN"

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

// decide records a verdict that no lookup stands behind: it holds for as long
// as whoever reached it keeps reaching it, and is due for a lookup the moment
// they stop.
func (c *hostnameCache) decide(host string, ready bool, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[host] = hostnameVerdict{ready: ready, why: why}
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
		if ready, why, decided := e.decidedWithoutLookup(h); decided {
			e.dns.decide(h, ready, why)
			continue
		}
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
// the hostname resolves to Cloudflare — the proxy switch to turn off, unless
// certificates come through the DNS challenge and the switch may stay on. With no
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
	case cloudflare.Proxied(addrs):
		if e.opts.DNSChallenge {
			// Where Cloudflare sends the traffic cannot be seen from the
			// outside, and the certificate no longer depends on it.
			return true, ""
		}
		return false, fmt.Sprintf("resolves to Cloudflare's proxy (%s), not to this server: turn the proxy off for this record (DNS only), or set %s on the agent to keep it on",
			strings.Join(addrs, ", "), cloudflareTokenVariable)
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
	if e.opts.DNSChallenge {
		// Proxied or not is the operator's choice then.
		return fmt.Sprintf("; %s %s", verb, strings.Join(records, " and "))
	}
	return fmt.Sprintf("; %s %s (DNS only, not proxied)", verb, strings.Join(records, " and "))
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
