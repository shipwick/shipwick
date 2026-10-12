package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
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
		out = append(out, api.Secret{Name: sec.Name, CreatedAt: sec.CreatedAt, UpdatedAt: sec.UpdatedAt, Application: sec.Application})
	}
	writeJSON(w, http.StatusOK, out)
}

// secretApplication checks the application a secret request names, if any.
// The application need not exist: its secrets come before its first
// deployment. ok is false once the refusal is written.
func secretApplication(w http.ResponseWriter, application string) (ok bool) {
	if application == "" {
		return true
	}
	if err := spec.ValidateName(application); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "application: "+err.Error(), nil)
		return false
	}
	return true
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
	body, ok := readBody(w, r, maxSecretBody)
	if !ok {
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
	if !secretApplication(w, req.Application) {
		return
	}
	if err := s.store.SetSecret(r.Context(), req.Application, name, req.Value, s.now()); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	if req.Application != "" {
		auditDetail(r, "application "+req.Application)
	}
	s.log.Info("secret set", "secret", name, "application", req.Application, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := api.ValidateSecretName(name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	application := r.URL.Query().Get("application")
	if !secretApplication(w, application) {
		return
	}
	if err := s.store.DeleteSecret(r.Context(), application, name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	if application != "" {
		auditDetail(r, "application "+application)
	}
	s.log.Info("secret removed", "secret", name, "application", application, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}
