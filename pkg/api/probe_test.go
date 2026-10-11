package api

import "testing"

func TestRedirectLoopKnowsARedirectToARedirectedHostname(t *testing.T) {
	redirects := []string{"www.example.com", "example.net"}
	for _, tc := range []struct {
		name     string
		status   int
		location string
		target   string
		loop     bool
	}{
		{"the bare domain sends to www", 301, "https://www.example.com/", "www.example.com", true},
		{"a temporary redirect too", 302, "https://www.example.com/", "www.example.com", true},
		{"307 and 308 as well", 308, "https://example.net/some/path?q=1", "example.net", true},
		{"case and a port do not hide it", 307, "https://WWW.Example.com:443/", "WWW.Example.com", true},
		{"to the application's own hostname", 302, "https://example.com/login", "", false},
		{"to a relative path", 302, "/login", "", false},
		{"to somewhere else", 301, "https://accounts.google.com/", "", false},
		{"not a redirect", 200, "https://www.example.com/", "", false},
		{"a redirect without a Location", 301, "", "", false},
		{"not a URL", 301, "::not a url", "", false},
	} {
		target, loop := RedirectLoop(tc.status, tc.location, redirects)
		if loop != tc.loop || target != tc.target {
			t.Errorf("%s: RedirectLoop(%d, %q) = %q, %v; want %q, %v", tc.name, tc.status, tc.location, target, loop, tc.target, tc.loop)
		}
	}
}
