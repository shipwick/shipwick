package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// rendered is one route of the config as Caddy reads it, with the handler
// chain kept as JSON: keys sorted, so it compares as text.
type rendered struct {
	Match  string
	Handle string
}

func renderedRoutes(t *testing.T, routes []Route) []rendered {
	t.Helper()
	raw, _, err := Build("unix//run/caddy/admin.sock", routes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var p struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  json.RawMessage
						Handle json.RawMessage
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	all := p.Apps.HTTP.Servers[serverName].Routes
	out := make([]rendered, 0, len(all)-1)
	for _, r := range all[:len(all)-1] { // without the catch-all
		out = append(out, rendered{Match: string(r.Match), Handle: string(r.Handle)})
	}
	return out
}

var web8080 = []Backend{{Name: "web_8080", Port: 8080}}

const (
	encodeJSON  = `{"encodings":{"gzip":{},"zstd":{}},"handler":"encode","minimum_length":1024,"prefer":["zstd","gzip"]}`
	proxyPrefix = `{"dynamic_upstreams":{"keep":"2s","name":"web_8080"`
)

func TestARouteWithAPathMatchesItAndEverythingUnderIt(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Aliases: []string{"www.example.com"}, Path: "/api", Backends: web8080}})
	if want := `[{"host":["example.com","www.example.com"],"path":["/api","/api/*"]}]`; got[0].Match != want {
		t.Errorf("match = %s, want %s: /api and /api/users, never /apix", got[0].Match, want)
	}
	if strings.Contains(got[0].Handle, "rewrite") {
		t.Errorf("the prefix is kept unless the application asks: %s", got[0].Handle)
	}
}

func TestTheLongestPathComesFirstAndNoPathLast(t *testing.T) {
	routes := []Route{
		{Domain: "example.com", Backends: web8080},
		{Domain: "example.com", Path: "/api", Backends: web8080},
		{Domain: "a.example.com", Backends: web8080},
		{Domain: "example.com", Path: "/api/v2/admin", Backends: web8080},
		{Domain: "zzz.example.com", Path: "/api", Backends: web8080},
	}
	want := []string{
		`[{"host":["example.com"],"path":["/api/v2/admin","/api/v2/admin/*"]}]`,
		`[{"host":["example.com"],"path":["/api","/api/*"]}]`,
		`[{"host":["zzz.example.com"],"path":["/api","/api/*"]}]`,
		`[{"host":["a.example.com"]}]`,
		`[{"host":["example.com"]}]`,
	}
	got := renderedRoutes(t, routes)
	for i := range want {
		if got[i].Match != want[i] {
			t.Errorf("route %d = %s, want %s", i, got[i].Match, want[i])
		}
	}

	// Whatever order the routes arrive in.
	reversed := []Route{routes[4], routes[3], routes[2], routes[1], routes[0]}
	_, fpA, _ := build(t, routes)
	_, fpB, _ := build(t, reversed)
	if fpA != fpB {
		t.Error("the same routes in another order must render the same config")
	}
}

func TestStripPrefixRewritesBeforeTheProxy(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Path: "/api", StripPrefix: true, Backends: web8080}})
	if want := `[` + encodeJSON + `,{"handler":"rewrite","strip_path_prefix":"/api"},` + proxyPrefix; !strings.HasPrefix(got[0].Handle, want) {
		t.Errorf("handle = %s\nwant it to start with %s", got[0].Handle, want)
	}
}

func TestResponseHeadersAreSetWhenTheResponseIsWritten(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Backends: web8080, Headers: map[string]string{
		"X-Frame-Options":           "DENY",
		"Strict-Transport-Security": "max-age=31536000",
	}}})
	want := `[{"handler":"headers","response":{"deferred":true,"set":{"Strict-Transport-Security":["max-age=31536000"],"X-Frame-Options":["DENY"]}}},` + encodeJSON
	if !strings.HasPrefix(got[0].Handle, want) {
		t.Errorf("handle = %s\nwant it to start with %s", got[0].Handle, want)
	}
}

