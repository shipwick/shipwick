package oidc

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Microsoft Entra serves the accounts of every tenant under one address,
// https://login.microsoftonline.com/organizations/v2.0 (work and school
// accounts) or .../common/v2.0 (those and personal ones). The discovery
// document found there does not name that address as its issuer, and cannot:
// each token is issued by the tenant of the account, and the document says
// so with a placeholder, https://login.microsoftonline.com/{tenantid}/v2.0.
//
// A document that names another issuer than the configured one is otherwise
// the mark of another provider, so this one is accepted only as far as it
// can be checked:
//
//   - The configured issuer has to be one of those two paths, and the
//     document's issuer that same address with the placeholder in the path's
//     place. Nothing else that is templated is taken, wherever it comes from.
//   - A token's iss has to be the template with the token's own tid filled
//     in, and tid has to look like a tenant's id. The keys are the ones the
//     configured address publishes, so the signature still says the token is
//     Microsoft's; iss and tid agreeing says which tenant's.
//   - Where a key says which issuer it signs for, the token has to be from
//     that one: among the keys of "common" are those of the tenant personal
//     accounts live in, which sign for nobody else.
//   - The operator has to say which tenants may sign in. Every company with
//     a Microsoft account is a tenant, and its administrators decide what
//     the accounts in it are called.

// tenantPlaceholder is what stands for the tenant's id in a templated issuer.
const tenantPlaceholder = "{tenantid}"

// AnyTenant in Config.Tenants accepts the accounts of every tenant.
const AnyTenant = "*"

// A tenant's id is a GUID; Entra writes it in lowercase.
var tenantID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidTenant reports whether id can be a tenant's.
func ValidTenant(id string) bool { return tenantID.MatchString(id) }

// TenantTemplate returns the issuer a multi-tenant provider's discovery
// document is expected to state for the configured issuer, or "" for an
// issuer that is not one of the multi-tenant addresses.
func TenantTemplate(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.Path != "/organizations/v2.0" && u.Path != "/common/v2.0" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/" + tenantPlaceholder + "/v2.0"
}

// NameClaim is the ID token claim people are named by.
func (p *Provider) NameClaim() string {
	if p.cfg.NameClaim == "" {
		return "email"
	}
	return p.cfg.NameClaim
}

// TenantAllowed reports whether an account of this tenant may sign in; ""
// is an account whose token named none. Without Tenants there is nothing to
// be allowed by: the provider is one issuer, and all of its accounts may.
func (p *Provider) TenantAllowed(tenant string) bool {
	return len(p.cfg.Tenants) == 0 || slices.Contains(p.cfg.Tenants, AnyTenant) || tenant != "" && slices.Contains(p.cfg.Tenants, tenant)
}

// issuedBy reports whether a token with this iss and tid, signed with a key
// that names keyIssuer ("" for a key that names none), comes from where the
// discovery document says tokens come from.
func (p *Provider) issuedBy(doc *discovery, iss, tid, keyIssuer string) bool {
	want := p.cfg.Issuer
	if doc.template != "" {
		if !ValidTenant(tid) {
			return false
		}
		want = strings.Replace(doc.template, tenantPlaceholder, tid, 1)
	}
	if keyIssuer != "" && strings.Replace(keyIssuer, tenantPlaceholder, tid, 1) != iss {
		return false
	}
	return iss == want
}

// AnyTenant reports whether the accounts of every tenant are accepted. What
// such an account says about itself beyond its subject identifier — its
// groups among it — is said by a directory the operator did not choose.
func (p *Provider) AnyTenant() bool {
	return slices.Contains(p.cfg.Tenants, AnyTenant)
}
