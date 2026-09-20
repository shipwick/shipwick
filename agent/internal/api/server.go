// Package api exposes the deployment engine over a JSON REST API.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
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
	tokenHash [sha256.Size]byte
	log       *slog.Logger

	closing   chan struct{} // closed by Close; ends long-lived streams
	closeOnce sync.Once
}

// New creates the API server. tokenHash is the SHA-256 of the bearer token;
// the server never sees or stores the token itself.
func New(engine *deploy.Engine, tokenHash [sha256.Size]byte, log *slog.Logger) *Server {
	return &Server{engine: engine, tokenHash: tokenHash, log: log, closing: make(chan struct{})}
}

// Close ends long-lived responses (log streams). Call it before
// http.Server.Shutdown, which would otherwise wait for them until its timeout.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.closing) })
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// The only unauthenticated endpoint: liveness for load balancers and
	// `deployctl server status`. It reveals nothing beyond the version.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	authed := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.authenticate(h))
	}
	authed("GET /api/v1/server", s.handleServer)
	authed("GET /api/v1/applications", s.handleListApplications)
	authed("GET /api/v1/applications/{name}", s.withName(s.handleGetApplication))
	authed("DELETE /api/v1/applications/{name}", s.withName(s.handleDeleteApplication))
	authed("POST /api/v1/applications/{name}/deploy", s.withName(s.handleDeploy))
	authed("POST /api/v1/applications/{name}/redeploy", s.withName(s.handleRedeploy))
	authed("POST /api/v1/applications/{name}/rollback", s.withName(s.handleRollback))
	authed("POST /api/v1/applications/{name}/stop", s.withName(s.handleStop))
	authed("POST /api/v1/applications/{name}/start", s.withName(s.handleStart))
	authed("GET /api/v1/applications/{name}/logs", s.withName(s.handleLogs))
	authed("GET /api/v1/applications/{name}/events", s.withName(s.handleEvents))
	authed("GET /api/v1/applications/{name}/metrics", s.withName(s.handleMetrics))
	authed("GET /api/v1/deployments", s.handleListDeployments)
	authed("GET /api/v1/deployments/{id}", s.handleGetDeployment)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, api.CodeEndpointNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path, nil)
	})

	return s.recoverPanics(s.logRequests(securityHeaders(mux)))
}

// authenticate requires "Authorization: Bearer <token>". Both sides are
// hashed before the constant-time comparison, so neither the token's content
// nor its length leaks through timing.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		presented := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(presented[:], s.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="shipwick"`)
			writeError(w, http.StatusUnauthorized, api.CodeUnauthorized, "missing or invalid API token", nil)
			return
		}
		next.ServeHTTP(w, r)
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
	switch {
	case errors.As(err, &conflict):
		// Shaped like a validation error, because to the user it is one: a
		// line of their deploy.yaml needs to change.
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{
			"fields": []spec.FieldError{{Field: "domain", Message: "already served by " + conflict.Owner}},
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
	case errors.Is(err, deploy.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, api.CodeRuntimeUnavailable, err.Error(), nil)
	default:
		// Callers are authenticated operators, so the cause is more useful
		// to them than an opaque message. Errors never contain env values.
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error(), nil)
	}
}
