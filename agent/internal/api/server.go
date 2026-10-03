// Package api exposes the deployment engine over a JSON REST API.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

type Server struct {
	engine    *deploy.Engine
	store     *store.Store
	tokenHash [sha256.Size]byte
	log       *slog.Logger
	now       func() time.Time

	closing   chan struct{} // closed by Close; ends long-lived streams
	closeOnce sync.Once

	// lastUsed is when each stored token's use was last written down, so
	// that it is written at most once a minute: see recordUse.
	usedMu   sync.Mutex
	lastUsed map[int64]time.Time

	// limiter slows down guessing: see ratelimit.go.
	limiter *rateLimiter
}

// New creates the API server. tokenHash is the SHA-256 of the root token;
// the server never sees or stores the token itself. Further tokens live in
// the store.
func New(engine *deploy.Engine, st *store.Store, tokenHash [sha256.Size]byte, log *slog.Logger) *Server {
	s := &Server{engine: engine, store: st, tokenHash: tokenHash, log: log, now: time.Now,
		closing: make(chan struct{}), lastUsed: map[int64]time.Time{}}
	s.limiter = newRateLimiter(func() time.Time { return s.now() })
	return s
}

// Close ends long-lived responses (log streams). Call it before
// http.Server.Shutdown, which would otherwise wait for them until its timeout.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.closing) })
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// The only unauthenticated endpoint: liveness for load balancers and
	// `shipwick server status`. It reveals nothing beyond the version.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	routes := routeTable{mux: mux, s: s}
	routes.read("GET /api/v1/server", s.handleServer)
	routes.read("GET /api/v1/applications", s.handleListApplications)
	routes.read("GET /api/v1/applications/{name}", s.withName(s.handleGetApplication))
	routes.admin("DELETE /api/v1/applications/{name}", s.withName(s.handleDeleteApplication))
	routes.deploy("POST /api/v1/applications/{name}/deploy", s.withName(s.handleDeploy))
	routes.deploy("POST /api/v1/applications/{name}/redeploy", s.withName(s.handleRedeploy))
	routes.deploy("POST /api/v1/applications/{name}/rollback", s.withName(s.handleRollback))
	routes.deploy("POST /api/v1/applications/{name}/stop", s.withName(s.handleStop))
	routes.deploy("POST /api/v1/applications/{name}/start", s.withName(s.handleStart))
	routes.read("GET /api/v1/applications/{name}/logs", s.withName(s.handleLogs))
	routes.read("GET /api/v1/applications/{name}/events", s.withName(s.handleEvents))
	routes.read("GET /api/v1/applications/{name}/metrics", s.withName(s.handleMetrics))
	routes.read("GET /api/v1/deployments", s.handleListDeployments)
	routes.read("GET /api/v1/deployments/{id}", s.handleGetDeployment)
	s.jobRoutes(routes)
	s.volumeRoutes(routes)
	s.managedVolumeRoutes(routes)
	s.tokenRoutes(routes)
	s.metricsRoutes(routes)
	s.imageRoutes(routes)
	s.secretRoutes(routes)
	s.staticRoutes(routes)
	s.validateRoutes(routes)
	s.backupRoutes(routes)
	s.trafficRoutes(routes)
	s.registryRoutes(routes)
	s.certificateRoutes(routes)
	s.prometheusRoutes(routes)
	s.exportRoutes(routes)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, api.CodeEndpointNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path, nil)
	})

	return s.recoverPanics(s.logRequests(securityHeaders(mux)))
}

// routeTable registers authenticated endpoints by the role they require.
type routeTable struct {
	mux *http.ServeMux
	s   *Server
}

func (t routeTable) read(pattern string, h http.HandlerFunc)   { t.handle(pattern, api.RoleRead, h) }
func (t routeTable) deploy(pattern string, h http.HandlerFunc) { t.handle(pattern, api.RoleDeploy, h) }
func (t routeTable) admin(pattern string, h http.HandlerFunc)  { t.handle(pattern, api.RoleAdmin, h) }

func (t routeTable) handle(pattern string, role api.Role, h http.HandlerFunc) {
	t.mux.Handle(pattern, t.s.authenticate(role, h))
}

