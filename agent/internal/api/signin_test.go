package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/oidc/oidctest"
	"github.com/shipwick/shipwick/pkg/api"
)

type signInFixture struct {
	*fixture
	idp   *oidctest.Provider
	clock time.Time
	// attempts numbers the sign-ins, so that each has a nonce of its own.
	attempts int
}

const testVerifier = "verifier-verifier-verifier-verifier-verifier-00"

func newSignInFixture(t *testing.T) *signInFixture {
	t.Helper()
	f := &signInFixture{fixture: newFixture(t), clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	now := func() time.Time { return f.clock }
	f.api.now = now
	f.idp = oidctest.New(t, now)
	f.api.UseSignIn(oidc.New(oidc.Config{Issuer: f.idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		Scopes: []string{"openid", "email", "profile"}, GroupsClaim: "groups", RedirectURL: oidctest.RedirectURL,
		Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: now}))
	return f
}

func exchangeBody(code, verifier, nonce, redirect string) string {
	body, _ := json.Marshal(api.SignInRequest{Code: code, CodeVerifier: verifier, Nonce: nonce, RedirectURI: redirect})
	return string(body)
}

// signIn takes a person through the provider and hands the agent what the
// dashboard's server would.
func (f *signInFixture) signIn(user oidctest.User) (int, []byte) {
	f.t.Helper()
	f.attempts++
	nonce := fmt.Sprintf("nonce-nonce-nonce-nonce-%04d", f.attempts)
	code := f.idp.Authorize(user, nonce, oidctest.Challenge(testVerifier))
	return f.doWithAuth("POST", "/api/v1/auth/exchange", exchangeBody(code, testVerifier, nonce, oidctest.RedirectURL), "")
}

// session signs a person in and returns the Authorization header of the
// session.
func (f *signInFixture) session(user oidctest.User) string {
	f.t.Helper()
	status, body := f.signIn(user)
	if status != http.StatusOK {
		f.t.Fatalf("sign in %s: status = %d, body = %s", user.Email, status, body)
	}
	return "Bearer " + decode[api.SignedIn](f.t, body).Session
}

func (f *signInFixture) grant(body string) api.AccessRule {
	f.t.Helper()
	status, answer := f.do("POST", "/api/v1/access/rules", body)
	if status != http.StatusCreated && status != http.StatusOK {
		f.t.Fatalf("grant %s: status = %d, body = %s", body, status, answer)
	}
	return decode[api.AccessRule](f.t, answer)
}

var (
	ada   = oidctest.User{Email: "Ada@Example.com", Groups: []string{"developers"}}
	grace = oidctest.User{Email: "grace@example.com"}
)

func TestAPersonWithARuleSignsInAndIsThatIdentity(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "group", "subject": "developers", "role": "deploy", "applications": ["my-api"]}`)

	status, body := f.signIn(ada)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	signed := decode[api.SignedIn](t, body)
	expires := f.clock.Add(10 * time.Hour)
	want := api.TokenIdentity{Kind: "user", Name: "ada@example.com", Role: api.RoleDeploy, Applications: []string{"my-api"}, ExpiresAt: &expires}
	if !strings.HasPrefix(signed.Session, "sws_") || len(signed.Session) != 47 || !reflect.DeepEqual(signed.Identity, want) {
		t.Fatalf("signed in = %+v, want identity %+v", signed, want)
	}
	auth := "Bearer " + signed.Session

	// The session is presented like a token, and GET /server says who it is.
	status, body = f.doWithAuth("GET", "/api/v1/server", "", auth)
	server := decode[api.Server](t, body)
	if status != http.StatusOK || !reflect.DeepEqual(server.Token, want) || !server.SignIn.Configured || server.SignIn.Issuer != f.idp.URL {
		t.Errorf("GET /server: status = %d, token = %+v, sign_in = %+v", status, server.Token, server.SignIn)
	}

	// What the access group built decides for a person as for a token.
	status, body = f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, auth)
	if d := decode[api.Deployment](t, body); status != http.StatusAccepted || d.By != "ada@example.com" {
		t.Errorf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	status, body = f.doWithAuth("POST", "/api/v1/applications/web/deploy", otherConfig, auth)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeTokenLimited || e.Message != "this session is limited to my-api; it can read web and not change it" {
		t.Errorf("deploy another application: status = %d, error = %+v", status, e)
	}
	status, body = f.doWithAuth("GET", "/api/v1/tokens", "", auth)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Message != "this session has the deploy role; this needs admin" {
		t.Errorf("list tokens: status = %d, error = %+v", status, e)
	}

	// And the audit trail names the person.
	var got []string
	for _, e := range f.audit("?actor=ada@example.com") {
		if e.Actor.Kind != "user" {
			t.Errorf("actor = %+v, want kind user", e.Actor)
		}
		got = append(got, e.Action+" "+e.Application+" "+e.Outcome+" "+e.Detail)
	}
	wantTrail := []string{"deploy web refused ", "deploy my-api ok deployment 1", "signin  ok role deploy, limited to my-api, expires 2026-10-01T19:00:00Z"}
	if !reflect.DeepEqual(got, wantTrail) {
		t.Errorf("audit trail = %q, want %q", got, wantTrail)
	}
}

func TestNobodyWithoutARuleGetsIn(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "email", "subject": "ada@example.com", "role": "admin"}`)

	status, body := f.signIn(grace)
	e := decodeError(t, body)
	want := "grace@example.com signed in, and no rule on this server gives that address a role. An admin grants one with: shipwick access grant grace@example.com --role read"
	if status != http.StatusForbidden || e.Code != api.CodeAccessNotGranted || e.Message != want || e.Details["email"] != "grace@example.com" {
		t.Errorf("status = %d, error = %+v", status, e)
	}
	if sessions := decode[[]api.Session](t, second(f.do("GET", "/api/v1/access/sessions", ""))); len(sessions) != 0 {
		t.Errorf("a refused sign-in left a session: %+v", sessions)
	}
	trail := f.audit("?actor=grace@example.com")
	if len(trail) != 1 || trail[0].Action != "signin" || trail[0].Outcome != api.AuditRefused || trail[0].Code != api.CodeAccessNotGranted || trail[0].Actor.Kind != "user" {
		t.Errorf("audit trail = %+v", trail)
	}
}

