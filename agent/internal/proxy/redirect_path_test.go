package proxy

import (
	"strings"
	"testing"
)

func TestARedirectHostnameLeadsToTheApplicationsPath(t *testing.T) {
	_, _, raw := build(t, []Route{
		{Domain: "example.com", Path: "/api", Backends: web8080, Redirects: []string{"api.example.net"}},
		{Domain: "example.com", Upstreams: []string{"site:80"}},
	})
	for _, r := range hostRoutesOf(t, raw) {
		if len(r.Match) == 0 || len(r.Match[0].Host) == 0 || r.Match[0].Host[0] != "api.example.net" {
			continue
		}
		if !strings.Contains(string(raw), `"match":[{"host":["api.example.net"]}]`) {
			t.Errorf("a redirect hostname is redirected whole, whatever path is asked of it: %s", raw)
		}
		got := r.Handle[0].Headers["Location"]
		if len(got) != 1 || got[0] != "https://example.com/api{http.request.uri}" {
			t.Errorf("Location = %v, want https://example.com/api{http.request.uri}: the rest of example.com is another application's", got)
		}
		return
	}
	t.Fatalf("no route for the redirect hostname in %s", raw)
}

func TestARedirectWithoutAPathLeadsToTheDomainAsBefore(t *testing.T) {
	_, _, raw := build(t, []Route{{Domain: "example.com", Backends: web8080, Redirects: []string{"www.example.com"}}})
	if !strings.Contains(string(raw), `"Location":["https://example.com{http.request.uri}"]`) {
		t.Errorf("the redirect of an application without a path changed: %s", raw)
	}
}
