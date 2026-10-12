package main

import "github.com/shipwick/shipwick/agent/internal/proxy"

// ownRoute is a route of the proxy for the agent's own API or the dashboard,
// at a fixed upstream. Streaming, because both carry followed logs, which the
// proxy must not buffer: the dashboard relays them. Strict-Transport-Security,
// because both are only ever served over HTTPS and nothing else sets it: the
// proxy redirects a plain request, and without the header a browser or a
// saved http:// URL would make one plain request before every redirect. The
// applications' routes are their operators' to decide (proxy.headers).
func ownRoute(domain, upstream string) proxy.Route {
	return proxy.Route{
		Domain:    domain,
		Upstreams: []string{upstream},
		Streaming: true,
		Headers:   map[string]string{"Strict-Transport-Security": "max-age=31536000"},
	}
}
