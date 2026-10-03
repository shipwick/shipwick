package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// registryRoutes registers the registry credentials the agent keeps, and the
// rotation of the key they and every other stored secret are encrypted with.
// Like secrets: anyone who may read sees which registries have a credential,
// and only admin stores or removes one.
func (s *Server) registryRoutes(routes routeTable) {
	routes.read("GET /api/v1/registries", s.handleListRegistries)
	routes.admin("PUT /api/v1/registries/{registry}", s.handleSetRegistry)
	routes.admin("DELETE /api/v1/registries/{registry}", s.handleDeleteRegistry)
	routes.admin("POST /api/v1/server/rotate-key", s.handleRotateKey)
}

// maxRegistryBody bounds a PUT /registries body: the password at its
// largest, JSON escaping and the envelope around it.
const maxRegistryBody = 4*api.MaxRegistryPasswordBytes + 2048

func (s *Server) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	registries, err := s.store.ListRegistries(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	out := make([]api.Registry, 0, len(registries))
	for _, reg := range registries {
		out = append(out, api.Registry{Registry: reg.Registry, Username: reg.Username, CreatedAt: reg.CreatedAt, UpdatedAt: reg.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetRegistry stores a credential once its registry has accepted it.
// The password is in the body and nowhere else afterwards: not in the log,
// not in the response, not in any error.
func (s *Server) handleSetRegistry(w http.ResponseWriter, r *http.Request) {
	registry, err := api.NormalizeRegistry(r.PathValue("registry"))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRegistryBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is too large or unreadable", nil)
		return
	}
	var req api.SetRegistryRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `invalid JSON body: expected {"username": "...", "password": "..."}`, nil)
		return
	}
	if err := api.ValidateRegistryCredential(req.Username, req.Password); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}

	var refused *deploy.RegistryLoginError
	_, err = s.engine.RegistryLogin(r.Context(), registry, req.Username, req.Password, s.now())
	switch {
	case errors.As(err, &refused):
		s.log.Info("registry login refused", "registry", registry, "by", principalFrom(r.Context()).Name)
		writeError(w, http.StatusBadRequest, api.CodeRegistryLoginFailed, refused.Error(), map[string]any{"registry": registry, "refused": refused.Refused})
		return
	case errors.Is(err, store.ErrTooManyRegistries):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	case err != nil:
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("registry credential stored", "registry", registry, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	registry, err := api.NormalizeRegistry(r.PathValue("registry"))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := s.store.DeleteRegistry(r.Context(), registry); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, api.CodeNotFound, "no credential is stored for "+registry, nil)
		return
	} else if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("registry credential removed", "registry", registry, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateKey replaces the encryption key. The agent's log is where this
// is recorded, with the token that asked and never the key; when the key
// comes from the environment the response carries the new one, once.
func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	who := principalFrom(r.Context()).Name
	// Not the request's context: a client that goes away must not stop a
	// rotation halfway through writing the key and the database.
	rotation, err := s.store.RotateKey(context.WithoutCancel(r.Context()))
	switch {
	case errors.Is(err, store.ErrRotationPending):
		writeError(w, http.StatusConflict, api.CodeKeyRotationPending,
			"the key was already rotated since the agent started, and its environment still holds the old one: put the new key in the agent's environment and restart it first",
			map[string]any{"key_file": s.store.PendingKeyFile()})
		return
	case errors.Is(err, store.ErrNotEncrypted):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	case err != nil:
		s.log.Error("key rotation failed", "by", who, "error", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error(), nil)
		return
	}
	s.log.Info("encryption key rotated", "by", who, "values", rotation.Values, "deployments", rotation.Deployments)

	out := api.KeyRotation{Values: rotation.Values, Deployments: rotation.Deployments, KeySource: api.KeySourceFile, KeyFile: rotation.KeyFile, Key: rotation.Key}
	if rotation.FromEnvironment {
		out.KeySource = api.KeySourceEnvironment
	}
	writeJSON(w, http.StatusOK, out)
}
