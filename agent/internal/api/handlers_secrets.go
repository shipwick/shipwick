package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
)

// maxSecretBody bounds a PUT /secrets body: the value at its largest, JSON
// escaping and the envelope around it.
const maxSecretBody = 4*api.MaxSecretValueBytes + 1024

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.store.ListSecrets(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	out := make([]api.Secret, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, api.Secret{Name: sec.Name, CreatedAt: sec.CreatedAt, UpdatedAt: sec.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSecret creates or replaces a secret. The value is in the body and
// nowhere else afterwards: not in the log, not in the response, not in any
// error.
func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := api.ValidateSecretName(name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSecretBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is too large or unreadable", nil)
		return
	}
	var req api.SetSecretRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `invalid JSON body: expected {"value": "..."}`, nil)
		return
	}
	if err := api.ValidateSecretValue(req.Value); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := s.store.SetSecret(r.Context(), name, req.Value, s.now()); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("secret set", "secret", name, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := api.ValidateSecretName(name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := s.store.DeleteSecret(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("secret removed", "secret", name, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}
