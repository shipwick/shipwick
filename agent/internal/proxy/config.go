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
	"strings"
)

// Route maps a public hostname to what serves it. With neither Backends nor
// Upstreams the application is known but nothing can serve it right now, and
// the hostname answers 503.
type Route struct {
	// Domain is a validated hostname (see pkg/spec); never user-formatted text.
	Domain string
	// Aliases are more hostnames served exactly like Domain: the same
	// handler, the same replicas.
	Aliases []string
	// Redirects are hostnames answered with a permanent redirect to Domain —
	// to Path on it, for a route that has one — path and query kept. They
	// need no backend, so they are served whether or not anything is.
	Redirects []string
	// Backends are names on the services network, resolved for every request:
	// the replicas of an application that are ready. A name must be listed
	// only while at least one replica carries it — Docker's DNS forwards names
	// it does not know, and those take seconds to fail.
	Backends []Backend
	// Upstreams are fixed "host:port" dial addresses, for what is not an
	// application: the agent's own API, the dashboard.
	Upstreams []string
	// Streaming disables response buffering, for upstreams that stream (the
	// agent's own log-follow endpoint), and keeps a followed log away from
	// the encoder: see encodeUnlessFollowed.
	Streaming bool
	// StaticRoot is a directory inside the proxy's own container whose files
	// are served as they are: a static application. It is built from an
	// application name and a digest, never from user-formatted text, and
	// excludes Backends and Upstreams.
	StaticRoot string
	// StaticFallback is the file under StaticRoot answered, with status 200,
	// for a request that names no file; empty means such a request is a 404.
	StaticFallback string
	// Path limits the route to one prefix of its hostnames: /api serves /api
	// and everything under it. Routes may share hostnames when their paths
	// differ; the longest path wins, and a route without one takes the rest.
	// Redirects stay whole hostnames.
	Path string
	// StripPrefix removes Path from a request on its way to the backends. The
	// files of a static application are always looked up without it.
	StripPrefix bool
	// Headers are set on the responses, over what a backend sent under the
	// same name.
	Headers map[string]string
	// BasicAuth are the accounts a request must present one of.
	BasicAuth []BasicAuth
	// PathRedirects answer single paths before anything else looks at them.
	PathRedirects []PathRedirect

	// plainLookups is set while rendering, for a proxy that lacks Shipwick's
	// source of replicas: see sourceKept.
	plainLookups bool
}

// BasicAuth is one account for Path and everything under it; an empty Path is
// the whole route. Where paths overlap, the accounts of the longest decide.
type BasicAuth struct {
	Path     string
	Username string
	// Hash is the bcrypt hash of the password; the proxy is never told more.
	Hash string
}

// PathRedirect sends a request for exactly From to To, a path on the same
// host or an absolute URL, with the query it came with unless To has its own.
type PathRedirect struct {
	From   string
	To     string
	Status int
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
	// resolveWait is how long a request waits for a name that has just been
	// asked again before it goes to the replicas of the last answer. Docker's
	// DNS answers in a few milliseconds or, now and then, not at all.
	resolveWait = "200ms"
	// resolveKeep is how long the last answer is used while the name goes
	// unanswered: long enough for a lost answer, which is what
	// happens, and no longer. Docker gives the address of a container that
	// is gone to the next one that starts, so an old answer may name a
	// container of another application; the agent keeps such an address
	// unused for a little longer than this (docker.AddressRest), and the two
	// must be changed together. A replica the agent stops itself does not
	// wait that out: the proxy is told to forget it (Forget).
	resolveKeep = "2s"

	// sourceKept is the source of replicas in Shipwick's Caddy (caddy/ in
	// this repository): it keeps the last answer while Docker's DNS is slow
	// or silent, and one application's lookup holds up no other's.
	sourceKept = "shipwick"
	// sourcePlain is the source every Caddy has. A lookup that goes
	// unanswered holds the requests to every application until the resolver
	// gives up; it is used only for a proxy without sourceKept.
	sourcePlain = "a"
	// missingSource is how Caddy says that it lacks sourceKept.
	missingSource = "unknown module: http.reverse_proxy.upstreams." + sourceKept
)

