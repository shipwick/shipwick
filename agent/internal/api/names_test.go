package api

import (
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

const (
	contoso  = "11111111-1111-4111-8111-111111111111"
	fabrikam = "22222222-2222-4222-8222-222222222222"
)

// useNames reconfigures the fixture's agent, as an operator who changes
// SHIPWICK_OIDC_NAME_CLAIM or SHIPWICK_OIDC_TENANTS and restarts it would.
func (f *signInFixture) useNames(claim string, tenants ...string) {
	issuer := f.idp.URL
	if len(tenants) > 0 {
		f.idp.ServeTenants()
		issuer = f.idp.MultiTenantIssuer()
	}
	f.api.UseSignIn(oidc.New(oidc.Config{Issuer: issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		Scopes: []string{"openid", "profile"}, GroupsClaim: "groups", RedirectURL: oidctest.RedirectURL, NameClaim: claim, Tenants: tenants,
		Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: func() time.Time { return f.clock }}))
}

func named(claim, value string) oidctest.User {
	return oidctest.User{Subject: "sub-of-" + value, Claims: map[string]any{claim: value}}
}

func TestAnAccountWithoutAnAddressSignsInUnderTheClaimTheOperatorChose(t *testing.T) {
	f := newSignInFixture(t)
	f.useNames("preferred_username")
	f.grant(`{"kind": "name", "subject": "Ada.Lovelace", "role": "deploy", "applications": ["my-api"]}`)

	// email_verified says nothing about a name that is not the email claim.
	unverified := false
	user := named("preferred_username", "Ada.Lovelace")
	user.EmailVerified = &unverified
	status, body := f.signIn(user)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	signed := decode[api.SignedIn](t, body)
	if signed.Identity.Kind != api.ActorUser || signed.Identity.Name != "Ada.Lovelace" || signed.Identity.Role != api.RoleDeploy {
		t.Fatalf("identity = %+v", signed.Identity)
	}
	auth := "Bearer " + signed.Session

	// The name is what a deployment's "by" and the audit trail show.
	status, body = f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, auth)
	if d := decode[api.Deployment](t, body); status != http.StatusAccepted || d.By != "Ada.Lovelace" {
		t.Errorf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	var got []string
	for _, e := range f.audit("?actor=Ada.Lovelace") {
		got = append(got, e.Actor.Kind+" "+e.Action+" "+e.Outcome)
	}
	if !reflect.DeepEqual(got, []string{"user deploy ok", "user signin ok"}) {
		t.Errorf("audit trail = %q", got)
	}

	_, body = f.do("GET", "/api/v1/server", "")
	if signIn := decode[api.Server](t, body).SignIn; !signIn.Configured || signIn.NameClaim != "preferred_username" {
		t.Errorf("sign_in = %+v", signIn)
	}
	_, body = f.do("GET", "/api/v1/access/sessions", "")
	if sessions := decode[[]api.Session](t, body); len(sessions) != 1 || sessions[0].Email != "Ada.Lovelace" {
		t.Errorf("sessions = %+v", sessions)
	}

	// A name is matched as it is: another capital is another person.
	status, body = f.do("DELETE", "/api/v1/access/sessions/ada.lovelace", "")
	if out := decode[api.SignedOut](t, body); status != http.StatusOK || out.Sessions != 0 {
		t.Errorf("signing out another spelling: status = %d, body = %s", status, body)
	}
	status, body = f.do("DELETE", "/api/v1/access/sessions/Ada.Lovelace", "")
	if out := decode[api.SignedOut](t, body); status != http.StatusOK || out.Sessions != 1 {
		t.Errorf("signing the person out: status = %d, body = %s", status, body)
	}
	status, body = f.signIn(named("preferred_username", "ada.lovelace"))
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeAccessNotGranted || e.Details["name"] != "ada.lovelace" ||
		!strings.HasSuffix(e.Message, "no rule on this server gives that name a role. An admin grants one with: shipwick access grant name:ada.lovelace --role read") {
		t.Errorf("another spelling signing in: status = %d, error = %+v", status, e)
	}
}

func TestRulesByAddressAndDomainApplyToNamesThatAreAddresses(t *testing.T) {
	f := newSignInFixture(t)
	f.useNames("upn")
	f.grant(`{"kind": "domain", "subject": "corp.example", "role": "read"}`)
	f.grant(`{"kind": "email", "subject": "Grace@Corp.Example", "role": "admin"}`)
	f.grant(`{"kind": "name", "subject": "svc-deploy", "role": "deploy"}`)

	for upn, want := range map[string]api.Role{
		"Ada@Corp.Example":   api.RoleRead,   // the domain, whatever the capitals
		"grace@corp.example": api.RoleAdmin,  // the address before the domain
		"GRACE@corp.example": api.RoleAdmin,  // an address is an address in any case
		"svc-deploy":         api.RoleDeploy, // not an address: by name
	} {
		status, body := f.signIn(named("upn", upn))
		if signed := decode[api.SignedIn](t, body); status != http.StatusOK || signed.Identity.Role != want || signed.Identity.Name != upn {
			t.Errorf("%s: status = %d, identity = %+v, want %s under the name as the provider wrote it", upn, status, signed.Identity, want)
		}
	}
	for _, upn := range []string{"svc-Deploy", "ada@other.example", "corp.example", "@corp.example"} {
		if status, body := f.signIn(named("upn", upn)); status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, body = %s; want no access", upn, status, body)
		}
	}
}