// authenticate requires "Authorization: Bearer <token>" and a token whose
// role covers the endpoint's. The token is hashed before it is compared or
// looked up (see identify), so neither its content nor its length leaks
// through timing. An address that failed too often lately is refused before
// its token is looked at (see ratelimit.go). The handler learns who called
// through the context.
func (s *Server) authenticate(role api.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := clientAddress(r.RemoteAddr)
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		who, known := s.identify(r.Context(), sha256.Sum256([]byte(token)))
		if !ok || !known {
			// A limited address is answered 429 instead of 401 and its
			// attempts no longer counted. A valid token is never refused:
			// behind the proxy every client shares one address, and one
			// guesser must not lock the others out (see ratelimit.go).
			if wait, limited := s.limiter.limited(addr); limited {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
				writeError(w, http.StatusTooManyRequests, api.CodeRateLimited, "too many failed authentications from this address; try again in a minute", nil)
				return
			}
			s.limiter.failed(addr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="shipwick"`)
			writeError(w, http.StatusUnauthorized, api.CodeUnauthorized, "missing or invalid API token", nil)
			return
		}
		if !who.Role.Covers(role) {
			writeError(w, http.StatusForbidden, api.CodeForbidden, forbiddenMessage(who.Role, role),
				map[string]any{"role": who.Role, "required": role})
			return
		}
		ctx := deploy.WithActor(context.WithValue(r.Context(), principalKey{}, who), who.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withName validates the {name} path segment before it reaches the engine,
// the database or Docker.
func (s *Server) withName(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := spec.ValidateName(name); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
			return
		}
		next(w, r, name)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer to flush and to
// adjust deadlines for streaming responses.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// logRequests logs one line per request. Headers and bodies are never logged:
// they carry the API token and application secrets.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Microsecond),
			"remote", r.RemoteAddr,
		)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, api.CodeInternal, "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON[T any](w http.ResponseWriter, status int, data T) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(api.Response[T]{Data: data})
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(api.ErrorResponse{Error: api.Error{Code: code, Message: message, Details: details}})
}

// writeEngineError maps engine and store errors to API errors.
func (s *Server) writeEngineError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *deploy.DomainConflictError
	var badImage *deploy.InvalidImageError
	var portConflict *deploy.PortConflictError
	var badCommand *deploy.InvalidCommandError
	var missingSecrets *deploy.MissingSecretsError
	var badUpload *deploy.InvalidUploadError
	var volumeInUse *deploy.VolumeInUseError
	var badCertificate *deploy.InvalidCertificateError
	var wildcard *deploy.WildcardError
	var unusableSecret *deploy.UnusableSecretError
	switch {
	case errors.As(err, &conflict):
		// Shaped like a validation error, because to the user it is one: a
		// line of their deploy.yaml needs to change.
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{
			"fields": []spec.FieldError{{Field: conflict.Field, Message: conflict.Message()}},
		})
	case errors.As(err, &portConflict):
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{
			"fields": []spec.FieldError{{Field: portConflict.Field(), Message: "already published by " + portConflict.Owner}},
		})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, api.CodeNotFound, "not found", nil)
	case errors.Is(err, deploy.ErrBusy):
		writeError(w, http.StatusConflict, api.CodeDeploymentInProgress, err.Error(), nil)
	case errors.Is(err, deploy.ErrNotDeployed):
		writeError(w, http.StatusConflict, api.CodeNotDeployed, err.Error(), nil)
	case errors.Is(err, deploy.ErrNoRollbackTarget):
		writeError(w, http.StatusConflict, api.CodeNoRollbackTarget, err.Error(), nil)
	case errors.As(err, &badImage):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "image: "+badImage.Reason, nil)
	case errors.Is(err, deploy.ErrJobRunning):
		writeError(w, http.StatusConflict, api.CodeJobAlreadyRunning, err.Error(), nil)
	case errors.As(err, &badCommand):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, badCommand.Error(), nil)
	case errors.Is(err, deploy.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, api.CodeRuntimeUnavailable, err.Error(), nil)
	case errors.Is(err, store.ErrTokenExists):
		writeError(w, http.StatusConflict, api.CodeTokenExists, err.Error(), nil)
	case errors.Is(err, deploy.ErrVolumeNotFound):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error(), nil)
	case errors.Is(err, deploy.ErrNotStopped):
		writeError(w, http.StatusConflict, api.CodeApplicationRunning, err.Error(), nil)
	case errors.Is(err, deploy.ErrInvalidArchive):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
	case errors.As(err, &missingSecrets):
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{"fields": missingSecrets.Fields()})
	case errors.As(err, &unusableSecret):
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{"fields": unusableSecret.Fields()})
	case errors.Is(err, store.ErrTooManySecrets):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
	case errors.Is(err, deploy.ErrImageNotBuilt):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
	case errors.Is(err, deploy.ErrStaticApplication):
		writeError(w, http.StatusConflict, api.CodeStaticApplication, err.Error(), nil)
	case errors.Is(err, deploy.ErrNoUpload):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error(), nil)
	case errors.As(err, &badUpload):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, badUpload.Reason, nil)
	case errors.Is(err, deploy.ErrNoSuchVolume):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error(), nil)
	case errors.As(err, &volumeInUse):
		writeError(w, http.StatusConflict, api.CodeVolumeInUse, volumeInUse.Error(), map[string]any{"application": volumeInUse.App})
	case errors.Is(err, deploy.ErrIncompleteImage):
		writeError(w, http.StatusConflict, api.CodeImageIncomplete, err.Error(), nil)
	case errors.As(err, &badCertificate):
		writeError(w, http.StatusBadRequest, api.CodeInvalidCertificate, badCertificate.Reason, nil)
	case errors.Is(err, store.ErrTooManyCertificates):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
	case errors.As(err, &wildcard):
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{"fields": wildcard.Fields()})
	case errors.Is(err, deploy.ErrTrafficUnavailable):
		writeError(w, http.StatusConflict, api.CodeTrafficUnavailable, err.Error(), nil)
	default:
		// Callers are authenticated operators, so the cause is more useful
		// to them than an opaque message. Errors never contain env values.
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error(), nil)
	}
}