func second(_ int, body []byte) []byte { return body }

func TestASignInTheProviderDidNotVouchForIsRefused(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "domain", "subject": "example.com", "role": "read"}`)
	const nonce = "nonce-nonce-nonce-nonce-nonce-00"
	challenge := oidctest.Challenge(testVerifier)
	exchange := func(body string) (int, api.Error) {
		status, answer := f.doWithAuth("POST", "/api/v1/auth/exchange", body, "")
		return status, decodeError(t, answer)
	}

	// The nonce the dashboard kept is not the one the ID token answers.
	code := f.idp.Authorize(ada, "the-nonce-of-another-sign-in-000", challenge)
	if status, e := exchange(exchangeBody(code, testVerifier, nonce, oidctest.RedirectURL)); status != http.StatusUnauthorized || e.Code != api.CodeSignInFailed || e.Details["reason"] != api.SignInNonceMismatch {
		t.Errorf("a wrong nonce: status = %d, error = %+v", status, e)
	}
	// A code read off a callback, without the verifier.
	code = f.idp.Authorize(ada, nonce, challenge)
	if status, e := exchange(exchangeBody(code, "another-verifier-another-verifier-another-0000", nonce, oidctest.RedirectURL)); status != http.StatusUnauthorized || e.Details["reason"] != api.SignInCodeRejected {
		t.Errorf("a wrong verifier: status = %d, error = %+v", status, e)
	}
	// A code that was used.
	code = f.idp.Authorize(ada, nonce, challenge)
	if status, body := f.doWithAuth("POST", "/api/v1/auth/exchange", exchangeBody(code, testVerifier, nonce, oidctest.RedirectURL), ""); status != http.StatusOK {
		t.Fatalf("the sign-in itself: status = %d, body = %s", status, body)
	}
	if status, e := exchange(exchangeBody(code, testVerifier, nonce, oidctest.RedirectURL)); status != http.StatusUnauthorized || e.Details["reason"] != api.SignInCodeRejected {
		t.Errorf("a code used twice: status = %d, error = %+v", status, e)
	}
	// A provider that redeemed a code twice would still not get a second
	// session out of one sign-in: the agent takes each nonce once.
	code = f.idp.Authorize(ada, nonce, challenge)
	if status, e := exchange(exchangeBody(code, testVerifier, nonce, oidctest.RedirectURL)); status != http.StatusUnauthorized || e.Details["reason"] != api.SignInNonceReused {
		t.Errorf("a nonce used twice: status = %d, error = %+v", status, e)
	}
	// An ID token for another client, and one that expired.
	f.idp.Mutate(func(c map[string]any) { c["aud"] = "another-app" })
	if status, body := f.signIn(ada); status != http.StatusUnauthorized || decodeError(t, body).Details["reason"] != api.SignInInvalidIDToken {
		t.Errorf("another client's ID token: status = %d, body = %s", status, body)
	}
	f.idp.Mutate(func(c map[string]any) { c["exp"] = f.clock.Add(-time.Hour).Unix() })
	if status, body := f.signIn(ada); status != http.StatusUnauthorized || decodeError(t, body).Details["reason"] != api.SignInInvalidIDToken {
		t.Errorf("an expired ID token: status = %d, body = %s", status, body)
	}
	// An address the provider does not vouch for, and no address at all.
	f.idp.Mutate(func(c map[string]any) { c["email_verified"] = false })
	if status, body := f.signIn(ada); status != http.StatusUnauthorized || decodeError(t, body).Details["reason"] != api.SignInEmailNotVerified {
		t.Errorf("an unverified address: status = %d, body = %s", status, body)
	}
	f.idp.Mutate(func(c map[string]any) { delete(c, "email") })
	if status, body := f.signIn(ada); status != http.StatusUnauthorized || decodeError(t, body).Details["reason"] != api.SignInEmailMissing {
		t.Errorf("no address: status = %d, body = %s", status, body)
	}
	f.idp.Mutate(nil)

	// The redirect URI is the configured one, not what a request says.
	code = f.idp.Authorize(ada, nonce, challenge)
	before := len(f.idp.Requests())
	status, e := exchange(exchangeBody(code, testVerifier, "nonce-nonce-nonce-nonce-nonce-01", "https://evil.example.com/auth/callback"))
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, oidctest.RedirectURL) || len(f.idp.Requests()) != before {
		t.Errorf("another redirect URI: status = %d, error = %+v, provider asked %v", status, e, f.idp.Requests()[before:])
	}
	for _, body := range []string{"", "{", `{"code": "c"}`, exchangeBody("c", "short", nonce, oidctest.RedirectURL), exchangeBody("c", testVerifier, "short", oidctest.RedirectURL),
		`{"code": "c", "client_secret": "x"}`} {
		if status, e := exchange(body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("body %q: status = %d, error = %+v", body, status, e)
		}
	}

	// Only the one sign-in that succeeded left a session and an entry.
	if sessions := decode[[]api.Session](t, second(f.do("GET", "/api/v1/access/sessions", ""))); len(sessions) != 1 {
		t.Errorf("sessions = %+v, want one", sessions)
	}
	// Neither the client secret, a code, a verifier nor a session is logged.
	for _, secret := range []string{oidctest.ClientSecret, testVerifier, "sws_", "code-"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Errorf("%q appears in the log:\n%s", secret, f.logs)
		}
	}
}

func TestRemovingOrChangingTheRuleEndsTheSessionAtOnce(t *testing.T) {
	f := newSignInFixture(t)
	rule := f.grant(`{"kind": "group", "subject": "developers", "role": "deploy"}`)
	domain := f.grant(`{"kind": "domain", "subject": "example.com", "role": "read"}`)
	adaAuth, graceAuth := f.session(ada), f.session(grace)

	// A rule that changes nothing for the person leaves the session alone.
	f.grant(`{"kind": "group", "subject": "sales", "role": "read"}`)
	if status, body := f.doWithAuth("GET", "/api/v1/server", "", adaAuth); status != http.StatusOK {
		t.Fatalf("after an unrelated rule: status = %d, body = %s", status, body)
	}

	// The group's rule goes: ada still has the domain's, which gives less.
	if status, body := f.do("DELETE", fmt.Sprintf("/api/v1/access/rules/%d", rule.ID), ""); status != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, body = %s", status, body)
	}
	// The list of who is signed in does not wait for ada's next request.
	if sessions := decode[[]api.Session](t, second(f.do("GET", "/api/v1/access/sessions", ""))); len(sessions) != 1 || sessions[0].Email != "grace@example.com" {
		t.Errorf("sessions after the rule went = %+v; want grace's alone", sessions)
	}
	status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, adaAuth)
	e := decodeError(t, body)
	if status != http.StatusUnauthorized || e.Code != api.CodeSessionEnded || e.Details["reason"] != api.SessionEndedAccessChanged ||
		e.Message != "what ada@example.com may do on this server was changed. Sign in again" {
		t.Errorf("after the role changed: status = %d, error = %+v", status, e)
	}
	if apps := decodeList(t, f.fixture); len(apps) != 0 {
		t.Errorf("the ended session deployed: %+v", apps)
	}
	// Signing in again gives what the rules give now.
	if who := decode[api.SignedIn](t, second(f.signIn(ada))).Identity; who.Role != api.RoleRead {
		t.Errorf("signed in again as %+v, want read", who)
	}

	// The last rule goes: nothing covers grace any more.
	f.do("DELETE", fmt.Sprintf("/api/v1/access/rules/%d", domain.ID), "")
	status, body = f.doWithAuth("GET", "/api/v1/applications", "", graceAuth)
	e = decodeError(t, body)
	if status != http.StatusUnauthorized || e.Code != api.CodeSessionEnded || e.Details["reason"] != api.SessionEndedRuleRemoved ||
		!strings.Contains(e.Message, "shipwick access grant grace@example.com --role read") {
		t.Errorf("after the rule was removed: status = %d, error = %+v", status, e)
	}
	// Granting it again does not bring the session back.
	f.grant(`{"kind": "domain", "subject": "example.com", "role": "read"}`)
	if status, body := f.doWithAuth("GET", "/api/v1/applications", "", graceAuth); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeSessionEnded {
		t.Errorf("after the rule came back: status = %d, body = %s", status, body)
	}
}

func TestASessionLastsTenHours(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "email", "subject": "ada@example.com", "role": "admin"}`)
	auth := f.session(ada)

	f.clock = f.clock.Add(10*time.Hour - time.Second)
	if status, body := f.doWithAuth("GET", "/api/v1/server", "", auth); status != http.StatusOK {
		t.Fatalf("a second before the end: status = %d, body = %s", status, body)
	}
	f.clock = f.clock.Add(time.Second)
	status, body := f.doWithAuth("GET", "/api/v1/server", "", auth)
	e := decodeError(t, body)
	if status != http.StatusUnauthorized || e.Code != api.CodeSessionExpired || e.Details["name"] != "ada@example.com" || e.Details["expired_at"] != "2026-10-01T19:00:00Z" ||
		e.Message != "the session of ada@example.com expired on 2026-10-01 at 19:00 UTC. Sign in again" {
		t.Errorf("at the end: status = %d, error = %+v", status, e)
	}
}

