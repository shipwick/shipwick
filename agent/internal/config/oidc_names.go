package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/oidc"
)

// maxTenants bounds SHIPWICK_OIDC_TENANTS: a list longer than this is a
// directory of companies, and "any" with rules is what it wants to be.
const maxTenants = 100

// loadNames reads what people are named by and, for a provider that serves
// several tenants, which of them may sign in.
func (s *SignIn) loadNames(getenv func(string) string) error {
	if !oidcClaim.MatchString(s.NameClaim) {
		return fmt.Errorf("%s: invalid value %q (expected the name of the ID token claim people are known by: email, preferred_username, upn or sub)", EnvOIDCNameClaim, s.NameClaim)
	}

	for _, id := range strings.Fields(strings.ReplaceAll(getenv(EnvOIDCTenants), ",", " ")) {
		id = strings.ToLower(id)
		if id != oidc.AnyTenant && !oidc.ValidTenant(id) {
			return fmt.Errorf("%s: invalid tenant %q (expected tenant ids separated by commas, e.g. 8f0d4c2e-6a1b-4c3d-9e5f-0a1b2c3d4e5f, or * for any tenant)", EnvOIDCTenants, id)
		}
		if !slices.Contains(s.Tenants, id) {
			s.Tenants = append(s.Tenants, id)
		}
	}
	every := slices.Contains(s.Tenants, oidc.AnyTenant)
	switch {
	case len(s.Tenants) > maxTenants:
		return fmt.Errorf("%s lists more than %d tenants", EnvOIDCTenants, maxTenants)
	case every && len(s.Tenants) > 1:
		return fmt.Errorf("%s: * accepts every tenant and stands alone; list tenant ids, or *, not both", EnvOIDCTenants)
	case oidc.TenantTemplate(s.Issuer) != "" && len(s.Tenants) == 0:
		return fmt.Errorf("%s is Microsoft Entra's address for the accounts of every tenant: say which tenants may sign in with %s (their tenant ids, separated by commas), or use the issuer of your own tenant, https://login.microsoftonline.com/<tenant id>/v2.0",
			EnvOIDCIssuer, EnvOIDCTenants)
	// An address, a user name and a UPN are what a tenant's administrators
	// say they are. With every tenant accepted, anyone who has a tenant
	// could name an account after the person a rule is written for; the
	// subject identifier is the one name no tenant chooses.
	case every && s.NameClaim != "sub":
		return fmt.Errorf("%s=* accepts the accounts of every tenant, whose administrators choose the addresses and user names in them: set %s=sub as well, so that people are named by what no tenant can choose, or list the tenants you trust",
			EnvOIDCTenants, EnvOIDCNameClaim)
	}
	return nil
}
