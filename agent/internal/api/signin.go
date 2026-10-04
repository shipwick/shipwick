package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// signIn is what the server keeps for people who sign in with the company's
// accounts. A person becomes the same identity a token is — kind "user",
// named by the e-mail address, or by the claim the operator chose — so that
// authorize and the audit trail need to know nothing about it.
type signIn struct {
	// provider is nil when no provider is configured: tokens only.
	provider *oidc.Provider

	mu sync.Mutex
	// nonces are the sign-ins completed lately, by the hash of their nonce:
	// an ID token is accepted for one of them once. An entry is kept for as
	// long as an ID token with that nonce could still be presented.
	nonces map[[sha256.Size]byte]time.Time
	// used is recordUse's map for sessions.
	used map[int64]time.Time
}

// nonceKeptFor is longer than an ID token is accepted for after it was
// issued (ten minutes and the allowance for clocks: see oidc.Verify).
const nonceKeptFor = 15 * time.Minute

// UseSignIn turns signing in on. Call it before the server takes requests.
func (s *Server) UseSignIn(p *oidc.Provider) {
	s.signIn.provider = p
}

// signInRoutes registers signing in and the rules that say who may. The
// first two are reached without a token: one tells the dashboard where to
// send a browser, all of it public; the other is where a person becomes a
// caller, and what it is handed — a code the provider issued a moment ago
// and the verifier that belongs to it — is the credential.
func (s *Server) signInRoutes(mux *http.ServeMux, routes routeTable) {
	mux.HandleFunc("GET /api/v1/auth", s.handleSignInConfig)
	mux.HandleFunc("POST /api/v1/auth/exchange", s.handleSignIn)
	routes.read("DELETE /api/v1/auth/session", s.handleSignOut)

	routes.admin("GET /api/v1/access/rules", s.handleListAccess)
	routes.admin("POST /api/v1/access/rules", s.handleGrantAccess)
	routes.admin("DELETE /api/v1/access/rules/{id}", s.handleRevokeAccess)
	routes.admin("GET /api/v1/access/sessions", s.handleListSessions)
	routes.admin("DELETE /api/v1/access/sessions/{email}", s.handleEndSessions)
}

func (s *Server) signInStatus() api.SignInStatus {
	if s.signIn.provider == nil {
		return api.SignInStatus{}
	}
	return api.SignInStatus{Configured: true, Issuer: s.signIn.provider.Issuer(), NameClaim: s.signIn.nameClaim()}
}

func (s *Server) handleSignInConfig(w http.ResponseWriter, r *http.Request) {
	p := s.signIn.provider
	if p == nil {
		writeJSON(w, http.StatusOK, api.SignInConfig{Scopes: []string{}})
		return
	}
	endpoint, err := p.AuthorizationEndpoint(r.Context())
	if err != nil {
		s.writeSignInError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SignInConfig{Configured: true, Issuer: p.Issuer(), AuthorizationEndpoint: endpoint,
		ClientID: p.ClientID(), Scopes: p.Scopes(), RedirectURI: p.RedirectURL()})
}

