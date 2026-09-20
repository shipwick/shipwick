// Package proxy drives Caddy, the reverse proxy in front of applications.
//
// The agent owns Caddy's entire configuration: whenever routing changes, the
// full config is regenerated from the desired routes and loaded through the
// admin API. Declarative beats patching: the same input always yields the same
// config, and a half-applied state cannot exist.
//
// Loading a config is not free, though: Caddy replaces its servers, and a
// connection that is being established at that instant is reset. So the config
// names applications, not replicas. Which replicas stand behind a name is the
// business of Docker's DNS, changes without Caddy hearing of it, and leaves
// the config alone through rollouts, crashes and restarts.
package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
)

// Route maps a public hostname to what serves it. With neither Backends nor
// Upstreams the application is known but nothing can serve it right now, and
// the hostname answers 503.
type Route struct {
	// Domain is a validated hostname (see pkg/spec); never user-formatted text.
	Domain string
	// Backends are names on the services network, resolved for every request:
	// the replicas of an application that are ready. A name must be listed
	// only while at least one replica carries it — Docker's DNS forwards names
	// it does not know, and those take seconds to fail.
	Backends []Backend
	// Upstreams are fixed "host:port" dial addresses, for what is not an
	// application: the agent's own API, the dashboard.
	Upstreams []string
	// Streaming disables response buffering, for upstreams that stream
	// (the agent's own log-follow endpoint).
	Streaming bool
}

// Backend is a name that the ready replicas of an application share, and the
// port they listen on. Two versions of an application that listen on
// different ports are two backends of one route while a rollout lasts.
type Backend struct {
	Name string
	Port int
}

const (
	serverName = "shipwick"
	// markerPrefix starts the @id of a marker route whose suffix is the
	// config's fingerprint. Asking Caddy for that id answers two questions
	// at once: is our config loaded, and is it this version of it.
	markerPrefix = "shipwick-config-"

	// resolveEvery is how long Caddy keeps the answer for a backend's name. It
	// bounds how long a new replica waits for traffic and how long a stopped
	// one is still tried (and found refusing, and skipped).
	resolveEvery = "1s"
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
						// The last replica of an application can die between
						// two looks of the supervisor. Until the route is
						// switched to the static 503, the proxy finds nobody
						// behind the name; it says the same thing then.
						"errors": obj{"routes": []any{obj{
							"match":  []any{obj{"expression": "{http.error.status_code} in [502, 503]"}},
							"handle": []any{unavailable()},
						}}},
					},
				},
			},
		},
	}
	return json.Marshal(root)
}

type obj = map[string]any

// unavailable is an honest 503 instead of a bare 502: the application is
// known, it just has no replica that can serve.
func unavailable() obj {
	return obj{
		"handler":     "static_response",
		"status_code": 503,
		"headers":     obj{"Content-Type": []string{"text/plain; charset=utf-8"}, "Retry-After": []string{"5"}},
		"body":        "503 Service Unavailable: no healthy replica.\n",
	}
}

func handlerFor(r Route) obj {
	if len(r.Backends) == 0 && len(r.Upstreams) == 0 {
		return unavailable()
	}
	h := obj{
		"handler": "reverse_proxy",
		"load_balancing": obj{
			"selection_policy": obj{"policy": "round_robin"},
			// 1. A failed connection is retried on another replica. Only
			//    connection failures are, and requests that are safe to repeat:
			//    one whose body reached an application is never sent twice.
			"try_duration": "5s",
		},
		"transport": obj{
			"protocol": "http",
			// 2. ...provided the failure is noticed in time. Replicas are one
			//    bridge hop away; a connection that takes half a second is not
			//    going to happen.
			"dial_timeout": "500ms",
		},
		"health_checks": obj{
			// 3. Having failed once, a dead replica is skipped by the requests
			//    behind it instead of costing each a timeout.
			"passive": obj{"fail_duration": "5s", "max_fails": 1},
		},
	}
	if len(r.Backends) > 0 {
		h["dynamic_upstreams"] = resolverFor(r.Backends)
	} else {
		upstreams := make([]any, 0, len(r.Upstreams))
		for _, u := range r.Upstreams {
			upstreams = append(upstreams, obj{"dial": u})
		}
		h["upstreams"] = upstreams
	}
	if r.Streaming {
		h["flush_interval"] = -1
	}
	return h
}

// resolverFor asks Docker's DNS, for every request, who stands behind the
// backends: one A record per replica that carries the name. A stopped replica
// leaves the answer by itself, and until the next lookup it refuses
// connections, which is a failure that is retried elsewhere.
func resolverFor(backends []Backend) obj {
	sources := make([]any, 0, len(backends))
	for _, b := range backends {
		sources = append(sources, obj{
			"source":  "a",
			"name":    b.Name,
			"port":    strconv.Itoa(b.Port),
			"refresh": resolveEvery,
			// The networks are IPv4; an AAAA question would be forwarded to
			// the outside resolvers and hold up the answer.
			"versions": obj{"ipv4": true, "ipv6": false},
		})
	}
	if len(sources) == 1 {
		return sources[0].(obj)
	}
	return obj{"source": "multi", "sources": sources}
}

// normalize orders routes and what is behind them, so that the same routing
// always produces the same config — and therefore the same fingerprint, which
// is what makes "nothing changed, skip the reload" possible.
func normalize(routes []Route) []Route {
	out := make([]Route, len(routes))
	for i, r := range routes {
		ups := append([]string(nil), r.Upstreams...)
		sort.Strings(ups)
		backends := append([]Backend(nil), r.Backends...)
		sort.Slice(backends, func(a, b int) bool {
			if backends[a].Name != backends[b].Name {
				return backends[a].Name < backends[b].Name
			}
			return backends[a].Port < backends[b].Port
		})
		out[i] = Route{Domain: r.Domain, Backends: backends, Upstreams: ups, Streaming: r.Streaming}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
