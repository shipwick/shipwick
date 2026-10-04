package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
)

// applicationPrefix starts the path of every endpoint that acts on one
// application, which {name} then names.
const applicationPrefix = "/api/v1/applications/{name}"

// route is what the API knows about an endpoint before its handler runs:
// enough to decide whether the caller may use it, and what the audit trail
// calls a request to it. Every endpoint goes through here, so that neither
// decision is left to a handler.
type route struct {
	method string
	path   string
	role   api.Role
	// application is set for the endpoints under /applications/{name}.
	application bool
	// document is set for the endpoints that take a deploy.yaml and act on
	// the application it names: see documentRoutes.
	document bool
	// audit is nil for an endpoint that changes nothing.
	audit *auditedRoute
}

func newRoute(pattern string, role api.Role) route {
	method, path, _ := strings.Cut(pattern, " ")
	rt := route{method: method, path: path, role: role}
	rt.application = path == applicationPrefix || strings.HasPrefix(path, applicationPrefix+"/")
	rt.document = documentRoutes[pattern]
	if a, ok := auditedRoutes[pattern]; ok {
		rt.audit = &a
	}
	return rt
}

func (rt route) pattern() string { return rt.method + " " + rt.path }

// refusal is why a caller who is who they say may still not do this.
type refusal struct {
	code    string
	message string
	details map[string]any
}

// authorize decides whether who may send r to rt. The role comes first, as
// it always did. What a role allows beyond reading, a limited caller has
// only for its applications: an endpoint under /applications/{name} is
// checked against the name in the path — which is also the name of an
// application a deployment would create — and an endpoint that is not about
// one application is refused, because nothing says which of them it would
// touch. An endpoint that takes a document is about the application the
// document names (see documentRoutes), and is checked against that name; a
// document that names none is refused like an endpoint about none. Reading
// is never limited, and neither is admin.
func authorize(who api.TokenIdentity, rt route, r *http.Request) *refusal {
	if !who.Role.Covers(rt.role) {
		return &refusal{api.CodeForbidden, forbiddenMessage(who, rt.role), map[string]any{"role": who.Role, "required": rt.role}}
	}
	if rt.role == api.RoleRead || !who.Limited() {
		return nil
	}
	limit := "this " + credential(who) + " is limited to " + englishList(who.Applications)
	if !rt.application && !rt.document {
		return &refusal{api.CodeTokenLimited, limit + ", and this is not about one application", map[string]any{"applications": who.Applications}}
	}
	name := r.PathValue("name")
	if rt.document && name == "" {
		return &refusal{api.CodeTokenLimited, limit + ", and the document does not name an application", map[string]any{"applications": who.Applications}}
	}
	if who.Allows(name) {
		return nil
	}
	return &refusal{api.CodeTokenLimited, limit + "; it can read " + printable(name) + " and not change it",
		map[string]any{"applications": who.Applications, "application": printable(name)}}
}

// serve runs an authenticated request: it is authorized, recorded when it
// changes something — a refusal included — and handed to its handler with
// the caller in the context.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, rt route, who api.TokenIdentity, next http.Handler) {
	if rt.document {
		nameDocument(r)
	}
	ctx := deploy.WithActor(context.WithValue(r.Context(), principalKey{}, who), who.Name)
	if rt.audit != nil {
		rec := s.beginAudit(w, r, rt, who)
		defer rec.finish()
		w, ctx = rec, context.WithValue(ctx, auditKey{}, rec)
	}
	if no := authorize(who, rt, r); no != nil {
		writeError(w, http.StatusForbidden, no.code, no.message, no.details)
		return
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

// refuseExpired answers a token whose time is up. Only someone who holds the
// token gets here, so saying which token it is and when it lapsed tells them
// nothing they should not know.
func (s *Server) refuseExpired(w http.ResponseWriter, who api.TokenIdentity) {
	if who.Kind == api.ActorUser {
		s.refuseExpiredSession(w, who)
		return
	}
	at := who.ExpiresAt.UTC()
	w.Header().Set("WWW-Authenticate", `Bearer realm="shipwick", error="invalid_token"`)
	writeError(w, http.StatusUnauthorized, api.CodeTokenExpired,
		"token "+who.Name+" expired on "+at.Format("2006-01-02 at 15:04 UTC")+"; an admin creates a new one with: shipwick token create",
		map[string]any{"name": who.Name, "expired_at": at.Format(time.RFC3339)})
}

// identityOf is the caller a stored token stands for.
func identityOf(name string, role api.Role, applications []string, expiresAt *time.Time) api.TokenIdentity {
	if applications == nil {
		applications = []string{}
	}
	return api.TokenIdentity{Kind: api.ActorToken, Name: name, Role: role, Applications: applications, ExpiresAt: expiresAt}
}

// englishList joins names the way a sentence does: "a", "a and b",
// "a, b and c".
func englishList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// printable is a path segment as it may be repeated in an answer or kept in
// the audit trail: what a name could be, at a name's length. Anything else
// is not repeated at all.
func printable(segment string) string {
	if segment == "" || len(segment) > 128 {
		return "(invalid)"
	}
	for _, c := range segment {
		if c <= ' ' || c > '~' {
			return "(invalid)"
		}
	}
	return segment
}