var (
	// RFC 7636: 43 to 128 unreserved characters.
	codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	// At least 128 bits in base64url, which is 22 characters.
	noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,256}$`)
)

// plausibleCode reports whether code can be one: it is the provider's to
// shape, and it travels in a form, so anything visible will do.
func plausibleCode(code string) bool {
	if code == "" || len(code) > 4096 {
		return false
	}
	for _, c := range code {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// maxSignInBody is more than the longest code a provider issues, with the
// verifier, the nonce and the redirect URI.
const maxSignInBody = 16 << 10

// handleSignIn turns what the provider sent the browser back with into a
// session. The dashboard's server hands over the code, the PKCE verifier and
// the nonce it kept in the browser's cookie while the person was away at the
// provider. The agent redeems the code with the client secret, which only it
// holds; believes the ID token only if the provider signed it for this
// client, just now, with that nonce; and looks the person up in the rules.
//
// A code that was read off a callback is worthless without the verifier. A
// code that was used cannot be used again: the provider redeems it once, and
// the agent accepts each nonce once.
func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	p := s.signIn.provider
	if p == nil {
		writeError(w, http.StatusConflict, api.CodeSignInNotConfigured,
			"signing in is not configured on this agent: it accepts API tokens only. An operator turns it on by setting SHIPWICK_OIDC_ISSUER", nil)
		return
	}
	var req api.SignInRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignInBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is too large or unreadable", nil)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// The decoder's error may quote the body, and the body is a credential.
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `the body must be JSON: {"code", "code_verifier", "nonce", "redirect_uri"}`, nil)
		return
	}
	switch {
	case !plausibleCode(req.Code):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "code is required: the one the provider appended to the callback", nil)
		return
	case !codeVerifierPattern.MatchString(req.CodeVerifier):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "code_verifier is required: the PKCE verifier whose S256 challenge went to the provider, 43 to 128 characters", nil)
		return
	case !noncePattern.MatchString(req.Nonce):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "nonce is required: the one sent to the provider, at least 22 characters of base64url", nil)
		return
	case req.RedirectURI != p.RedirectURL():
		// Never whatever a request says: the provider would refuse another
		// one anyway, and the agent does not ask it to.
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "redirect_uri must be "+p.RedirectURL()+": the dashboard this agent is configured with", map[string]any{"redirect_uri": p.RedirectURL()})
		return
	}

	// Failed sign-ins are counted like wrong tokens, and an address that
	// keeps failing costs the provider no further request.
	addr := clientAddress(r.RemoteAddr)
	if wait, limited := s.limiter.limited(addr); limited {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		writeError(w, http.StatusTooManyRequests, api.CodeRateLimited, "too many failed authentications from this address; try again in a minute", nil)
		return
	}
	raw, err := p.Exchange(r.Context(), req.Code, req.CodeVerifier)
	if err != nil {
		s.failSignIn(w, addr, err)
		return
	}
	claims, err := p.Verify(r.Context(), raw, req.Nonce)
	if err != nil {
		s.failSignIn(w, addr, err)
		return
	}
	now := s.now()
	if !s.signIn.firstUse(req.Nonce, now) {
		s.limiter.failed(addr)
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed, "this sign-in was completed before. Sign in again", map[string]any{"reason": api.SignInNonceReused})
		return
	}

	email, ok := s.personNamed(w, claims)
	if !ok {
		return
	}
	groups := make([]string, 0, len(claims.Groups))
	for _, g := range claims.Groups {
		// Where every tenant may sign in, a group is whatever the account's
		// own tenant calls it: no rule is to be met that way.
		if api.ValidGroup(g) && !p.AnyTenant() {
			groups = append(groups, g)
		}
	}

	rules, err := s.store.AccessRules(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	grant, ok := api.ResolveAccess(rules, email, groups)
	if !ok {
		s.log.Info("sign-in refused: no access rule", "user", email)
		s.auditSignIn(r, email, api.AuditRefused, http.StatusForbidden, api.CodeAccessNotGranted, withTenant("", claims.Tenant))
		writeError(w, http.StatusForbidden, api.CodeAccessNotGranted,
			email+" signed in, and no rule on this server gives that "+s.signIn.nameWord()+" a role. An admin grants one with: shipwick access grant "+api.WhoOf(email)+" --role read",
			map[string]any{"email": email, "name": email})
		return
	}

	value, hash, err := newSession()
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	sess, err := s.store.CreateSession(r.Context(), store.Session{Hash: hash, Email: email, Groups: groups,
		Role: grant.Role, Applications: grant.Applications, CreatedAt: now, ExpiresAt: now.Add(api.SessionLifetime),
		NameClaim: s.signIn.nameClaim(), Tenant: claims.Tenant})
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("signed in", "user", email, "role", sess.Role, "applications", sess.Applications, "expires", sess.ExpiresAt)
	s.auditSignIn(r, email, api.AuditOK, http.StatusOK, "", withTenant(describeToken(sess.Role, sess.Applications, &sess.ExpiresAt), sess.Tenant))
	writeJSON(w, http.StatusOK, api.SignedIn{Session: value, Identity: sessionIdentity(sess)})
}

// failSignIn answers a sign-in the provider or the ID token did not carry.
func (s *Server) failSignIn(w http.ResponseWriter, addr string, err error) {
	var oe *oidc.Error
	if errors.As(err, &oe) && oe.Kind != oidc.Unavailable {
		s.limiter.failed(addr)
	}
	s.writeSignInError(w, err)
}

// writeSignInError maps what the oidc package reports to the API's codes.
// Its messages are written to be shown: they hold neither the client secret
// nor anything the caller sent.
func (s *Server) writeSignInError(w http.ResponseWriter, err error) {
	var oe *oidc.Error
	if !errors.As(err, &oe) {
		s.log.Error("sign-in failed", "error", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, "sign-in failed inside the agent; its log says why", nil)
		return
	}
	s.log.Warn("sign-in failed", "error", oe.Message)
	switch oe.Kind {
	case oidc.CodeRejected:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed, oe.Message, map[string]any{"reason": api.SignInCodeRejected})
	case oidc.InvalidToken:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed, oe.Message, map[string]any{"reason": api.SignInInvalidIDToken})
	case oidc.NonceMismatch:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed, oe.Message, map[string]any{"reason": api.SignInNonceMismatch})
	case oidc.TenantNotAllowed:
		writeError(w, http.StatusUnauthorized, api.CodeSignInFailed, oe.Message, map[string]any{"reason": api.SignInTenantNotAllowed})
	default:
		writeError(w, http.StatusBadGateway, api.CodeSignInUnavailable, oe.Message, nil)
	}
}

// firstUse records that a sign-in with this nonce was completed, and reports
// whether it is the first.
func (si *signIn) firstUse(nonce string, now time.Time) bool {
	si.mu.Lock()
	defer si.mu.Unlock()
	if si.nonces == nil {
		si.nonces = map[[sha256.Size]byte]time.Time{}
	}
	for k, at := range si.nonces {
		if now.Sub(at) > nonceKeptFor {
			delete(si.nonces, k)
		}
	}
	key := sha256.Sum256([]byte(nonce))
	if _, seen := si.nonces[key]; seen {
		return false
	}
	si.nonces[key] = now
	return true
}

// auditSignIn records a sign-in. It is not an authenticated request, so
// serve does not write it; the actor is the person the provider vouched
// for, whether or not a rule let them in. A sign-in the provider did not
// vouch for has no actor and is not recorded.
func (s *Server) auditSignIn(r *http.Request, email, outcome string, status int, code, detail string) {
	e := api.AuditEntry{At: s.now().UTC(), Actor: api.Actor{Kind: api.ActorUser, Name: email},
		Address: clientAddress(r.RemoteAddr), ForwardedFor: forwardedFor(r),
		Action: "signin", Outcome: outcome, Status: status, Code: code, Detail: detail}
	if _, err := s.store.AddAuditEntry(context.WithoutCancel(r.Context()), e); err != nil {
		s.log.Error("could not write the audit entry", "action", e.Action, "actor", e.Actor.Name, "error", err)
	}
}

// newSession draws a session's value, as newToken draws a token's.
func newSession() (value string, hash []byte, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("generate session: %w", err)
	}
	value = api.SessionPrefix + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(value))
	return value, sum[:], nil
}

// sessionIdentity is the caller a session stands for.
func sessionIdentity(sess store.Session) api.TokenIdentity {
	who := identityOf(sess.Email, sess.Role, sess.Applications, &sess.ExpiresAt)
	who.Kind = api.ActorUser
	return who
}

// identifySession resolves the hash of a presented credential to the session
// it is. A session is what the rules gave the person when they signed in,
// and stands only while the rules still give the same: each request asks
// them again, so that taking a rule away, or changing it, ends the session
// with the next request and not when it expires. ended is why a session that
// is known no longer works.
func (s *Server) identifySession(ctx context.Context, presented [sha256.Size]byte) (who api.TokenIdentity, ended string, known bool) {
	sess, err := s.store.GetSessionByHash(ctx, presented[:])
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("could not look up the session", "error", err)
		}
		return api.TokenIdentity{}, "", false
	}
	if subtle.ConstantTimeCompare(sess.Hash, presented[:]) != 1 {
		return api.TokenIdentity{}, "", false
	}
	who = sessionIdentity(sess)
	now := s.now()
	switch {
	case sess.EndedAt != nil:
		return who, sess.EndedReason, true
	case !now.Before(sess.ExpiresAt):
		return who, "", true
	}
	rules, err := s.store.AccessRules(ctx)
	if err != nil {
		s.log.Error("could not read the access rules", "error", err)
		return api.TokenIdentity{}, "", false
	}
	grant, ok := api.ResolveAccess(rules, sess.Email, sess.Groups)
	switch {
	// A name read from another claim than the agent reads now is not the
	// name the rules are written for, and a tenant taken off the list is
	// access taken away like a rule.
	case !s.signIn.stillVouchedFor(sess):
		ended = api.SessionEndedSignInChanged
	case !ok:
		ended = api.SessionEndedRuleRemoved
	case !grant.Equal(api.Grant{Role: sess.Role, Applications: sess.Applications}):
		ended = api.SessionEndedAccessChanged
	}
	if ended != "" {
		if err := s.store.EndSession(context.WithoutCancel(ctx), sess.ID, ended, now); err != nil {
			s.log.Error("could not end the session", "user", sess.Email, "error", err)
		}
		s.log.Info("session ended", "user", sess.Email, "reason", ended)
		return who, ended, true
	}
	s.recordSessionUse(ctx, sess)
	return who, "", true
}

// recordSessionUse is recordUse for a session.
func (s *Server) recordSessionUse(ctx context.Context, sess store.Session) {
	now := s.now()
	s.signIn.mu.Lock()
	if s.signIn.used == nil {
		s.signIn.used = map[int64]time.Time{}
	}
	last, seen := s.signIn.used[sess.ID]
	if !seen && sess.LastUsedAt != nil {
		last, seen = *sess.LastUsedAt, true
	}
	if seen && now.Sub(last) < lastUsedResolution {
		s.signIn.mu.Unlock()
		return
	}
	s.signIn.used[sess.ID] = now
	// Sessions come and go by the day: what is over is not worth remembering.
	for id, at := range s.signIn.used {
		if now.Sub(at) > api.SessionLifetime {
			delete(s.signIn.used, id)
		}
	}
	s.signIn.mu.Unlock()

	if err := s.store.TouchSession(context.WithoutCancel(ctx), sess.ID, now); err != nil {
		s.log.Warn("could not record session use", "user", sess.Email, "error", err)
	}
}

// refuseEnded answers a session that was ended before its time. Only someone
// who holds the session gets here, as with an expired token.
func (s *Server) refuseEnded(w http.ResponseWriter, who api.TokenIdentity, reason string) {
	var message string
	switch reason {
	case api.SessionEndedRuleRemoved:
		message = "no rule on this server gives " + who.Name + " a role any more. An admin grants one with: shipwick access grant " + api.WhoOf(who.Name) + " --role read"
	case api.SessionEndedAccessChanged:
		message = "what " + who.Name + " may do on this server was changed. Sign in again"
	case api.SessionEndedSignInChanged:
		message = "how people sign in to this server was changed. Sign in again"
	default:
		message = "an admin ended this session. Sign in again"
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="shipwick", error="invalid_token"`)
	writeError(w, http.StatusUnauthorized, api.CodeSessionEnded, message, map[string]any{"name": who.Name, "reason": reason})
}

