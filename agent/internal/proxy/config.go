// Package proxy drives Caddy, the reverse proxy in front of applications.
//
// The agent owns Caddy's entire configuration: whenever routing changes, the
// full config is regenerated from the desired routes and loaded through the
// admin API, which Caddy applies gracefully, without dropping connections.
// Declarative beats patching: the same input always yields the same config,
// and a half-applied state cannot exist.
package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Route maps a public hostname to the replicas that serve it.
type Route struct {
	// Domain is a validated hostname (see pkg/spec); never user-formatted text.
	Domain string
	// Upstreams are "host:port" dial addresses of replicas ready for traffic.
	// Empty means the application exists but nothing can serve it right now.
	Upstreams []string
	// Streaming disables response buffering, for upstreams that stream
	// (the agent's own log-follow endpoint).
	Streaming bool
}

const (
	serverName = "shipwick"
	// markerPrefix starts the @id of a marker route whose suffix is the
	// config's fingerprint. Asking Caddy for that id answers two questions
	// at once: is our config loaded, and is it this version of it.
	markerPrefix = "shipwick-config-"
)

// Build renders the Caddy JSON config for routes, and its fingerprint.
// adminListen is echoed into the config: loading a config without it would
// move the admin endpoint back to Caddy's default and lock the agent out.
//
// The config is assembled as data and marshalled — never templated — so no
// input can alter its structure.
func Build(adminListen string, routes []Route) (config []byte, fingerprint string, err error) {
	// The fingerprint covers the whole rendered config, not just the routes:
	// an agent upgrade that changes how routes are rendered (a timeout, a
	// header) must count as a change too, or Caddy would keep the old
	// rendering until some application happened to be redeployed.
	unmarked, err := render(adminListen, routes, "")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(unmarked)
	fingerprint = hex.EncodeToString(sum[:8])

	config, err = render(adminListen, routes, markerPrefix+fingerprint)
	return config, fingerprint, err
}

func render(adminListen string, routes []Route, markerID string) ([]byte, error) {
	routes = normalize(routes)
	caddyRoutes := make([]any, 0, len(routes)+1)
	for _, r := range routes {
		caddyRoutes = append(caddyRoutes, obj{
			"match":    []any{obj{"host": []string{r.Domain}}},
			"handle":   []any{handlerFor(r)},
			"terminal": true,
		})
	}
	// The last route doubles as the fingerprint marker (looked up by @id) and
	// as the catch-all: without one, Caddy answers unmatched requests with an
	// empty 200. It has no host matcher on purpose — Caddy would try to get a
	// certificate for any hostname mentioned in one.
	caddyRoutes = append(caddyRoutes, obj{
		"@id": markerID,
		"handle": []any{obj{
			"handler":     "static_response",
			"status_code": 404,
			"headers":     obj{"Content-Type": []string{"text/plain; charset=utf-8"}},
			"body":        "404 Not Found: no application is served at this address.\n",
		}},
	})

	root := obj{
		"admin": obj{"listen": adminListen},
		"apps": obj{
			"http": obj{
				"servers": obj{
					serverName: obj{
						// Listening on :443 with host matchers is all Caddy
						// needs to obtain and renew certificates for those
						// hosts and to redirect :80 to HTTPS on its own.
						"listen": []string{":443"},
						"routes": caddyRoutes,
					},
				},
			},
		},
	}
	return json.Marshal(root)
}

type obj = map[string]any

func handlerFor(r Route) obj {
	if len(r.Upstreams) == 0 {
		// An honest 503 beats Caddy's bare 502 for "no upstreams": the
		// application is known, it just has no healthy replica.
		return obj{
			"handler":     "static_response",
			"status_code": 503,
			"headers":     obj{"Content-Type": []string{"text/plain; charset=utf-8"}, "Retry-After": []string{"5"}},
			"body":        "503 Service Unavailable: no healthy replica.\n",
		}
	}
	upstreams := make([]any, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		upstreams = append(upstreams, obj{"dial": u})
	}
	// A replica that crashed stays listed until the supervisor's next tick.
	// These settings keep that second cheap. They cannot make it free: Caddy
	// abandons retries in progress when its config is reloaded, and removing
	// the dead replica *is* a reload — so the one request that discovers the
	// crash may still get a 502, on top of those that were in flight on the
	// replica when it died. Planned changes are unaffected: a deployment takes
	// replicas out of the config before it stops them.
	h := obj{
		"handler":   "reverse_proxy",
		"upstreams": upstreams,
		"load_balancing": obj{
			"selection_policy": obj{"policy": "round_robin"},
			// 1. A failed connection is retried on another replica. Only
			//    connection failures are: a request that reached an
			//    application is never sent twice.
			"try_duration": "5s",
		},
		"transport": obj{
			"protocol": "http",
			// 2. ...provided the failure is noticed in time. Upstreams are
			//    container names, and Docker's DNS forwards a name it no longer
			//    knows to the outside resolvers — seconds, longer than any
			//    retry budget. Replicas are one bridge hop away; a connection
			//    that takes half a second is not going to happen.
			"dial_timeout": "500ms",
		},
		"health_checks": obj{
			// 3. Having failed once, the dead replica is skipped by the
			//    requests behind it instead of costing each a timeout.
			"passive": obj{"fail_duration": "5s", "max_fails": 1},
		},
	}
	if r.Streaming {
		h["flush_interval"] = -1
	}
	return h
}

// normalize orders routes and upstreams, so that the same routing always
// produces the same config — and therefore the same fingerprint, which is what
// makes "nothing changed, skip the reload" possible.
func normalize(routes []Route) []Route {
	out := make([]Route, len(routes))
	for i, r := range routes {
		ups := append([]string(nil), r.Upstreams...)
		sort.Strings(ups)
		out[i] = Route{Domain: r.Domain, Upstreams: ups, Streaming: r.Streaming}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
