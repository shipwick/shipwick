package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
)

// cloudflareRoutes registers the check of the Cloudflare token. GET /server
// carries the result of the last one; asking for a new one is admin's, as it
// writes a DNS record in every zone the token sees, and it is recorded.
func (s *Server) cloudflareRoutes(routes routeTable) {
	routes.admin("POST /api/v1/server/cloudflare/verify", s.handleVerifyCloudflare)
}

// handleVerifyCloudflare checks the token now and answers with what the
// check found. The check runs on a context of its own: a client that goes
// away must not leave the record it created in a zone.
func (s *Server) handleVerifyCloudflare(w http.ResponseWriter, r *http.Request) {
	status, err := s.engine.VerifyCloudflare(context.WithoutCancel(r.Context()))
	var recent *deploy.CloudflareCheckedRecentlyError
	switch {
	case errors.Is(err, deploy.ErrCloudflareNotConfigured):
		writeError(w, http.StatusConflict, api.CodeCloudflareNotConfigured, err.Error(), nil)
		return
	case errors.As(err, &recent):
		seconds := int(recent.RetryAfter.Seconds() + 0.5)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, api.CodeCloudflareCheckedRecently, err.Error(),
			map[string]any{"checked_at": recent.CheckedAt.UTC().Format(time.RFC3339), "retry_after_seconds": seconds})
		return
	case err != nil:
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
