package deploy

import (
	"context"
	"net"
	"sync"
	"time"
)

const (
	// resolverTimeout bounds the question to one name server. A server that
	// can be reached answers in a few milliseconds.
	resolverTimeout = 700 * time.Millisecond
	// resolversSilentFor is how long name servers that could not be reached
	// are left alone before they are asked again.
	resolversSilentFor = 5 * time.Minute
)

// Resolvers asks a list of name servers about a hostname and falls back to
// the system's resolver when none of them can be reached — the case of a
// server whose firewall lets no DNS out. It remembers that: every lookup
// would otherwise wait for each of them in turn before it gets its answer,
// and the supervisor looks hostnames up all day.
type Resolvers struct {
	servers  []string
	ask      func(ctx context.Context, server, host string) ([]string, error)
	fallback func(ctx context.Context, host string) ([]string, error)
	now      func() time.Time

	mu          sync.Mutex
	silentSince time.Time
}

// NewResolvers asks servers, each "address:port", in the order given.
func NewResolvers(servers []string) *Resolvers {
	return &Resolvers{servers: servers, ask: askResolver, fallback: net.DefaultResolver.LookupHost, now: time.Now}
}

// SystemLookupHost asks the server's own resolver: the one to use where the
// public ones cannot be reached or do not know the names, a network with a
// name service of its own.
func SystemLookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// LookupHost is an Options.LookupHost.
func (r *Resolvers) LookupHost(ctx context.Context, host string) ([]string, error) {
	if r.silent() {
		return r.fallback(ctx, host)
	}
	return lookupThrough(ctx, host, r.servers, r.ask, func(ctx context.Context, host string) ([]string, error) {
		r.mu.Lock()
		r.silentSince = r.now()
		r.mu.Unlock()
		return r.fallback(ctx, host)
	})
}

func (r *Resolvers) silent() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.silentSince.IsZero() && r.now().Sub(r.silentSince) < resolversSilentFor
}
