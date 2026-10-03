package commands

import (
	"slices"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// describeProxy is the Proxy line of a validation summary: what the proxy
// block asks for, without any of its values — one of them is a password.
func describeProxy(app spec.App) string {
	p := app.Proxy
	var parts []string
	if p.StripPrefix {
		parts = append(parts, "strips "+app.Path)
	}
	if n := len(p.Headers); n > 0 {
		parts = append(parts, plural(n, "response header"))
	}
	if len(p.BasicAuth) > 0 {
		seen := map[string]bool{}
		var paths []string
		for _, a := range p.BasicAuth {
			where := a.Path
			if where == "" {
				where = "everything"
			}
			if !seen[where] {
				seen[where] = true
				paths = append(paths, where)
			}
		}
		sort.Strings(paths)
		parts = append(parts, "a password for "+strings.Join(paths, ", "))
	}
	if n := len(p.Redirects); n > 0 {
		parts = append(parts, plural(n, "redirect"))
	}
	return strings.Join(parts, "; ")
}

// takenHostname is the first hostname of app, with its path where the path
// is what collides, that one of the other applications already has.
func takenHostname(app spec.App, others []api.Application) (taken, owner string) {
	served := append([]string{app.Domain}, app.Aliases...)
	for _, other := range others {
		if other.Name == app.Name {
			continue
		}
		theirs := append([]string{other.Domain}, other.Aliases...)
		samePath := spec.PathKey(other.Path) == spec.PathKey(app.Path)
		for _, h := range served {
			switch {
			case h == "":
			case slices.Contains(other.Redirects, h):
				return h, other.Name
			case samePath && slices.Contains(theirs, h):
				return h + app.Path, other.Name
			}
		}
		for _, h := range app.Redirects {
			if slices.Contains(theirs, h) || slices.Contains(other.Redirects, h) {
				return h, other.Name
			}
		}
	}
	return "", ""
}
