package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

const (
	defaultLogTail = 100
	maxLogTail     = 5000

	defaultDeploymentLimit = 50
	maxDeploymentLimit     = 500
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Health{Status: "ok", Version: version.Version})
}

func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	server, err := s.engine.Server(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, server)
}

func (s *Server) handleListApplications(w http.ResponseWriter, r *http.Request) {
	apps, err := s.engine.Applications(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) handleGetApplication(w http.ResponseWriter, r *http.Request, name string) {
	app, err := s.engine.Application(r.Context(), name)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleDeleteApplication(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.engine.Delete(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeploy accepts the deploy.yaml document itself as the request body
// (YAML, or JSON, which YAML subsumes). The agent re-validates everything:
// the CLI's validation is a convenience, not a trust boundary.
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, name string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, spec.MaxConfigBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest,
				fmt.Sprintf("request body exceeds %d KB", spec.MaxConfigBytes/1024), nil)
			return
		}
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "could not read request body", nil)
		return
	}

	app, err := spec.Parse(body)
	if err != nil {
		var verr *spec.ValidationError
		if errors.As(err, &verr) {
			writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{"fields": verr.Fields})
			return
		}
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, err.Error(), nil)
		return
	}
	if app.Name != name {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			fmt.Sprintf("the config describes application %q but the URL names %q", app.Name, name), nil)
		return
	}

	d, err := s.engine.Deploy(r.Context(), app)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.acceptDeployment(w, d)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.engine.Stop(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.handleGetApplication(w, r, name)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.engine.Start(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.handleGetApplication(w, r, name)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, name string) {
	follow, err := boolParam(r, "follow")
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if follow {
		s.streamLogs(w, r, name)
		return
	}

	tail, ok := intParam(w, r, "tail", defaultLogTail, 1, maxLogTail)
	if !ok {
		return
	}
	lines, err := s.engine.Logs(r.Context(), name, tail)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lines)
}

// streamLogs answers logs?follow=true with newline-delimited JSON: one
// api.LogLine object per line, no envelope, until the client disconnects, the
// containers go away, or the agent shuts down.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, name string) {
	// tail=0 is meaningful here: "only what is logged from now on".
	tail, ok := intParam(w, r, "tail", defaultLogTail, 0, maxLogTail)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.closing:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Errors up to this point are still reported as regular JSON errors.
	lines, err := s.engine.LogStream(ctx, name, tail)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	// A stream outlives the server-wide write timeout by design.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		s.log.Warn("could not lift the write deadline for a log stream", "error", err)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	rc.Flush() // deliver the headers now, even if the app is quiet

	enc := json.NewEncoder(w)
	for line := range lines {
		if err := enc.Encode(line); err != nil {
			return // client went away; cancel() stops the producers
		}
		// Flush per line while the stream is idle, per batch while it is busy.
		if len(lines) == 0 {
			rc.Flush()
		}
	}
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", defaultDeploymentLimit, 1, maxDeploymentLimit)
	if !ok {
		return
	}
	application := r.URL.Query().Get("application")
	if application != "" {
		if err := spec.ValidateName(application); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
			return
		}
	}
	deployments, err := s.engine.Deployments(r.Context(), application, limit)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "deployment id must be a positive number", nil)
		return
	}
	d, err := s.engine.Deployment(r.Context(), id)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// intParam reads an optional integer query parameter. On invalid input it
// writes the error response and returns ok=false.
func intParam(w http.ResponseWriter, r *http.Request, name string, fallback, min, max int) (value int, ok bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			fmt.Sprintf("%s must be a number between %d and %d", name, min, max), nil)
		return 0, false
	}
	return n, true
}

func boolParam(r *http.Request, name string) (bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return v, nil
}

const (
	defaultEventLimit = 50
	maxEventLimit     = 500
)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, name string) {
	limit, ok := intParam(w, r, "limit", defaultEventLimit, 1, maxEventLimit)
	if !ok {
		return
	}
	events, err := s.engine.Events(r.Context(), name, limit)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// handleRedeploy deploys the active configuration again, optionally with
// another image. The body is optional: {"image": "..."}.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request, name string) {
	var req api.RedeployRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	d, err := s.engine.Redeploy(r.Context(), name, req.Image)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.acceptDeployment(w, d)
}

// handleRollback deploys the configuration of an earlier successful
// deployment. The body is optional: {"deployment_id": 12}; without it, the
// most recent one before the active deployment is used.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request, name string) {
	var req api.RollbackRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	if req.DeploymentID < 0 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "deployment_id must be a positive number", nil)
		return
	}
	d, err := s.engine.Rollback(r.Context(), name, req.DeploymentID)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.acceptDeployment(w, d)
}

// acceptDeployment answers 202: the deployment runs in the background; poll
// the Location until completed_at is set.
func (s *Server) acceptDeployment(w http.ResponseWriter, d store.Deployment) {
	w.Header().Set("Location", "/api/v1/deployments/"+strconv.FormatInt(d.ID, 10))
	writeJSON(w, http.StatusAccepted, deploy.DeploymentView(d))
}

// decodeOptionalBody decodes a small JSON body into v. An empty body is fine
// and leaves v untouched. Unknown fields are rejected: a typo like "imgae"
// must not silently redeploy the old image.
func (s *Server) decodeOptionalBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is too large or unreadable", nil)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "invalid JSON body: "+err.Error(), nil)
		return false
	}
	return true
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request, name string) {
	metrics, err := s.engine.Metrics(r.Context(), name)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}
