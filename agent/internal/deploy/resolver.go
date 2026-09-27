package deploy

import (
	"context"
	"errors"
	"net"
	"time"
)

// The DNS gate asks public resolvers, not the server's own. The server's
// resolver answered "no such host" when the hostname was first looked up, and
// keeps that answer for as long as the zone's negative TTL says — half an hour
// on Cloudflare — long after the record was created. A certificate authority
// looks the hostname up from the outside, and that is the view that matters.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// PublicLookupHost resolves host through the public resolvers, falling back to
// the system's when none of them can be reached. The default for
// Options.LookupHost.
func PublicLookupHost(ctx context.Context, host string) ([]string, error) {
	return lookupThrough(ctx, host, publicResolvers, askResolver, net.DefaultResolver.LookupHost)
}

// lookupThrough asks the servers in turn. The first that knows the hostname
// decides; "no such host" from all of them is believed; a server that cannot
// be reached is skipped, and when none could be, the fallback decides.
func lookupThrough(ctx context.Context, host string, servers []string,
	ask func(ctx context.Context, server, host string) ([]string, error),
	fallback func(ctx context.Context, host string) ([]string, error)) ([]string, error) {
	var notFound, unreachable error
	for _, server := range servers {
		addrs, err := ask(ctx, server, host)
		if err == nil && len(addrs) > 0 {
			return addrs, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			notFound = err
			continue
		}
		unreachable = err
	}
	if notFound != nil {
		return nil, notFound
	}
	if unreachable != nil && fallback != nil {
		return fallback(ctx, host)
	}
	return nil, unreachable
}

func askResolver(ctx context.Context, server, host string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
	return r.LookupHost(ctx, host)
}
