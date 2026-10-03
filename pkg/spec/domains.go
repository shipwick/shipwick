package spec

import (
	"fmt"
	"strings"
)

// MaxHostnames bounds the aliases of one application, and its redirects.
const MaxHostnames = 20

// validateDomains checks the hostnames served next to the domain (aliases)
// and the ones redirected to it (redirects). Both need a domain: an alias is
// served like it, a redirect is sent to it.
func (r raw) validateDomains(verr *ValidationError, domain string) (aliases, redirects []string) {
	if len(r.Aliases) == 0 && len(r.Redirects) == 0 {
		return nil, nil
	}
	if domain == "" {
		if len(r.Aliases) > 0 {
			verr.add("aliases", "requires domain: aliases are served like it", "domain: example.com")
		}
		if len(r.Redirects) > 0 {
			verr.add("redirects", "requires domain: redirects are sent to it", "domain: example.com")
		}
		return nil, nil
	}
	if IsWildcard(domain) && len(r.Redirects) > 0 {
		// A redirect needs one address to send the visitor to.
		verr.add("redirects", "cannot be sent to a wildcard domain", "domain: example.com, with the wildcard under aliases")
	}
	// One hostname, one meaning: listed twice, or as an alias and a
	// redirect, the proxy would have two routes for it.
	seen := map[string]string{domain: "domain"}
	aliases = validateHostnames(verr, "aliases", r.Aliases, seen, ValidateHostname)
	redirects = validateHostnames(verr, "redirects", r.Redirects, seen, ValidateDomain)
	return aliases, redirects
}

// validateHostnames normalizes the entries of one list the way the domain is
// normalized, and records each under its field in seen. valid says what a
// hostname of this list may be: an alias may be a wildcard, a redirect is one
// name.
func validateHostnames(verr *ValidationError, field string, in []string, seen map[string]string, valid func(string) error) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxHostnames {
		verr.add(field, fmt.Sprintf("too many (%d)", len(in)), fmt.Sprintf("at most %d", MaxHostnames))
		return nil
	}
	out := make([]string, 0, len(in))
	for i, raw := range in {
		entry := fmt.Sprintf("%s[%d]", field, i)
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			verr.add(entry, "is empty", "a hostname, e.g. www.example.com")
		} else if err := valid(h); err != nil {
			verr.add(entry, err.Error(), "www.example.com")
		} else if under := seen[h]; under != "" {
			verr.add(entry, fmt.Sprintf("%q is already listed under %s", h, under), "each hostname once")
		}
		seen[h] = entry
		out = append(out, h)
	}
	return out
}