func TestANameThatCannotStandInTheTrailIsRefused(t *testing.T) {
	f := newSignInFixture(t)
	f.useNames("preferred_username")
	f.grant(`{"kind": "domain", "subject": "example.com", "role": "admin"}`)

	users := map[string]oidctest.User{
		"no such claim":        {Email: "ada@example.com", Claims: map[string]any{"nickname": "ada"}},
		"an empty name":        named("preferred_username", ""),
		"a name with a space":  named("preferred_username", "ada lovelace"),
		"a line break":         named("preferred_username", "ada\nlevel=error msg=forged"),
		"a control character":  named("preferred_username", "ada\x1b[31m"),
		"letters beyond ASCII": named("preferred_username", "adа"),
		"too long":             named("preferred_username", strings.Repeat("a", 255)),
		"not a string":         {Subject: "s", Claims: map[string]any{"preferred_username": []string{"ada"}}},
	}
	for name, user := range users {
		status, body := f.signIn(user)
		e := decodeError(t, body)
		if status != http.StatusUnauthorized || e.Code != api.CodeSignInFailed || e.Details["reason"] != api.SignInNameMissing || e.Details["claim"] != "preferred_username" {
			t.Errorf("%s: status = %d, error = %+v", name, status, e)
		}
		if strings.Contains(e.Message, "forged") || strings.Contains(e.Message, "\x1b") {
			t.Errorf("%s: the answer repeats the value: %q", name, e.Message)
		}
	}
	if strings.Contains(f.logs.String(), "forged") {
		t.Errorf("a refused name reached the log:\n%s", f.logs)
	}
	if entries := f.audit("?action=signin"); len(entries) != 0 {
		t.Errorf("a sign-in without a name is in the trail: %+v", entries)
	}

	// With the email claim, nothing of this applies and 0.6 stands: an
	// account without an address is refused as email_missing.
	g := newSignInFixture(t)
	status, body := g.signIn(named("preferred_username", "ada"))
	if e := decodeError(t, body); status != http.StatusUnauthorized || e.Details["reason"] != api.SignInEmailMissing || !strings.Contains(e.Message, "SHIPWICK_OIDC_NAME_CLAIM") {
		t.Errorf("email claim, no address: status = %d, error = %+v", status, e)
	}
}

func TestANameRuleAndAnAddressRuleAreStoredAsTheyAreMatched(t *testing.T) {
	f := newSignInFixture(t)
	for body, want := range map[string]string{
		`{"kind": "name", "subject": "AAAA-bbbb_cccc", "role": "read"}`:   "name:AAAA-bbbb_cccc",
		`{"kind": "name", "subject": "auth0|5f7c8ec7", "role": "read"}`:   "name:auth0|5f7c8ec7",
		`{"kind": "email", "subject": "Ada@Example.com", "role": "read"}`: "ada@example.com",
	} {
		if rule := f.grant(body); rule.Who() != want {
			t.Errorf("%s stored as %s, want %s", body, rule.Who(), want)
		}
	}
	for _, body := range []string{
		`{"kind": "name", "subject": "ada lovelace", "role": "read"}`,
		`{"kind": "name", "subject": "", "role": "read"}`,
		`{"kind": "name", "subject": "ada\u0000", "role": "read"}`,
		`{"kind": "name", "subject": "` + strings.Repeat("a", 255) + `", "role": "read"}`,
		`{"kind": "person", "subject": "ada", "role": "read"}`,
	} {
		status, answer := f.do("POST", "/api/v1/access/rules", body)
		if e := decodeError(t, answer); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("%s: status = %d, error = %+v", body, status, e)
		}
	}
}