// Build renders the Caddy JSON config for routes, and its fingerprint.
// adminListen is echoed into the config: loading a config without it would
// move the admin endpoint back to Caddy's default and lock the agent out.
//
// The config is assembled as data and marshalled — never templated — so no
// input can alter its structure.
func Build(adminListen string, routes []Route) (config []byte, fingerprint string, err error) {
	return BuildWithTLS(adminListen, routes, TLS{})
}

// BuildWithTLS is Build with what the proxy is told about certificates: see
// TLS. The fingerprint covers that too, as a hash: a certificate replaced or
// a token changed is a configuration to load.
func BuildWithTLS(adminListen string, routes []Route, tls TLS) (config []byte, fingerprint string, err error) {
	return buildFor(adminListen, routes, tls, false)
}

// buildFor is BuildWithTLS for a proxy that has Shipwick's source of replicas,
// or, with plainLookups, for one that does not.
func buildFor(adminListen string, routes []Route, tls TLS, plainLookups bool) (config []byte, fingerprint string, err error) {
	// The fingerprint covers the whole rendered config, not just the routes:
	// an agent upgrade that changes how routes are rendered (a timeout, a
	// header) must count as a change too, or Caddy would keep the old
	// rendering until some application happened to be redeployed.
	unmarked, err := render(adminListen, routes, tls, plainLookups, "")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(unmarked)
	fingerprint = hex.EncodeToString(sum[:8])

	config, err = render(adminListen, routes, tls, plainLookups, markerPrefix+fingerprint)
	return config, fingerprint, err
}

