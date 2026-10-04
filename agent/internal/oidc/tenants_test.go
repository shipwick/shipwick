package oidc_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/oidc/oidctest"
)

const (
	contoso  = "11111111-1111-4111-8111-111111111111"
	fabrikam = "22222222-2222-4222-8222-222222222222"
)

// newTenantFixture is a provider that serves many tenants, the way Entra's
// "organizations" address does, and an agent that accepts tenants.
func newTenantFixture(t *testing.T, nameClaim string, tenants ...string) *fixture {
	f := newFixture(t)
	f.idp.ServeTenants()
	now := func() time.Time { return f.clock }
	f.provider = oidc.New(oidc.Config{Issuer: f.idp.MultiTenantIssuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		Scopes: []string{"openid", "profile"}, GroupsClaim: "groups", RedirectURL: oidctest.RedirectURL, NameClaim: nameClaim, Tenants: tenants,
		Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: now})
	return f
}

func (f *fixture) signInAs(user oidctest.User) (oidc.Claims, error) {
	f.t.Helper()
	code := f.idp.Authorize(user, nonce, oidctest.Challenge(verifier))
	raw, err := f.provider.Exchange(context.Background(), code, verifier)
	if err != nil {
		return oidc.Claims{}, err
	}
	return f.provider.Verify(context.Background(), raw, nonce)
}

func worker(tenant string) oidctest.User {
	return oidctest.User{Subject: "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", Tenant: tenant,
		Claims: map[string]any{"preferred_username": "Ada.Lovelace@contoso.example"}}
}

func TestAnAccountOfAListedTenantSignsInThroughTheAddressForEveryTenant(t *testing.T) {
	f := newTenantFixture(t, "preferred_username", contoso)
	claims, err := f.signInAs(worker(contoso))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if claims.Tenant != contoso || claims.Name != "Ada.Lovelace@contoso.example" || claims.Email != "" {
		t.Errorf("claims = %+v", claims)
	}
	if endpoint, err := f.provider.AuthorizationEndpoint(context.Background()); err != nil || endpoint != f.idp.URL+"/authorize" {
		t.Errorf("authorization endpoint = %q, %v", endpoint, err)
	}
}

func TestAnAccountOfATenantThatIsNotListedIsRefusedAsSuch(t *testing.T) {
	f := newTenantFixture(t, "sub", contoso)
	_, err := f.signInAs(worker(fabrikam))
	if kindOf(err) != oidc.TenantNotAllowed || !strings.Contains(err.Error(), "SHIPWICK_OIDC_TENANTS") {
		t.Fatalf("err = %v (kind %d), want TenantNotAllowed", err, kindOf(err))
	}
	if !strings.Contains(f.logs.String(), fabrikam) {
		t.Errorf("the log does not name the tenant that was refused: %s", f.logs)
	}
}

func TestEveryTenantIsAcceptedOnlyWhereTheOperatorSaysSo(t *testing.T) {
	f := newTenantFixture(t, "sub", oidc.AnyTenant)
	for _, tenant := range []string{contoso, fabrikam} {
		if claims, err := f.signInAs(worker(tenant)); err != nil || claims.Tenant != tenant || claims.Name != "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ" {
			t.Errorf("tenant %s: claims = %+v, %v", tenant, claims, err)
		}
	}
}

func TestATokenWhoseIssuerIsNotItsOwnTenantsIsRefusedWhateverTheListSays(t *testing.T) {
	for name, tamper := range map[string]func(f *fixture, claims map[string]any){
		// Signed by the provider's keys for one tenant, and claiming another
		// that is allowed.
		"tid of an allowed tenant under another tenant's issuer": func(_ *fixture, claims map[string]any) { claims["tid"] = contoso },
		"an allowed tenant's issuer with another tid":            func(f *fixture, claims map[string]any) { claims["iss"] = f.idp.TenantIssuer(contoso) },
		"an issuer elsewhere": func(_ *fixture, claims map[string]any) {
			claims["iss"] = "https://login.example.net/" + fabrikam + "/v2.0"
		},
		"the template itself":    func(f *fixture, claims map[string]any) { claims["iss"] = f.idp.TenantIssuer("{tenantid}") },
		"the configured address": func(f *fixture, claims map[string]any) { claims["iss"] = f.idp.MultiTenantIssuer() },
		"no tid":                 func(_ *fixture, claims map[string]any) { delete(claims, "tid") },
		"a tid that is a path": func(f *fixture, claims map[string]any) {
			claims["tid"], claims["iss"] = "x/y", f.idp.TenantIssuer("x/y")
		},
		"a tid in capitals": func(f *fixture, claims map[string]any) {
			claims["tid"], claims["iss"] = "ABCDEF01-2222-4222-8222-222222222222", f.idp.TenantIssuer("ABCDEF01-2222-4222-8222-222222222222")
		},
	} {
		for _, tenants := range [][]string{{contoso}, {oidc.AnyTenant}} {
			f := newTenantFixture(t, "sub", tenants...)
			f.idp.Mutate(func(claims map[string]any) { tamper(f, claims) })
			_, err := f.signInAs(worker(fabrikam))
			if kindOf(err) != oidc.InvalidToken || !strings.Contains(err.Error(), "it was issued by") {
				t.Errorf("%s, tenants %v: err = %v (kind %d), want InvalidToken", name, tenants, err, kindOf(err))
			}
		}
	}
}

