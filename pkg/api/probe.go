package api

import (
	"net/http"
	"net/url"
	"strings"
)

// IsRedirect reports whether status is one of the four redirects a browser
// follows to another URL.
func IsRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// RedirectLoop judges what an application answered at its own hostname. An
// answer that sends the visitor to one of the application's `redirects` is a
// loop: the proxy sends those hostnames back to the domain, and the visitor
// goes round until the browser gives up. target is that hostname, as the
// application spelled it. Any other answer is not judged: a redirect to a
// sign-in page, to the application's own hostname under another path, or
// to somewhere else altogether is the application's business.
func RedirectLoop(status int, location string, redirects []string) (target string, loop bool) {
	if !IsRedirect(status) {
		return "", false
	}
	u, err := url.Parse(location)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	for _, r := range redirects {
		if strings.EqualFold(r, host) {
			return host, true
		}
	}
	return "", false
}