func (s *Server) refuseExpiredSession(w http.ResponseWriter, who api.TokenIdentity) {
	at := who.ExpiresAt.UTC()
	w.Header().Set("WWW-Authenticate", `Bearer realm="shipwick", error="invalid_token"`)
	writeError(w, http.StatusUnauthorized, api.CodeSessionExpired,
		"the session of "+who.Name+" expired on "+at.Format("2006-01-02 at 15:04 UTC")+". Sign in again",
		map[string]any{"name": who.Name, "expired_at": at.Format(time.RFC3339)})
}

// credential is what the caller holds, for the sentences that refuse it.
func credential(who api.TokenIdentity) string {
	if who.Kind == api.ActorUser {
		return "session"
	}
	return "token"
}

// handleSignOut forgets the session the request was made with.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if principalFrom(r.Context()).Kind != api.ActorUser {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			"this request was made with an API token, which has no session to end; a token is revoked with: shipwick token revoke", nil)
		return
	}
	value, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(value))
	sess, err := s.store.GetSessionByHash(r.Context(), hash[:])
	if err == nil {
		err = s.store.DeleteSession(r.Context(), sess.ID)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeEngineError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAccess(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.AccessRules(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// handleGrantAccess stores a rule; one for the same subject is replaced,
// which is how a role is changed. Rules can be written before a provider is
// configured: they wait for it.
func (s *Server) handleGrantAccess(w http.ResponseWriter, r *http.Request) {
	var req api.GrantAccessRequest
	if !s.decodeOptionalBody(w, r, &req) {
		return
	}
	switch {
	case req.Subject == "" || req.Kind == "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `kind and subject are required, e.g. {"kind": "email", "subject": "ada@example.com", "role": "deploy"}`, nil)
		return
	case req.Role == "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "role is required: read, deploy or admin", nil)
		return
	}
	subject, err := api.ValidateAccessSubject(req.Kind, req.Subject)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := api.ValidateRole(req.Role); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if len(req.Applications) > 0 && req.Role != api.RoleDeploy {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "only the deploy role can be limited to applications: read changes nothing, and admin is for the whole server", nil)
		return
	}
	applications, err := api.ValidateTokenApplications(req.Role, req.Applications)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, strings.Replace(err.Error(), "a token", "a rule", 1), nil)
		return
	}
	rule := api.AccessRule{Kind: req.Kind, Subject: subject, Role: req.Role, Applications: applications,
		CreatedAt: s.now(), CreatedBy: principalFrom(r.Context()).Name}
	auditTarget(r, rule.Who())
	auditDetail(r, describeToken(rule.Role, rule.Applications, nil))

	rule, replaced, err := s.store.GrantAccess(r.Context(), rule)
	if errors.Is(err, store.ErrTooManyAccessRules) {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	} else if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	s.log.Info("access granted", "to", rule.Who(), "role", rule.Role, "applications", rule.Applications, "by", rule.CreatedBy)
	status := http.StatusCreated
	if replaced {
		status = http.StatusOK
	}
	writeJSON(w, status, rule)
}

