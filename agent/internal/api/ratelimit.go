package api

import (
	"net"
	"sync"
	"time"
)

// Failed authentications are counted per client address, and an address that
// fails failedAuthLimit times within failedAuthWindow is refused for
// rateLimitFor without its token being looked at. Only failures count: a
// dashboard polling with a good token is never slowed down. Behind the proxy
// every client shares Caddy's address, so the limit is one that honest use
// cannot reach — a mistyped token is one failure, not twenty — while a guess
// at a 256-bit token gets nowhere at twenty tries a minute either way; what
// the limit removes is "nothing slows it down at all".
const (
	failedAuthLimit  = 20
	failedAuthWindow = time.Minute
	rateLimitFor     = time.Minute
	// limiterPruneEvery bounds the map: an address that stopped trying is
	// forgotten on the next prune after its window and block have passed.
	limiterPruneEvery = time.Minute
)

type rateLimiter struct {
	now func() time.Time

	mu      sync.Mutex
	clients map[string]*clientFailures
	pruned  time.Time
}

type clientFailures struct {
	failures     []time.Time // within the window, oldest first
	blockedUntil time.Time
}

func newRateLimiter(now func() time.Time) *rateLimiter {
	return &rateLimiter{now: now, clients: map[string]*clientFailures{}}
}

// limited reports whether addr is refused right now, and for how much longer.
func (l *rateLimiter) limited(addr string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	c, ok := l.clients[addr]
	if !ok || !now.Before(c.blockedUntil) {
		return 0, false
	}
	return c.blockedUntil.Sub(now), true
}

// failed records one failed authentication from addr. The failure that
// reaches the limit starts the block and clears the count, so that the block
// lasts rateLimitFor from then and not one minute per further attempt.
func (l *rateLimiter) failed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	c, ok := l.clients[addr]
	if !ok {
		c = &clientFailures{}
		l.clients[addr] = c
	}
	c.failures = append(c.recent(now), now)
	if len(c.failures) >= failedAuthLimit {
		c.blockedUntil = now.Add(rateLimitFor)
		c.failures = nil
	}
}

// recent is the failures still within the window.
func (c *clientFailures) recent(now time.Time) []time.Time {
	cutoff := now.Add(-failedAuthWindow)
	i := 0
	for i < len(c.failures) && !c.failures[i].After(cutoff) {
		i++
	}
	return c.failures[i:]
}

// prune forgets addresses with nothing left to hold against them. Called with
// l.mu held.
func (l *rateLimiter) prune(now time.Time) {
	if now.Sub(l.pruned) < limiterPruneEvery {
		return
	}
	l.pruned = now
	for addr, c := range l.clients {
		if len(c.recent(now)) == 0 && !now.Before(c.blockedUntil) {
			delete(l.clients, addr)
		}
	}
}

// clientAddress is the host part of a connection's remote address. Behind
// the proxy it is Caddy's, for every client alike; the alternative, trusting
// X-Forwarded-For, would let a caller pick the address it is counted under.
func clientAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
