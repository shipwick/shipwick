package api

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// auditedRoute says how a request to an endpoint is written into the audit
// trail. The application comes from the path by itself; target and detail
// name further path segments worth keeping.
type auditedRoute struct {
	action string
	// target is the path segment that names what was acted on: {job},
	// {hostname}, {id}.
	target string
	// detail is a path segment recorded next to it: "volume" gives
	// "volume data".
	detail string
}

// auditedRoutes is every endpoint that changes something, or hands out data
// only admin may have. An endpoint that is not a GET has to be here or in
// unauditedRoutes, with the reason: a test walks the route table.
var auditedRoutes = map[string]auditedRoute{
	"DELETE /api/v1/applications/{name}":              {action: "application.delete"},
	"POST /api/v1/applications/{name}/deploy":         {action: "deploy"},
	"POST /api/v1/applications/{name}/redeploy":       {action: "redeploy"},
	"POST /api/v1/applications/{name}/rollback":       {action: "rollback"},
	"POST /api/v1/applications/{name}/stop":           {action: "stop"},
	"POST /api/v1/applications/{name}/start":          {action: "start"},
	"POST /api/v1/applications/{name}/images":         {action: "image.upload"},
	"PUT /api/v1/applications/{name}/static":          {action: "static.upload"},
	"POST /api/v1/applications/{name}/jobs/{job}/run": {action: "job.run", target: "job"},
	"POST /api/v1/applications/{name}/run":            {action: "run"},

	"POST /api/v1/applications/{name}/backups":                              {action: "backup.create"},
	"POST /api/v1/applications/{name}/backups/{id}/verify":                  {action: "backup.verify", target: "id"},
	"POST /api/v1/applications/{name}/backups/{id}/restore":                 {action: "backup.restore", target: "id"},
	"GET /api/v1/applications/{name}/backups/{id}/volumes/{volume}/archive": {action: "backup.download", target: "id", detail: "volume"},
	"DELETE /api/v1/applications/{name}/backups/{id}":                       {action: "backup.delete", target: "id"},
	"POST /api/v1/server/backups":                                           {action: "server.backup"},
	"POST /api/v1/server/backups/adopt":                                     {action: "backup.adopt"},

	"GET /api/v1/applications/{name}/volumes/{volume}/archive": {action: "volume.download", target: "volume"},
	"PUT /api/v1/applications/{name}/volumes/{volume}/archive": {action: "volume.restore", target: "volume"},
	"DELETE /api/v1/volumes/{name}":                            {action: "volume.delete", target: "name"},

	"PUT /api/v1/secrets/{name}":             {action: "secret.set", target: "name"},
	"DELETE /api/v1/secrets/{name}":          {action: "secret.delete", target: "name"},
	"PUT /api/v1/registries/{registry}":      {action: "registry.login", target: "registry"},
	"DELETE /api/v1/registries/{registry}":   {action: "registry.logout", target: "registry"},
	"PUT /api/v1/certificates/{hostname}":    {action: "certificate.set", target: "hostname"},
	"DELETE /api/v1/certificates/{hostname}": {action: "certificate.delete", target: "hostname"},
	"POST /api/v1/tokens":                    {action: "token.create"},
	"DELETE /api/v1/tokens/{name}":           {action: "token.revoke", target: "name"},
	"POST /api/v1/server/rotate-key":         {action: "key.rotate"},

	"POST /api/v1/export":          {action: "export.download"},
	"POST /api/v1/exports":         {action: "export.create"},
	"POST /api/v1/import":          {action: "import"},
	"POST /api/v1/standby/pull":    {action: "standby.pull"},
	"POST /api/v1/standby/promote": {action: "standby.promote"},

	"DELETE /api/v1/auth/session":            {action: "signout"},
	"POST /api/v1/access/rules":              {action: "access.grant"},
	"DELETE /api/v1/access/rules/{id}":       {action: "access.revoke", target: "id"},
	"DELETE /api/v1/access/sessions/{email}": {action: "access.signout", target: "email"},
}

// unauditedRoutes are the endpoints that are not GETs and change nothing.
var unauditedRoutes = map[string]string{
	"POST /api/v1/applications/{name}/validate":       "judges a deploy.yaml and stores nothing",
	"POST /api/v1/applications/{name}/images/missing": "answers which layers the server lacks",
}

type auditKey struct{}

// auditRecorder is the response writer of an audited request. It notes how
// the request was answered, and writes the entry when the handler returns.
type auditRecorder struct {
	http.ResponseWriter
	s     *Server
	entry api.AuditEntry

	status int
	// failure is the start of an error body: enough to read its code from.
	failure bytes.Buffer
}

// maxFailurePrefix is more than the envelope up to the end of "code" takes.
const maxFailurePrefix = 128

func (s *Server) beginAudit(w http.ResponseWriter, r *http.Request, rt route, who api.TokenIdentity) *auditRecorder {
	rec := &auditRecorder{ResponseWriter: w, s: s}
	rec.entry = api.AuditEntry{
		At:           s.now().UTC(),
		Actor:        api.Actor{Kind: who.Kind, Name: who.Name},
		Address:      clientAddress(r.RemoteAddr),
		ForwardedFor: forwardedFor(r),
		Action:       rt.audit.action,
	}
	if name := r.PathValue("name"); rt.application && spec.ValidateName(name) == nil {
		rec.entry.Application = name
	}
	if rt.audit.target != "" {
		rec.entry.Target = printable(r.PathValue(rt.audit.target))
	}
	if rt.audit.detail != "" {
		rec.entry.Detail = rt.audit.detail + " " + printable(r.PathValue(rt.audit.detail))
	}
	return rec
}