func TestTextIsNeverReadAsAPlaceholderOfTheProxy(t *testing.T) {
	got := renderedRoutes(t, []Route{{
		Domain: "example.com", Backends: web8080,
		Headers:        map[string]string{"X-Leak": "{env.HOME} {file./data/caddy/key}", "Report-To": `{"group":"default"}`},
		PathRedirects:  []PathRedirect{{From: "/old", To: "/{env.HOME}?x={env.PATH}", Status: 308}},
		StaticRoot:     "/srv/shipwick/web/abc",
		StaticFallback: "{env.HOME}.html",
	}})
	for _, want := range []string{
		`"X-Leak":["\\{env.HOME} \\{file./data/caddy/key}"]`,
		`"Report-To":["\\{\"group\":\"default\"}"]`,
		`"Location":["/\\{env.HOME}?x=\\{env.PATH}"]`,
		`"uri":"/\\{env.HOME}.html"`,
	} {
		if !strings.Contains(got[0].Handle, want) {
			t.Errorf("handle lacks %s:\n%s", want, got[0].Handle)
		}
	}
}

func TestPathRedirectsAnswerBeforeAnythingElse(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Backends: web8080, PathRedirects: []PathRedirect{
		{From: "/old", To: "/new", Status: 308},
		{From: "/blog", To: "https://blog.example.org/?from=site", Status: 301},
	}}})
	want := `[{"handler":"subroute","routes":[` +
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://blog.example.org/?from=site"]},"status_code":301}],"match":[{"path":["/blog"]}],"terminal":true},` +
		`{"handle":[{"handler":"static_response","headers":{"Location":["/new{http.request.uri.prefixed_query}"]},"status_code":308}],"match":[{"path":["/old"]}],"terminal":true}` +
		`]},` + encodeJSON
	if !strings.HasPrefix(got[0].Handle, want) {
		t.Errorf("handle = %s\nwant it to start with %s", got[0].Handle, want)
	}
}

func TestBasicAuthAsksTheAccountsOfTheLongestPath(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Backends: web8080, BasicAuth: []BasicAuth{
		{Username: "everyone", Hash: "$2a$10$whole"},
		{Path: "/Admin", Username: "root", Hash: "$2a$10$root"},
		{Path: "/admin", Username: "admin", Hash: "$2a$10$admin"},
		{Path: "/admin/keys", Username: "keys", Hash: "$2a$10$keys"},
	}}})
	auth := func(accounts string) string {
		return `"handle":[{"handler":"authentication","providers":{"http_basic":{"accounts":[` + accounts + `],"hash":{"algorithm":"bcrypt"},"hash_cache":{}}}}]`
	}
	want := `[{"handler":"subroute","routes":[` +
		`{"group":"accounts",` + auth(`{"password":"$2a$10$keys","username":"keys"}`) + `,"match":[{"path":["/admin/keys","/admin/keys/*"]}]},` +
		`{"group":"accounts",` + auth(`{"password":"$2a$10$admin","username":"admin"},{"password":"$2a$10$root","username":"root"}`) + `,"match":[{"path":["/admin","/admin/*"]}]},` +
		`{"group":"accounts",` + auth(`{"password":"$2a$10$whole","username":"everyone"}`) + `}` +
		`]},` + encodeJSON
	if !strings.HasPrefix(got[0].Handle, want) {
		t.Errorf("handle = %s\nwant it to start with %s", got[0].Handle, want)
	}
}

func TestEverythingARouteCanDoInOrder(t *testing.T) {
	route := Route{
		Domain: "example.com", Path: "/api", StripPrefix: true, Backends: web8080,
		Headers:       map[string]string{"X-Frame-Options": "DENY"},
		BasicAuth:     []BasicAuth{{Path: "/api/admin", Username: "admin", Hash: "$2a$10$admin"}},
		PathRedirects: []PathRedirect{{From: "/api/old", To: "/api/new", Status: 302}},
	}
	want := `[{"handler":"headers","response":{"deferred":true,"set":{"X-Frame-Options":["DENY"]}}},` +
		`{"handler":"subroute","routes":[` +
		`{"handle":[{"handler":"static_response","headers":{"Location":["/api/new{http.request.uri.prefixed_query}"]},"status_code":302}],"match":[{"path":["/api/old"]}],"terminal":true},` +
		`{"group":"accounts","handle":[{"handler":"authentication","providers":{"http_basic":{"accounts":[{"password":"$2a$10$admin","username":"admin"}],"hash":{"algorithm":"bcrypt"},"hash_cache":{}}}}],"match":[{"path":["/api/admin","/api/admin/*"]}]}` +
		`]},` + encodeJSON + `,{"handler":"rewrite","strip_path_prefix":"/api"},` + proxyPrefix
	got := renderedRoutes(t, []Route{route})
	if !strings.HasPrefix(got[0].Handle, want) {
		t.Errorf("handle = %s\nwant it to start with %s", got[0].Handle, want)
	}

	// An application that is down still redirects and still asks who is
	// there; only then does it say that nobody serves.
	route.Backends = nil
	down := renderedRoutes(t, []Route{route})
	if !strings.Contains(down[0].Handle, `"authentication"`) || !strings.HasSuffix(down[0].Handle, `"status_code":503}]`) || strings.Contains(down[0].Handle, "rewrite") {
		t.Errorf("handle of a route nobody serves = %s", down[0].Handle)
	}
}