func render(adminListen string, routes []Route, tls TLS, plainLookups bool, markerID string) ([]byte, error) {
	routes = normalize(routes)
	for i := range routes {
		routes[i].plainLookups = plainLookups
	}
	caddyRoutes := make([]any, 0, len(routes)+1)
	// Routes are tried in order and the first match answers. A wildcard
	// matches the names other routes serve one by one, so every wildcard
	// comes after every exact hostname: api.example.com wins over
	// *.example.com whichever application sorts first.
	var hosts []string
	var wildcardRoutes []any
	for _, r := range routes {
		exact, wildcard := splitWildcards(append([]string{r.Domain}, r.Aliases...))
		hosts = append(append(hosts, exact...), wildcard...)
		if len(exact) > 0 {
			caddyRoutes = append(caddyRoutes, obj{
				"match":    []any{matcherFor(exact, r.Path)},
				"handle":   handlersFor(r),
				"terminal": true,
			})
		}
		if len(wildcard) > 0 {
			wildcardRoutes = append(wildcardRoutes, obj{
				"match":    []any{matcherFor(wildcard, r.Path)},
				"handle":   handlersFor(r),
				"terminal": true,
			})
		}
		hosts = append(hosts, r.Redirects...)
		if len(r.Redirects) > 0 {
			// A route of its own: the redirect hostnames appear in a host
			// matcher on :443 like any other, which is what gets them a
			// certificate — https://www.example.com has to be answered
			// before it can be redirected.
			caddyRoutes = append(caddyRoutes, obj{
				"match":    []any{obj{"host": r.Redirects}},
				"handle":   []any{redirectTo(r.Domain, r.Path)},
				"terminal": true,
			})
		}
	}
	caddyRoutes = append(caddyRoutes, wildcardRoutes...)
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
		"admin":   obj{"listen": adminListen},
		"logging": logging(),
		"apps": obj{
			"http": obj{
				"servers": obj{
					serverName: obj{
						// Listening on :443 with host matchers is all Caddy
						// needs to obtain and renew certificates for those
						// hosts and to redirect :80 to HTTPS on its own.
						"listen": []string{":443"},
						"routes": caddyRoutes,
						"logs":   accessLogs(routes),
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
	// Certificates are added to the finished document rather than woven into
	// it: without a token and without supplied certificates nothing is added,
	// and Caddy's defaults apply.
	apps := root["apps"].(obj)
	tls.apply(apps["http"].(obj)["servers"].(obj)[serverName].(obj), hosts)
	if app := tls.app(); app != nil {
		apps["tls"] = app
	}
	return json.Marshal(root)
}

type obj = map[string]any

// accessLogger names the logger the server's access log is written with; Caddy
// puts it under "http.log.access".
const accessLogger = "shipwick"

// accessLogs turns the access log on for the server. The agent reads it to
// know what each application's traffic looks like (see deploy/traffic.go).
// Hostnames with fixed upstreams — the agent's own API, the dashboard — are
// left out: nobody is shown their traffic, and a dashboard that polls would
// fill the log with itself.
func accessLogs(routes []Route) obj {
	logs := obj{"default_logger_name": accessLogger}
	var skip []string
	for _, r := range routes {
		if len(r.Upstreams) > 0 {
			skip = append(skip, r.Domain)
			skip = append(skip, r.Aliases...)
		}
	}
	if len(skip) > 0 {
		logs["skip_hosts"] = skip
	}
	return logs
}

// logging sends the access log to Caddy's standard output as JSON, one
// request per line, and keeps it out of the default log, which stays on
// standard error with everything else Caddy has to say. An entry carries the
// time, the hostname, the method, the path, the status, the duration, the
// response size and the client's address, and nothing more: no headers in
// either direction (cookies, Authorization) and no query string, which is
// where tokens travel in a URL.
func logging() obj {
	access := "http.log.access." + accessLogger
	remove := obj{"filter": "delete"}
	return obj{"logs": obj{
		"default": obj{"exclude": []string{access}},
		"shipwick-access": obj{
			"include": []string{access},
			"writer":  obj{"output": "stdout"},
			"encoder": obj{
				"format": "filter",
				"wrap":   obj{"format": "json"},
				"fields": obj{
					"request>headers":     remove,
					"request>tls":         remove,
					"request>proto":       remove,
					"request>remote_ip":   remove,
					"request>remote_port": remove,
					"request>uri":         obj{"filter": "regexp", "regexp": `\?.*$`, "value": ""},
					"resp_headers":        remove,
					"user_id":             remove,
					"bytes_read":          remove,
				},
			},
		},
	}}
}

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

// redirectTo sends the request to the same path and query on domain, below
// the application's own path if it has one: that part of the domain is all
// the application serves, and the rest of it may be another application's.
// 308 rather than 301: the method is kept, so a POST to the old hostname does
// not turn into a GET.
func redirectTo(domain, path string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": 308,
		"headers":     obj{"Location": []string{"https://" + domain + literal(path) + "{http.request.uri}"}},
	}
}

// handlersFor is the chain a route's requests go through: what the
// application asked of the proxy — its response headers, its redirects, its
// accounts — then compression, then the proxy. A route that nothing serves
// still redirects and still asks for a password, and answers 503 after that.
func handlersFor(r Route) []any {
	var chain []any
	if len(r.Headers) > 0 {
		chain = append(chain, responseHeaders(r.Headers))
	}
	if gate := gateFor(r); gate != nil {
		chain = append(chain, gate)
	}
	if r.StaticRoot == "" && len(r.Backends) == 0 && len(r.Upstreams) == 0 {
		return append(chain, unavailable())
	}
	if r.Streaming {
		chain = append(chain, encodeUnlessFollowed())
	} else {
		chain = append(chain, encode())
	}
	if r.Path != "" && (r.StripPrefix || r.StaticRoot != "") {
		chain = append(chain, obj{"handler": "rewrite", "strip_path_prefix": r.Path})
	}
	if r.StaticRoot != "" && r.StaticFallback != "" {
		// The first file server hands on what it does not find; the request
		// is then rewritten into one for the fallback page, which the second
		// file server answers.
		first := fileServer(r.StaticRoot)
		first["pass_thru"] = true
		return append(chain, first, obj{"handler": "rewrite", "uri": "/" + literal(r.StaticFallback)}, fileServer(r.StaticRoot))
	}
	return append(chain, handlerFor(r))
}

// under matches a path and everything below it: /api and /api/users, but not
// /apix.
// matcherFor matches requests for hosts, and under path when there is one.
func matcherFor(hosts []string, path string) obj {
	match := obj{"host": hosts}
	if path != "" {
		match["path"] = under(path)
	}
	return match
}

func under(path string) []string {
	return []string{path, path + "/*"}
}

// literal keeps the proxy from reading text as its own placeholders: a header
// value such as {env.HOME} or {file./data/…} would be filled in from the
// proxy's own environment and files, and those hold the certificates' keys.
func literal(s string) string {
	return strings.ReplaceAll(s, "{", `\{`)
}

// responseHeaders sets headers when the response is written, not before:
// that is what lets them replace the ones a backend sends.
func responseHeaders(headers map[string]string) obj {
	set := make(map[string][]string, len(headers))
	for name, value := range headers {
		set[name] = []string{literal(value)}
	}
	return obj{"handler": "headers", "response": obj{"deferred": true, "set": set}}
}

// gateFor is what a request has to get past before it is served: the
// redirects, which answer it themselves, then the accounts. Nil when the
// route has neither.
func gateFor(r Route) any {
	redirects := r.PathRedirects
	if r.StaticRoot != "" && r.Path != "" {
		// A folder is a directory: /docs goes to /docs/, as a web server would
		// send it, so that the page's relative links lead below /docs/ and not
		// to its neighbours. The application's own redirect of the path wins.
		own := false
		for _, red := range redirects {
			own = own || strings.EqualFold(red.From, r.Path)
		}
		if !own {
			redirects = append(redirects[:len(redirects):len(redirects)], PathRedirect{From: r.Path, To: r.Path + "/", Status: 308})
		}
	}
	routes := make([]any, 0, len(redirects)+len(r.BasicAuth))
	for _, red := range redirects {
		location := literal(red.To)
		if !strings.Contains(red.To, "?") {
			location += "{http.request.uri.prefixed_query}"
		}
		routes = append(routes, obj{
			"match": []any{obj{"path": []string{red.From}}},
			"handle": []any{obj{
				"handler":     "static_response",
				"status_code": red.Status,
				"headers":     obj{"Location": []string{location}},
			}},
			"terminal": true,
		})
	}
	// One route per path, the longest first, all in one group: of a group
	// only the first route that matches runs, so /admin is asked for the
	// accounts of /admin and not for those of the whole application as well.
	for i := 0; i < len(r.BasicAuth); {
		path := r.BasicAuth[i].Path
		var accounts []any
		for ; i < len(r.BasicAuth) && r.BasicAuth[i].Path == path; i++ {
			accounts = append(accounts, obj{"username": r.BasicAuth[i].Username, "password": r.BasicAuth[i].Hash})
		}
		route := obj{
			"group": "accounts",
			"handle": []any{obj{
				"handler": "authentication",
				"providers": obj{"http_basic": obj{
					"accounts": accounts,
					"hash":     obj{"algorithm": "bcrypt"},
					// Without it every request pays for a bcrypt comparison.
					"hash_cache": obj{},
				}},
			}},
		}
		if path != "" {
			route["match"] = []any{obj{"path": under(path)}}
		}
		routes = append(routes, route)
	}
	if len(routes) == 0 {
		return nil
	}
	return obj{"handler": "subroute", "routes": routes}
}

// encode compresses responses for clients that ask, with zstd or gzip, and
// leaves alone what is small (under a kilobyte), already compressed, or not
// text — Caddy's default set of content types.
func encode() obj {
	return obj{
		"handler":        "encode",
		"encodings":      obj{"zstd": obj{}, "gzip": obj{}},
		"prefer":         []string{"zstd", "gzip"},
		"minimum_length": 1024,
	}
}

// encodeUnlessFollowed is the encoder for routes that carry a followed log
// (?follow=true on the agent's API, and the dashboard relaying it), which goes
// around it. The encoder does not hold back log lines: it decides on the first
// write whether to compress, and what it does compress it flushes line by
// line. What it holds back is the response header, until the first byte of
// the body exists — and a followed log of a quiet application has none for
// minutes, during which the client cannot tell a stream that is open from a
// request that hangs.
func encodeUnlessFollowed() obj {
	return obj{
		"handler": "subroute",
		"routes": []any{obj{
			"match":  []any{obj{"not": []any{obj{"query": obj{"follow": []string{"*"}}}}}},
			"handle": []any{encode()},
		}},
	}
}

func handlerFor(r Route) obj {
	if r.StaticRoot != "" {
		return fileServer(r.StaticRoot)
	}
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
			//    going to happen. An address the proxy was told to forget
			//    stays unused for as long (docker.ForgottenRest): a
			//    connection on its way there has been given up by then.
			"dial_timeout": "500ms",
			// Caddy would send requests to replicas through the proxy its
			// environment names (HTTP_PROXY), which is there for the
			// certificate authority and knows no container.
			"network_proxy": obj{"from": "none"},
		},
		"health_checks": obj{
			// 3. Having failed once, a dead replica is skipped by the requests
			//    behind it instead of costing each a timeout.
			"passive": obj{"fail_duration": "5s", "max_fails": 1},
		},
	}
	if len(r.Backends) > 0 {
		h["dynamic_upstreams"] = resolverFor(r.Backends, r.plainLookups)
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

// fileServer serves a directory of the proxy's own filesystem: the uploaded
// folder of a static application. A request for a directory gets its
// index.html; a path that names nothing is a 404, as it would be from any
// web server, unless the application names a fallback page (see handlersFor).
func fileServer(root string) obj {
	return obj{
		"handler":     "file_server",
		"root":        root,
		"index_names": []string{"index.html"},
	}
}

// resolverFor asks Docker's DNS, for every request, who stands behind the
// backends: one A record per replica that carries the name. A stopped replica
// leaves the answer by itself, and until the next lookup it refuses
// connections, which is a failure that is retried elsewhere.
func resolverFor(backends []Backend, plainLookups bool) obj {
	sources := make([]any, 0, len(backends))
	for _, b := range backends {
		if plainLookups {
			sources = append(sources, obj{
				"source":  sourcePlain,
				"name":    b.Name,
				"port":    strconv.Itoa(b.Port),
				"refresh": resolveEvery,
				// The networks are IPv4; an AAAA question would be forwarded to
				// the outside resolvers and hold up the answer.
				"versions": obj{"ipv4": true, "ipv6": false},
			})
			continue
		}
		sources = append(sources, obj{
			"source":  sourceKept,
			"name":    b.Name,
			"port":    strconv.Itoa(b.Port),
			"refresh": resolveEvery,
			"wait":    resolveWait,
			"keep":    resolveKeep,
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
		aliases := append([]string(nil), r.Aliases...)
		sort.Strings(aliases)
		redirects := append([]string(nil), r.Redirects...)
		sort.Strings(redirects)
		// Paths are matched without regard to case, and that is how accounts
		// are grouped: the longest path first, the whole route last.
		accounts := append([]BasicAuth(nil), r.BasicAuth...)
		for a := range accounts {
			accounts[a].Path = strings.ToLower(accounts[a].Path)
		}
		sort.SliceStable(accounts, func(a, b int) bool {
			if len(accounts[a].Path) != len(accounts[b].Path) {
				return len(accounts[a].Path) > len(accounts[b].Path)
			}
			if accounts[a].Path != accounts[b].Path {
				return accounts[a].Path < accounts[b].Path
			}
			return accounts[a].Username < accounts[b].Username
		})
		pathRedirects := append([]PathRedirect(nil), r.PathRedirects...)
		sort.SliceStable(pathRedirects, func(a, b int) bool { return pathRedirects[a].From < pathRedirects[b].From })

		r.Aliases, r.Redirects, r.Backends, r.Upstreams, r.BasicAuth, r.PathRedirects = aliases, redirects, backends, ups, accounts, pathRedirects
		out[i] = r
	}
	// Caddy takes the first route that matches. Among routes that share a
	// hostname the longest path must come first and the one without a path
	// last; ordering all routes that way does it for every hostname at once.
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Path) != len(out[j].Path) {
			return len(out[i].Path) > len(out[j].Path)
		}
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Path < out[j].Path
	})
	return out
}