func TestBadSessionsAreCountedLikeBadTokensAndEndedOnesAreNot(t *testing.T) {
	f := newSignInFixture(t)
	rule := f.grant(`{"kind": "email", "subject": "ada@example.com", "role": "read"}`)
	auth := f.session(ada)
	f.do("DELETE", fmt.Sprintf("/api/v1/access/rules/%d", rule.ID), "")

	// Whoever holds an ended session is told so however often they ask.
	for range failedAuthLimit + 5 {
		if status, body := f.doWithAuth("GET", "/api/v1/server", "", auth); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeSessionEnded {
			t.Fatalf("an ended session: status = %d, body = %s", status, body)
		}
	}
	// A session nobody issued is a wrong token, and counted.
	for i := range failedAuthLimit {
		if status, body := f.doWithAuth("GET", "/api/v1/server", "", "Bearer sws_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeUnauthorized {
			t.Fatalf("attempt %d with an invented session: status = %d, body = %s", i, status, body)
		}
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/server", "", "Bearer sws_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); status != http.StatusTooManyRequests {
		t.Errorf("after %d invented sessions: status = %d, want 429", failedAuthLimit, status)
	}
	// A limited address gets no further request to the provider out of the agent.
	before := len(f.idp.Requests())
	if status, body := f.signIn(ada); status != http.StatusTooManyRequests || len(f.idp.Requests()) != before {
		t.Errorf("a sign-in from a limited address: status = %d, body = %s", status, body)
	}
}