func TestASessionEndsWhenTheAgentNamesPeopleByAnotherClaim(t *testing.T) {
	f := newSignInFixture(t)
	f.grant(`{"kind": "email", "subject": "ada@example.com", "role": "admin"}`)
	auth := f.session(oidctest.User{Email: "ada@example.com"})
	if status, _ := f.doWithAuth("GET", "/api/v1/tokens", "", auth); status != http.StatusOK {
		t.Fatalf("the session before the change: status = %d", status)
	}

	// The operator switches to subject identifiers. The session's name was
	// an address the provider reported; what the rules are now written for
	// is something else.
	f.useNames("sub")
	status, body := f.doWithAuth("GET", "/api/v1/tokens", "", auth)
	e := decodeError(t, body)
	if status != http.StatusUnauthorized || e.Code != api.CodeSessionEnded || e.Details["reason"] != api.SessionEndedSignInChanged ||
		e.Message != "how people sign in to this server was changed. Sign in again" {
		t.Errorf("the session after the change: status = %d, error = %+v", status, e)
	}
	// Ended for good: going back does not revive it.
	f.useNames("email")
	if status, body := f.doWithAuth("GET", "/api/v1/tokens", "", auth); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeSessionEnded {
		t.Errorf("the session after the change was undone: status = %d, body = %s", status, body)
	}
}

func TestOnlyAccountsOfListedTenantsSignInAndASessionEndsWithItsTenant(t *testing.T) {
	f := newSignInFixture(t)
	f.useNames("sub", contoso, fabrikam)
	f.grant(`{"kind": "name", "subject": "AAAA-ada", "role": "deploy"}`)
	f.grant(`{"kind": "name", "subject": "BBBB-eve", "role": "admin"}`)

	ada := oidctest.User{Subject: "AAAA-ada", Tenant: contoso}
	status, body := f.signIn(ada)
	if signed := decode[api.SignedIn](t, body); status != http.StatusOK || signed.Identity.Name != "AAAA-ada" {
		t.Fatalf("an account of a listed tenant: status = %d, body = %s", status, body)
	}
	auth := "Bearer " + decode[api.SignedIn](t, body).Session
	if entries := f.audit("?action=signin"); len(entries) != 1 || !strings.HasSuffix(entries[0].Detail, ", tenant "+contoso) {
		t.Errorf("the sign-in in the trail: %+v", entries)
	}

	// A rule does not help an account of a tenant that is not listed.
	status, body = f.signIn(oidctest.User{Subject: "BBBB-eve", Tenant: "33333333-3333-4333-8333-333333333333"})
	if e := decodeError(t, body); status != http.StatusUnauthorized || e.Code != api.CodeSignInFailed || e.Details["reason"] != api.SignInTenantNotAllowed {
		t.Errorf("an account of another tenant: status = %d, error = %+v", status, e)
	}

	// Taking the tenant off the list ends its sessions with their next request.
	f.useNames("sub", fabrikam)
	status, body = f.doWithAuth("GET", "/api/v1/applications", "", auth)
	if e := decodeError(t, body); status != http.StatusUnauthorized || e.Code != api.CodeSessionEnded || e.Details["reason"] != api.SessionEndedSignInChanged {
		t.Errorf("a session of a tenant taken off the list: status = %d, error = %+v", status, e)
	}
	if status, body := f.signIn(ada); status != http.StatusUnauthorized || decodeError(t, body).Details["reason"] != api.SignInTenantNotAllowed {
		t.Errorf("signing in again from that tenant: status = %d, body = %s", status, body)
	}
}

// With every tenant accepted, what an account says of itself beyond its
// identifier comes from a directory nobody here chose.
func TestGroupsOfAnAccountFromAnyTenantGiveNothing(t *testing.T) {
	f := newSignInFixture(t)
	f.useNames("sub", oidc.AnyTenant)
	f.grant(`{"kind": "group", "subject": "platform", "role": "admin"}`)
	f.grant(`{"kind": "name", "subject": "AAAA-ada", "role": "read"}`)

	status, body := f.signIn(oidctest.User{Subject: "BBBB-eve", Tenant: fabrikam, Groups: []string{"platform"}})
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeAccessNotGranted {
		t.Errorf("an account of some tenant that names the group: status = %d, body = %s", status, body)
	}
	status, body = f.signIn(oidctest.User{Subject: "AAAA-ada", Tenant: contoso, Groups: []string{"platform"}})
	if signed := decode[api.SignedIn](t, body); status != http.StatusOK || signed.Identity.Role != api.RoleRead {
		t.Errorf("the person a name rule is for: status = %d, body = %s; want read, not the group's admin", status, body)
	}

	// With the tenants listed, their groups count as they always did.
	g := newSignInFixture(t)
	g.useNames("sub", contoso)
	g.grant(`{"kind": "group", "subject": "platform", "role": "admin"}`)
	status, body = g.signIn(oidctest.User{Subject: "AAAA-ada", Tenant: contoso, Groups: []string{"platform"}})
	if signed := decode[api.SignedIn](t, body); status != http.StatusOK || signed.Identity.Role != api.RoleAdmin {
		t.Errorf("a group of a listed tenant: status = %d, body = %s", status, body)
	}
}
