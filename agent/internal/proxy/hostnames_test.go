package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// hostRoute mirrors one route of the rendered config: who it matches and what
// it answers.
type hostRoute struct {
	Terminal bool
	Match    []struct{ Host []string }
	Handle   []struct {
		Handler    string
		StatusCode int `json:"status_code"`
		Headers    map[string][]string
		Dynamic    *resolver `json:"dynamic_upstreams"`
	}
}

func hostRoutesOf(t *testing.T, raw []byte) []hostRoute {
	t.Helper()
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct{ Routes []hostRoute }
			}
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Apps.HTTP.Servers[serverName].Routes
}

func TestAliasesShareTheDomainsRoute(t *testing.T) {
	_, _, raw := build(t, []Route{{
		Domain:   "example.com",
		Aliases:  []string{"www2.example.com", "api.example.com"},
		Backends: []Backend{{Name: "web_8080", Port: 8080}},
	}})
	routes := hostRoutesOf(t, raw)
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want the application's and the catch-all: aliases are more hosts of one route, not routes of their own", len(routes))
	}
	if got := strings.Join(routes[0].Match[0].Host, ","); got != "example.com,api.example.com,www2.example.com" {
		t.Errorf("hosts = %q, want the domain and the aliases in a stable order", got)
	}
	if h := routes[0].Handle[0]; h.Handler != "reverse_proxy" || h.Dynamic == nil || h.Dynamic.Name != "web_8080" {
		t.Errorf("handler = %+v, want the same backend for every host", h)
	}
}

func TestRedirectsAreARouteOfTheirOwnWithoutABackend(t *testing.T) {
	// Nothing serves the application; the redirect must be there all the same.
	_, _, raw := build(t, []Route{{Domain: "example.com", Redirects: []string{"www.example.com", "example.net"}}})
	routes := hostRoutesOf(t, raw)
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want the application's, its redirects' and the catch-all", len(routes))
	}
	if h := routes[0].Handle[0]; routes[0].Match[0].Host[0] != "example.com" || h.StatusCode != 503 {
		t.Errorf("first route = %+v, want the domain answering 503", routes[0])
	}

	redirect := routes[1]
	if got := strings.Join(redirect.Match[0].Host, ","); got != "example.net,www.example.com" {
		t.Errorf("redirect hosts = %q, want every redirect hostname, sorted", got)
	}
	if !redirect.Terminal || len(redirect.Handle) != 1 {
		t.Fatalf("redirect route = %+v", redirect)
	}
	h := redirect.Handle[0]
	if h.Handler != "static_response" || h.StatusCode != 308 {
		t.Errorf("handler = %s %d, want a static 308: the method must survive the redirect", h.Handler, h.StatusCode)
	}
	if got := h.Headers["Location"]; len(got) != 1 || got[0] != "https://example.com{http.request.uri}" {
		t.Errorf("Location = %v, want https://example.com{http.request.uri}: the path and query are kept", got)
	}
	if strings.Contains(string(raw), `"dynamic_upstreams"`) {
		t.Error("a redirect needs no backend")
	}
}

func TestHostnameOrderDoesNotChangeTheFingerprint(t *testing.T) {
	_, a, _ := build(t, []Route{{Domain: "example.com", Aliases: []string{"a.example.com", "b.example.com"}, Redirects: []string{"x.example.com", "y.example.com"}}})
	_, b, _ := build(t, []Route{{Domain: "example.com", Aliases: []string{"b.example.com", "a.example.com"}, Redirects: []string{"y.example.com", "x.example.com"}}})
	if a != b {
		t.Error("the same hostnames in another order must not reload the proxy")
	}
	_, c, _ := build(t, []Route{{Domain: "example.com", Aliases: []string{"a.example.com", "b.example.com"}}})
	if c == a {
		t.Error("dropping the redirects is another config")
	}
}
