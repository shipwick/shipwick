package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	out := make([]api.Token, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, tokenView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateToken answers with the token value, the only time it exists
// outside the caller's hands: the agent keeps its hash and nothing else.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req api.CreateTokenRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	switch {
	case req.Name == "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `name is required, e.g. {"name": "ci", "role": "deploy"}`, nil)
		return
	case req.Role == "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "role is required: read, deploy or admin", nil)
		return
	}
	if err := api.ValidateTokenName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := api.ValidateRole(req.Role); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	applications, err := api.ValidateTokenApplications(req.Role, req.Applications)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	now := s.now()
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(now) {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "expires_at is in the past: a token that has expired already would be of no use", nil)
			return
		}
		at := req.ExpiresAt.UTC()
		req.ExpiresAt = &at
	}
	auditTarget(r, req.Name)
	auditDetail(r, describeToken(req.Role, applications, req.ExpiresAt))

	value, hash, err := newToken()
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	t, err := s.store.CreateToken(r.Context(), store.Token{Name: req.Name, Role: req.Role, Hash: hash, CreatedAt: now, Applications: applications, ExpiresAt: req.ExpiresAt})
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("token created", "token", t.Name, "role", t.Role, "applications", t.Applications, "expires", t.ExpiresAt, "by", principalFrom(r.Context()).Name)
	writeJSON(w, http.StatusCreated, api.CreatedToken{ID: t.ID, Name: t.Name, Role: t.Role, CreatedAt: t.CreatedAt, Token: value,
		Applications: t.Applications, ExpiresAt: t.ExpiresAt})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == api.RootTokenName {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			"the root token is the one the agent is configured with; change it on the agent, not here", nil)
		return
	}
	if err := api.ValidateTokenName(name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := s.store.DeleteToken(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("token revoked", "token", name, "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

// newToken draws 32 random bytes and returns them as a token value together
// with the hash that is stored. The prefix is not part of the secret; it
// makes a token recognisable wherever it must not appear.
func newToken() (value string, hash []byte, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	value = api.TokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(value))
	return value, sum[:], nil
}

func tokenView(t store.Token) api.Token {
	return api.Token{ID: t.ID, Name: t.Name, Role: t.Role, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
		Applications: t.Applications, ExpiresAt: t.ExpiresAt}
}
