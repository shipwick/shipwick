package api

import (
	"net/http"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// nameClaim is the ID token claim people are named by on this agent.
func (si *signIn) nameClaim() string {
	if si.provider == nil {
		return api.DefaultNameClaim
	}
	return si.provider.NameClaim()
}

// nameWord is what a person's name is called in a sentence.
func (si *signIn) nameWord() string {
	if si.nameClaim() == api.DefaultNameClaim {
		return "address"
	}
	return "name"
}

// stillVouchedFor reports whether a session's name was read the way the
// agent reads names now, from a tenant it still accepts.
func (si *signIn) stillVouchedFor(sess store.Session) bool {
	return sess.NameClaim == si.nameClaim() && (si.provider == nil || si.provider.TenantAllowed(sess.Tenant))
}

// personNamed returns the name the person who signed in is known by, or
// answers why there is none. The name is the value of the claim the operator
// chose, and it is all the rules, the audit trail and a deployment's "by"
// will know of the person: an address is brought to lowercase as before, and
// anything else is kept as the provider wrote it, within what a name may be
// made of (api.ValidatePersonName).
//
// email_verified says something about the email claim only. Where people
// are named by another claim it is not looked at: an account without an
// address has nothing to verify, and the name does not rest on it.
func (s *Server) personNamed(w http.ResponseWriter, claims oidc.Claims) (string, bool) {
	claim := s.signIn.nameClaim()
	name, err := api.PersonName(claim, claims.Name)
	switch {
	case err != nil && claim == api.DefaultNameClaim:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed,
			"the provider named no e-mail address Shipwick can use for this account. The email scope must be among SHIPWICK_OIDC_SCOPES on the agent, and the account needs an address; where accounts have none, SHIPWICK_OIDC_NAME_CLAIM names people by another claim",
			map[string]any{"reason": api.SignInEmailMissing})
		return "", false
	case err != nil:
		// What the claim held is not repeated: it is the part that was
		// not fit to be.
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed,
			"the provider's ID token has no "+claim+" claim Shipwick can name this account by: it is missing, or holds something other than "+
				"letters, digits and punctuation without spaces. SHIPWICK_OIDC_NAME_CLAIM on the agent says which claim that is, and SHIPWICK_OIDC_SCOPES has to ask for it",
			map[string]any{"reason": api.SignInNameMissing, "claim": claim})
		return "", false
	case claim == api.DefaultNameClaim && claims.EmailVerified != nil && !*claims.EmailVerified:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed,
			"the provider says the address "+name+" has not been verified. Verify it there, then sign in again",
			map[string]any{"reason": api.SignInEmailNotVerified})
		return "", false
	}
	return name, true
}

// withTenant adds the tenant an account signed in from to what the audit
// trail says of a sign-in, where the provider has tenants.
func withTenant(detail, tenant string) string {
	if tenant == "" {
		return detail
	}
	return joinDetail(detail, "tenant "+tenant)
}