func (s *Server) handleRevokeAccess(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the rule is named by its id, as GET /access/rules lists it", nil)
		return
	}
	rule, err := s.store.RevokeAccess(r.Context(), id)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	auditTarget(r, rule.Who())
	s.log.Info("access revoked", "from", rule.Who(), "by", principalFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleListSessions lists the sessions that would be served right now. One
// whose rule went or changed since its last request is left out: that
// request, when it comes, ends it.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ActiveSessions(r.Context(), s.now())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	rules, err := s.store.AccessRules(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	out := make([]api.Session, 0, len(sessions))
	for _, sess := range sessions {
		if grant, ok := api.ResolveAccess(rules, sess.Email, sess.Groups); !ok || !grant.Equal(api.Grant{Role: sess.Role, Applications: sess.Applications}) || !s.signIn.stillVouchedFor(sess) {
			continue
		}
		out = append(out, api.Session{ID: sess.ID, Email: sess.Email, Role: sess.Role, Applications: sess.Applications,
			CreatedAt: sess.CreatedAt, ExpiresAt: sess.ExpiresAt, LastUsedAt: sess.LastUsedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEndSessions signs a person out everywhere. It does not keep them
// out: while a rule covers them they can sign in again, with whatever the
// provider says about them then.
func (s *Server) handleEndSessions(w http.ResponseWriter, r *http.Request) {
	email, err := api.PersonName(s.signIn.nameClaim(), r.PathValue("email"))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	n, err := s.store.EndSessionsOf(r.Context(), email, api.SessionEndedSignedOut, s.now())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	auditDetail(r, strconv.Itoa(n)+" sessions")
	s.log.Info("sessions ended", "user", email, "sessions", n, "by", principalFrom(r.Context()).Name)
	writeJSON(w, http.StatusOK, api.SignedOut{Sessions: n})
}