func TestFailedSignInsAreCountedAndTheProviderIsLeftAlone(t *testing.T) {
	f := newSignInFixture(t)
	for i := range failedAuthLimit {
		body := exchangeBody("an-invented-code", testVerifier, fmt.Sprintf("nonce-nonce-nonce-nonce-%04d", i), oidctest.RedirectURL)
		if status, _ := f.doWithAuth("POST", "/api/v1/auth/exchange", body, ""); status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d", i, status)
		}
	}
	before := len(f.idp.Requests())
	status, body := f.doWithAuth("POST", "/api/v1/auth/exchange", exchangeBody("an-invented-code", testVerifier, "nonce-nonce-nonce-nonce-9999", oidctest.RedirectURL), "")
	if status != http.StatusTooManyRequests || decodeError(t, body).Code != api.CodeRateLimited || len(f.idp.Requests()) != before {
		t.Errorf("after %d failed sign-ins: status = %d, body = %s", failedAuthLimit, status, body)
	}
	// A valid token is never refused, as before.
	if status, _ := f.do("GET", "/api/v1/server", ""); status != http.StatusOK {
		t.Errorf("the root token from a limited address: status = %d", status)
	}
}

func TestSigningOutForgetsTheSessionAndAnAdminCanSignAPersonOut(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "domain", "subject": "example.com", "role": "read"}`)
	laptop, phone, other := f.session(ada), f.session(ada), f.session(grace)

	if status, body := f.doWithAuth("DELETE", "/api/v1/auth/session", "", laptop); status != http.StatusNoContent {
		t.Fatalf("sign out: status = %d, body = %s", status, body)
	}
	if status, body := f.doWithAuth("GET", "/api/v1/server", "", laptop); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeUnauthorized {
		t.Errorf("a session its holder ended: status = %d, body = %s", status, body)
	}
	if status, body := f.do("DELETE", "/api/v1/auth/session", ""); status != http.StatusBadRequest || !strings.Contains(decodeError(t, body).Message, "shipwick token revoke") {
		t.Errorf("signing out with a token: status = %d, body = %s", status, body)
	}

	f.doWithAuth("GET", "/api/v1/server", "", phone)
	sessions := decode[[]api.Session](t, second(f.do("GET", "/api/v1/access/sessions", "")))
	if len(sessions) != 2 || sessions[0].Email != "ada@example.com" || sessions[0].Role != api.RoleRead || sessions[0].LastUsedAt == nil ||
		!sessions[0].ExpiresAt.Equal(f.clock.Add(10*time.Hour)) || sessions[1].LastUsedAt != nil {
		t.Fatalf("sessions = %+v", sessions)
	}

	status, body := f.do("DELETE", "/api/v1/access/sessions/Ada@Example.com", "")
	if out := decode[api.SignedOut](t, body); status != http.StatusOK || out.Sessions != 1 {
		t.Fatalf("sign ada out: status = %d, body = %s", status, body)
	}
	status, body = f.doWithAuth("GET", "/api/v1/server", "", phone)
	if e := decodeError(t, body); status != http.StatusUnauthorized || e.Code != api.CodeSessionEnded || e.Details["reason"] != api.SessionEndedSignedOut {
		t.Errorf("a session an admin ended: status = %d, body = %s", status, body)
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/server", "", other); status != http.StatusOK {
		t.Errorf("another person's session: status = %d", status)
	}
	if status, _ := f.do("DELETE", "/api/v1/access/sessions/not-an-address", ""); status != http.StatusBadRequest {
		t.Errorf("an invalid address: status = %d", status)
	}
}

func TestAccessRulesAreValidatedAndAudited(t *testing.T) {
	f := newSignInFixture(t)
	for body, want := range map[string]string{
		`{}`: "kind and subject are required",
		`{"kind": "email", "subject": "ada@example.com"}`:                                             "role is required",
		`{"kind": "email", "subject": "not an address", "role": "read"}`:                              "not an e-mail address",
		`{"kind": "domain", "subject": "*.example.com", "role": "read"}`:                              "invalid domain",
		`{"kind": "group", "subject": " padded", "role": "read"}`:                                     "invalid group",
		`{"kind": "everyone", "subject": "x", "role": "read"}`:                                        "invalid kind",
		`{"kind": "email", "subject": "ada@example.com", "role": "owner"}`:                            "invalid role",
		`{"kind": "email", "subject": "ada@example.com", "role": "admin", "applications": ["web"]}`:   "only the deploy role can be limited",
		`{"kind": "email", "subject": "ada@example.com", "role": "deploy", "applications": ["Web!"]}`: "applications:",
	} {
		status, answer := f.do("POST", "/api/v1/access/rules", body)
		if e := decodeError(t, answer); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, want) {
			t.Errorf("%s: status = %d, error = %+v; want %q", body, status, e, want)
		}
	}

	status, body := f.do("POST", "/api/v1/access/rules", `{"kind": "email", "subject": "Ada@Example.com", "role": "deploy", "applications": ["web", "my-api", "web"]}`)
	rule := decode[api.AccessRule](t, body)
	if status != http.StatusCreated || rule.Subject != "ada@example.com" || !reflect.DeepEqual(rule.Applications, []string{"my-api", "web"}) || rule.CreatedBy != "root" || !rule.CreatedAt.Equal(f.clock) {
		t.Fatalf("grant: status = %d, rule = %+v", status, rule)
	}
	if status, body := f.do("POST", "/api/v1/access/rules", `{"kind": "email", "subject": "ada@example.com", "role": "admin"}`); status != http.StatusOK || decode[api.AccessRule](t, body).ID != rule.ID {
		t.Errorf("grant again: status = %d, body = %s; want 200 and the same rule", status, body)
	}
	f.grant(`{"kind": "group", "subject": "/platform", "role": "admin"}`)
	rules := decode[[]api.AccessRule](t, second(f.do("GET", "/api/v1/access/rules", "")))
	if len(rules) != 2 || rules[0].Role != api.RoleAdmin || len(rules[0].Applications) != 0 || rules[1].Who() != "group:/platform" {
		t.Errorf("rules = %+v", rules)
	}
	if status, _ := f.do("DELETE", "/api/v1/access/rules/2", ""); status != http.StatusNoContent {
		t.Errorf("revoke: status = %d", status)
	}
	if status, body := f.do("DELETE", "/api/v1/access/rules/2", ""); status != http.StatusNotFound {
		t.Errorf("revoke twice: status = %d, body = %s", status, body)
	}
	if status, _ := f.do("DELETE", "/api/v1/access/rules/platform", ""); status != http.StatusBadRequest {
		t.Errorf("revoke by a name: status = %d", status)
	}

	var got []string
	for _, e := range f.audit("") {
		if strings.HasPrefix(e.Action, "access.") && e.Outcome == api.AuditOK {
			got = append(got, e.Action+" "+e.Target+" | "+e.Detail)
		}
	}
	want := []string{"access.revoke group:/platform | ", "access.grant group:/platform | role admin", "access.grant ada@example.com | role admin",
		"access.grant ada@example.com | role deploy, limited to my-api web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("audit trail = %q, want %q", got, want)
	}

	// Managing access is admin's.
	f.grant(`{"kind": "domain", "subject": "example.com", "role": "deploy"}`)
	auth := f.session(grace)
	for _, tt := range []struct{ method, path string }{{"GET", "/api/v1/access/rules"}, {"POST", "/api/v1/access/rules"}, {"DELETE", "/api/v1/access/rules/1"},
		{"GET", "/api/v1/access/sessions"}, {"DELETE", "/api/v1/access/sessions/ada@example.com"}} {
		if status, body := f.doWithAuth(tt.method, tt.path, `{"kind": "email", "subject": "grace@example.com", "role": "admin"}`, auth); status != http.StatusForbidden {
			t.Errorf("%s %s as deploy: status = %d, body = %s", tt.method, tt.path, status, body)
		}
	}
}

func TestWithoutAProviderNothingChanges(t *testing.T) {
	f := newFixture(t)
	status, body := f.doWithAuth("GET", "/api/v1/auth", "", "")
	if cfg := decode[api.SignInConfig](t, body); status != http.StatusOK || cfg.Configured || cfg.Scopes == nil {
		t.Errorf("GET /auth: status = %d, body = %s", status, body)
	}
	status, body = f.doWithAuth("POST", "/api/v1/auth/exchange", exchangeBody("c", testVerifier, "nonce-nonce-nonce-nonce-0000", "https://x/auth/callback"), "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeSignInNotConfigured {
		t.Errorf("POST /auth/exchange: status = %d, error = %+v", status, e)
	}
	server := decode[api.Server](t, second(f.do("GET", "/api/v1/server", "")))
	if server.SignIn.Configured || server.Token.Kind != api.ActorToken {
		t.Errorf("GET /server: sign_in = %+v, token = %+v", server.SignIn, server.Token)
	}
	// Rules can be prepared before the provider is.
	if status, body := f.do("POST", "/api/v1/access/rules", `{"kind": "email", "subject": "ada@example.com", "role": "read"}`); status != http.StatusCreated {
		t.Errorf("grant without a provider: status = %d, body = %s", status, body)
	}
}

func TestTheDashboardLearnsWhereToSendABrowserWithoutAToken(t *testing.T) {
	f := newSignInFixture(t)
	status, body := f.doWithAuth("GET", "/api/v1/auth", "", "")
	want := api.SignInConfig{Configured: true, Issuer: f.idp.URL, AuthorizationEndpoint: f.idp.URL + "/authorize", ClientID: oidctest.ClientID,
		Scopes: []string{"openid", "email", "profile"}, RedirectURI: oidctest.RedirectURL}
	if got := decode[api.SignInConfig](t, body); status != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("GET /auth: status = %d, config = %+v, want %+v", status, got, want)
	}
	if strings.Contains(string(body), oidctest.ClientSecret) {
		t.Errorf("GET /auth returns the client secret: %s", body)
	}

	f.idp.Down(true)
	f.clock = f.clock.Add(2 * time.Hour)
	status, body = f.doWithAuth("GET", "/api/v1/auth", "", "")
	if e := decodeError(t, body); status != http.StatusBadGateway || e.Code != api.CodeSignInUnavailable {
		t.Errorf("provider down: status = %d, error = %+v", status, e)
	}
	f.grant(`{"kind": "email", "subject": "ada@example.com", "role": "read"}`)
	if status, body := f.signIn(ada); status != http.StatusBadGateway || decodeError(t, body).Code != api.CodeSignInUnavailable {
		t.Errorf("sign in with the provider down: status = %d, body = %s", status, body)
	}
	// The provider being away is not held against the address.
	if wait, limited := f.api.limiter.limited("127.0.0.1"); limited {
		t.Errorf("limited for %s after the provider was unreachable", wait)
	}
}