func TestTheAddressForEveryTenantIsNotUsedWithoutTenants(t *testing.T) {
	f := newTenantFixture(t, "sub")
	_, err := f.signInAs(worker(contoso))
	if kindOf(err) != oidc.Unavailable || !strings.Contains(err.Error(), "SHIPWICK_OIDC_TENANTS") {
		t.Fatalf("err = %v (kind %d), want Unavailable naming SHIPWICK_OIDC_TENANTS", err, kindOf(err))
	}
}

// A discovery document may name a templated issuer only at the addresses
// that are known to be for many tenants. Anywhere else it is what it always
// was: a document about another provider.
func TestATemplatedIssuerIsRefusedWhereTheConfiguredIssuerIsNotForManyTenants(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL + "/{tenantid}/v2.0",
			"authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/keys",
		})
	}))
	defer srv.Close()
	for _, issuer := range []string{srv.URL, srv.URL + "/tenants/v2.0", srv.URL + "/organizations", srv.URL + "/organizations/v1.0"} {
		p := oidc.New(oidc.Config{Issuer: issuer, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, Tenants: []string{oidc.AnyTenant},
			RedirectURL: oidctest.RedirectURL, Logger: slog.New(slog.DiscardHandler)})
		_, err := p.AuthorizationEndpoint(context.Background())
		if kindOf(err) != oidc.Unavailable || !strings.Contains(err.Error(), "calls itself") {
			t.Errorf("issuer %s: err = %v, want the document refused", issuer, err)
		}
	}
	// And at such an address, only its own template.
	for _, issuer := range []string{"https://login.example.net/organizations/v2.0", "https://login.example.net/common/v2.0"} {
		if got := oidc.TenantTemplate(issuer); got != "https://login.example.net/{tenantid}/v2.0" {
			t.Errorf("TenantTemplate(%s) = %q", issuer, got)
		}
	}
	for _, issuer := range []string{"https://login.example.net/consumers/v2.0", "https://login.example.net/organizations/v2.0/", "https://login.example.net/" + contoso + "/v2.0", "https://accounts.example.com"} {
		if got := oidc.TenantTemplate(issuer); got != "" {
			t.Errorf("TenantTemplate(%s) = %q, want none", issuer, got)
		}
	}
}

func TestAKeyThatSignsForOneIssuerSignsForNoOther(t *testing.T) {
	f := newTenantFixture(t, "sub", oidc.AnyTenant)
	// Entra's own keys name the template: they sign for every tenant.
	f.idp.KeysSignFor(f.idp.TenantIssuer("{tenantid}"))
	if _, err := f.signInAs(worker(contoso)); err != nil {
		t.Fatalf("keys for every tenant: %v", err)
	}

	f = newTenantFixture(t, "sub", oidc.AnyTenant)
	f.idp.KeysSignFor(f.idp.TenantIssuer(fabrikam))
	if _, err := f.signInAs(worker(fabrikam)); err != nil {
		t.Fatalf("the tenant the keys are for: %v", err)
	}
	f = newTenantFixture(t, "sub", oidc.AnyTenant)
	f.idp.KeysSignFor(f.idp.TenantIssuer(fabrikam))
	if _, err := f.signInAs(worker(contoso)); kindOf(err) != oidc.InvalidToken {
		t.Errorf("a key of one tenant signing for another: err = %v, want InvalidToken", err)
	}
}

func TestTenantsNarrowAProviderThatIsOneIssuerToo(t *testing.T) {
	f := newFixture(t)
	now := func() time.Time { return f.clock }
	f.provider = oidc.New(oidc.Config{Issuer: f.idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, Tenants: []string{contoso},
		RedirectURL: oidctest.RedirectURL, Logger: slog.New(slog.DiscardHandler), Now: now})
	if _, err := f.signInAs(oidctest.User{Email: "ada@example.com"}); kindOf(err) != oidc.TenantNotAllowed {
		t.Errorf("a token without tid: err = %v, want TenantNotAllowed", err)
	}
	if claims, err := f.signInAs(oidctest.User{Email: "ada@example.com", Claims: map[string]any{"tid": contoso}}); err != nil || claims.Tenant != contoso {
		t.Errorf("a token of the listed tenant: claims = %+v, %v", claims, err)
	}
}

func TestThePersonIsNamedByTheClaimOfTheOperatorsChoice(t *testing.T) {
	user := oidctest.User{Subject: "248289761001", Email: "Ada@Example.com", Claims: map[string]any{
		"preferred_username": "ada", "upn": "ada@corp.example", "employee": 7, "nothing": nil}}
	for claim, want := range map[string]string{
		"":                   "Ada@Example.com",
		"email":              "Ada@Example.com",
		"sub":                "248289761001",
		"preferred_username": "ada",
		"upn":                "ada@corp.example",
		// Not a string, absent, or null: no name, and the caller refuses.
		"employee": "",
		"nickname": "",
		"nothing":  "",
	} {
		f := newFixture(t)
		now := func() time.Time { return f.clock }
		f.provider = oidc.New(oidc.Config{Issuer: f.idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, NameClaim: claim,
			RedirectURL: oidctest.RedirectURL, Logger: slog.New(slog.DiscardHandler), Now: now})
		claims, err := f.signInAs(user)
		if err != nil || claims.Name != want {
			t.Errorf("name claim %q: name = %q, %v; want %q", claim, claims.Name, err, want)
		}
	}
}