func TestAStaticRouteWithAPathAlwaysLosesItsPrefix(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", Path: "/docs", StaticRoot: "/srv/shipwick/docs/abc", Headers: map[string]string{"Cache-Control": "no-cache"}}})
	// /docs itself goes to /docs/: the page's relative links must lead below it.
	want := `[{"handler":"headers","response":{"deferred":true,"set":{"Cache-Control":["no-cache"]}}},` +
		`{"handler":"subroute","routes":[{"handle":[{"handler":"static_response","headers":{"Location":["/docs/{http.request.uri.prefixed_query}"]},"status_code":308}],"match":[{"path":["/docs"]}],"terminal":true}]},` + encodeJSON +
		`,{"handler":"rewrite","strip_path_prefix":"/docs"},{"handler":"file_server","index_names":["index.html"],"root":"/srv/shipwick/docs/abc"}]`
	if got[0].Handle != want {
		t.Errorf("handle = %s\nwant %s", got[0].Handle, want)
	}

	// The application's own redirect of its path is the one that counts.
	own := renderedRoutes(t, []Route{{Domain: "example.com", Path: "/docs", StaticRoot: "/srv/shipwick/docs/abc", PathRedirects: []PathRedirect{{From: "/Docs", To: "/docs/intro", Status: 302}}}})
	if strings.Count(own[0].Handle, "static_response") != 1 || !strings.Contains(own[0].Handle, `"Location":["/docs/intro{http.request.uri.prefixed_query}"]`) {
		t.Errorf("handle = %s", own[0].Handle)
	}
}

func TestAStaticFallbackAnswersWhatNamesNoFile(t *testing.T) {
	got := renderedRoutes(t, []Route{{Domain: "example.com", StaticRoot: "/srv/shipwick/web/abc", StaticFallback: "index.html"}})
	want := `[` + encodeJSON +
		`,{"handler":"file_server","index_names":["index.html"],"pass_thru":true,"root":"/srv/shipwick/web/abc"}` +
		`,{"handler":"rewrite","uri":"/index.html"}` +
		`,{"handler":"file_server","index_names":["index.html"],"root":"/srv/shipwick/web/abc"}]`
	if got[0].Handle != want {
		t.Errorf("handle = %s\nwant %s", got[0].Handle, want)
	}
}

func TestOrderOfAccountsAndRedirectsDoesNotChangeTheFingerprint(t *testing.T) {
	a := Route{
		Domain: "example.com", Backends: web8080,
		Headers:       map[string]string{"A": "1", "B": "2"},
		BasicAuth:     []BasicAuth{{Username: "b", Hash: "h2"}, {Path: "/x", Username: "a", Hash: "h1"}},
		PathRedirects: []PathRedirect{{From: "/b", To: "/c", Status: 308}, {From: "/a", To: "/c", Status: 308}},
	}
	b := a
	b.Headers = map[string]string{"B": "2", "A": "1"}
	b.BasicAuth = []BasicAuth{a.BasicAuth[1], a.BasicAuth[0]}
	b.PathRedirects = []PathRedirect{a.PathRedirects[1], a.PathRedirects[0]}
	_, fpA, _ := build(t, []Route{a})
	_, fpB, _ := build(t, []Route{b})
	if fpA != fpB {
		t.Error("the same route written in another order must render the same config, or every tick would reload Caddy")
	}
	if a.BasicAuth[0].Username != "b" || a.PathRedirects[0].From != "/b" {
		t.Error("Build reordered the caller's slices")
	}
}
