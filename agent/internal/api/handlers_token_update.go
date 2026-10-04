package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// errTokenUnchangeable carries the sentence that refuses an update out of
// the store's transaction.
type errTokenUnchangeable struct{ message string }

func (e *errTokenUnchangeable) Error() string { return e.message }

// handleUpdateToken changes the applications a token is limited to, its end,
// or both, and leaves its value and its role as they are: whatever holds the
// token keeps working, with what the token may do from the next request on.
//
// The end can be moved, also for a token that has expired. Expiry bounds how
// long a token that leaked without anyone noticing is of use, and that is
// decided by whoever sets the date: an admin who can move it could as well
// create a token without one. What moving it saves is handing a new value to
// every place the old one is stored in, which is the step people put off
// until the pipeline is red. It is recorded with the old end and the new, so
// that the trail shows a token that outlived its first date and who let it.
func (s *Server) handleUpdateToken(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == api.RootTokenName {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			"the root token is the one the agent is configured with: it is admin, has no end, and is changed on the agent, not here", nil)
		return
	}
	if err := api.ValidateTokenName(name); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	var req api.UpdateTokenRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	now := s.now()
	switch {
	case req.Empty():
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			`nothing to change: name the applications, the end, or both, e.g. {"applications": ["my-api", "web"]} or {"expires_at": "2027-01-31T00:00:00Z"}`, nil)
		return
	case req.ExpiresAt != nil && req.NeverExpires:
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "expires_at and never_expires contradict each other: send one", nil)
		return
	case req.ExpiresAt != nil && !req.ExpiresAt.After(now):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			"expires_at is in the past. To stop a token now, revoke it: DELETE /tokens/"+name, nil)
		return
	}

	before, after, err := s.store.UpdateToken(r.Context(), name, func(t store.Token) (store.Token, error) {
		if req.Applications != nil {
			// Validated like creation, against the role the token has.
			applications, err := api.ValidateTokenApplications(t.Role, *req.Applications)
			if err != nil {
				return t, &errTokenUnchangeable{err.Error()}
			}
			t.Applications = applications
		}
		switch {
		case req.NeverExpires:
			t.ExpiresAt = nil
		case req.ExpiresAt != nil:
			at := req.ExpiresAt.UTC().Truncate(time.Second)
			t.ExpiresAt = &at
		}
		return t, nil
	})
	var refused *errTokenUnchangeable
	switch {
	case errors.As(err, &refused):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, refused.message, nil)
		return
	case err != nil:
		s.writeEngineError(w, r, err)
		return
	}
	changes := describeTokenChange(before, after)
	auditDetail(r, changes)
	s.log.Info("token changed", "token", after.Name, "changes", changes, "by", principalFrom(r.Context()).Name)
	writeJSON(w, http.StatusOK, tokenView(after))
}

// describeTokenChange is what the audit trail says about a token that was
// changed: each thing that differs, as it was and as it is.
func describeTokenChange(before, after store.Token) string {
	applications := func(list []string) string {
		if len(list) == 0 {
			return "all"
		}
		return strings.Join(list, " ")
	}
	expires := func(at *time.Time) string {
		if at == nil {
			return "never"
		}
		return at.UTC().Format(time.RFC3339)
	}
	var changes []string
	if !slices.Equal(before.Applications, after.Applications) {
		changes = append(changes, "applications "+applications(before.Applications)+" -> "+applications(after.Applications))
	}
	if expires(before.ExpiresAt) != expires(after.ExpiresAt) {
		changes = append(changes, "expires "+expires(before.ExpiresAt)+" -> "+expires(after.ExpiresAt))
	}
	if len(changes) == 0 {
		return "nothing changed"
	}
	return strings.Join(changes, ", ")
}