func (a *auditRecorder) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *auditRecorder) Write(p []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if a.status >= 400 && a.failure.Len() < maxFailurePrefix {
		a.failure.Write(p[:min(len(p), maxFailurePrefix-a.failure.Len())])
	}
	return a.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the real writer.
func (a *auditRecorder) Unwrap() http.ResponseWriter {
	return a.ResponseWriter
}

var (
	errorCodePattern = regexp.MustCompile(`^\{"error":\{"code":"([A-Z_]{1,64})"`)
	// What an accepted operation points at: the last two segments of its
	// Location say what was started.
	startedPattern = regexp.MustCompile(`/(deployments|runs|backups|exports)/([0-9]{1,19})$`)
)

// finish writes the entry. It is deferred around the handler: a handler
// that panicked is recorded as failed before the panic goes on.
func (a *auditRecorder) finish() {
	panicked := recover()
	e := a.entry
	e.Status = a.status
	switch {
	case panicked != nil:
		e.Status, e.Code = http.StatusInternalServerError, api.CodeInternal
	case e.Status == 0:
		e.Status = http.StatusOK
	}
	if m := errorCodePattern.FindSubmatch(a.failure.Bytes()); m != nil {
		e.Code = string(m[1])
	}
	switch {
	case e.Status < 400 && a.Header().Get(exportErrorTrailer) != "":
		// An export that broke off after its first byte was answered 200.
		e.Outcome, e.Code = api.AuditFailed, api.CodeInternal
	case e.Status < 400:
		e.Outcome = api.AuditOK
	case e.Status == http.StatusForbidden:
		e.Outcome = api.AuditRefused
	default:
		e.Outcome = api.AuditFailed
	}
	if m := startedPattern.FindStringSubmatch(a.Header().Get("Location")); m != nil && e.Status < 400 {
		e.Detail = joinDetail(e.Detail, strings.TrimSuffix(m[1], "s")+" "+m[2])
	}
	// The request may be over and its context cancelled: the entry is
	// written all the same.
	if _, err := a.s.store.AddAuditEntry(context.Background(), e); err != nil {
		a.s.log.Error("could not write the audit entry", "action", e.Action, "actor", e.Actor.Name, "error", err)
	}
	if panicked != nil {
		panic(panicked)
	}
}

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + ", " + b
}

// auditTarget names what the request acts on where the path does not: the
// token a POST /tokens creates. Only names belong here, never a value.
func auditTarget(r *http.Request, target string) {
	if rec, ok := r.Context().Value(auditKey{}).(*auditRecorder); ok {
		rec.entry.Target = target
	}
}

// auditDetail adds what the handler knows to the request's entry. The same
// rule holds: names and settings, never a value from the body.
func auditDetail(r *http.Request, detail string) {
	if rec, ok := r.Context().Value(auditKey{}).(*auditRecorder); ok {
		rec.entry.Detail = joinDetail(rec.entry.Detail, detail)
	}
}

// forwardedFor is the client address the nearest proxy reported, or "".
// Caddy replaces what a client sends in X-Forwarded-For with the address it
// saw; a caller who reaches the agent directly can write what it likes, which
// is why this is kept next to the connection's address and not instead of it.
func forwardedFor(r *http.Request) string {
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return ""
	}
	parts := strings.Split(values[len(values)-1], ",")
	ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1]))
	if ip == nil {
		return ""
	}
	return ip.String()
}

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 500
)

// auditRoutes registers the audit trail. It names every token and what each
// did to which application: admin's to read.
func (s *Server) auditRoutes(routes routeTable) {
	routes.admin("GET /api/v1/audit", s.handleAudit)
}

// handleAudit lists the audit trail, newest first. ?before= takes the id of
// the last entry of a page and continues after it.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", defaultAuditLimit, 1, maxAuditLimit)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := store.AuditFilter{Application: q.Get("application"), Actor: q.Get("actor"), Limit: limit}
	if filter.Application != "" {
		if err := spec.ValidateName(filter.Application); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
			return
		}
	}
	if len(filter.Actor) > 320 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "actor is the name of a token or the address of a person, e.g. actor=ci", nil)
		return
	}
	if raw := q.Get("since"); raw != "" {
		since, err := api.ParseSince(raw, s.now())
		if err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "since: "+err.Error(), nil)
			return
		}
		filter.Since = since
	}
	if raw := q.Get("before"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "before must be the id of an audit entry", nil)
			return
		}
		filter.Before = before
	}
	entries, err := s.store.AuditEntries(r.Context(), filter)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// describeToken is what the audit trail says about a token that was created:
// what it may do, never what it is.
func describeToken(role api.Role, applications []string, expiresAt *time.Time) string {
	detail := "role " + string(role)
	if len(applications) > 0 {
		detail += ", limited to " + strings.Join(applications, " ")
	}
	if expiresAt != nil {
		detail += ", expires " + expiresAt.UTC().Format(time.RFC3339)
	}
	return detail
}
