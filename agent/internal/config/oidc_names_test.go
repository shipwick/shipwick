package config

import (
	"reflect"
	"strings"
	"testing"
)

const (
	entraOrganizations = "https://login.microsoftonline.com/organizations/v2.0"
	tenantA            = "8f0d4c2e-6a1b-4c3d-9e5f-0a1b2c3d4e5f"
	tenantB            = "0a1b2c3d-4e5f-4a6b-8c7d-9e8f7a6b5c4d"
)

func TestPeopleAreNamedByTheClaimOfTheOperatorsChoice(t *testing.T) {
	for value, want := range map[string]string{"": "email", "email": "email", " preferred_username ": "preferred_username", "upn": "upn", "sub": "sub"} {
		cfg, err := Load(signInEnv(map[string]string{EnvOIDCNameClaim: value}))
		if err != nil || cfg.SignIn.NameClaim != want || len(cfg.SignIn.Tenants) != 0 {
			t.Errorf("%s=%q: SignIn = %+v, %v; want the claim %q", EnvOIDCNameClaim, value, cfg.SignIn, err, want)
		}
	}
	if _, err := Load(signInEnv(map[string]string{EnvOIDCNameClaim: "user name"})); err == nil || !strings.Contains(err.Error(), EnvOIDCNameClaim) {
		t.Errorf("a claim that is not a name: %v", err)
	}
}

func TestAMultiTenantIssuerNeedsItsTenants(t *testing.T) {
	cfg, err := Load(signInEnv(map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: strings.ToUpper(tenantA) + ", " + tenantB + "," + tenantA}))
	if err != nil || !reflect.DeepEqual(cfg.SignIn.Tenants, []string{tenantA, tenantB}) {
		t.Fatalf("SignIn = %+v, %v; want both tenants, in lowercase, each once", cfg.SignIn, err)
	}
	cfg, err = Load(signInEnv(map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: "*", EnvOIDCNameClaim: "sub"}))
	if err != nil || !reflect.DeepEqual(cfg.SignIn.Tenants, []string{"*"}) {
		t.Fatalf("every tenant, named by sub: SignIn = %+v, %v", cfg.SignIn, err)
	}
	// Tenants narrow an issuer of one tenant as well.
	if cfg, err := Load(signInEnv(map[string]string{EnvOIDCTenants: tenantA})); err != nil || !reflect.DeepEqual(cfg.SignIn.Tenants, []string{tenantA}) {
		t.Errorf("tenants with an ordinary issuer: SignIn = %+v, %v", cfg.SignIn, err)
	}

	for _, tt := range []struct {
		why  string
		env  map[string]string
		want string
	}{
		{"the address for every tenant, and no tenants", map[string]string{EnvOIDCIssuer: entraOrganizations}, "say which tenants may sign in with " + EnvOIDCTenants},
		{"common, and no tenants", map[string]string{EnvOIDCIssuer: "https://login.microsoftonline.com/common/v2.0"}, EnvOIDCTenants},
		{"every tenant, named by address", map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: "*"}, "set " + EnvOIDCNameClaim + "=sub as well"},
		{"every tenant, named by user name", map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: "*", EnvOIDCNameClaim: "preferred_username"}, EnvOIDCNameClaim + "=sub"},
		{"every tenant and one more", map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: "*," + tenantA, EnvOIDCNameClaim: "sub"}, "stands alone"},
		{"a tenant's name instead of its id", map[string]string{EnvOIDCIssuer: entraOrganizations, EnvOIDCTenants: "contoso.onmicrosoft.com"}, `invalid tenant "contoso.onmicrosoft.com"`},
		{"tenants without an issuer", map[string]string{EnvOIDCIssuer: "", EnvOIDCClientID: "", EnvOIDCClientSecret: "", EnvOIDCTenants: tenantA}, EnvOIDCTenants + " is set but " + EnvOIDCIssuer + " is not"},
		{"a name claim without an issuer", map[string]string{EnvOIDCIssuer: "", EnvOIDCClientID: "", EnvOIDCClientSecret: "", EnvOIDCNameClaim: "sub"}, EnvOIDCNameClaim + " is set but " + EnvOIDCIssuer + " is not"},
	} {
		if _, err := Load(signInEnv(tt.env)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tt.why, err, tt.want)
		}
	}
}
