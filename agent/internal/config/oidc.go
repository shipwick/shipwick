package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/pkg/api"
)

const (
	EnvOIDCIssuer       = "SHIPWICK_OIDC_ISSUER"
	EnvOIDCClientID     = "SHIPWICK_OIDC_CLIENT_ID"
	EnvOIDCClientSecret = "SHIPWICK_OIDC_CLIENT_SECRET"
	EnvOIDCScopes       = "SHIPWICK_OIDC_SCOPES"
	EnvOIDCGroupsClaim  = "SHIPWICK_OIDC_GROUPS_CLAIM"
	// EnvOIDCRedirectURL replaces the dashboard's hostname as the place the
	// provider sends the browser back to, for a dashboard run on the
	// developer's machine: only a localhost URL is accepted.
	EnvOIDCRedirectURL = "SHIPWICK_OIDC_REDIRECT_URL"
	// EnvOIDCNameClaim names the ID token claim people are known by, for
	// providers whose accounts have no address.
	EnvOIDCNameClaim = "SHIPWICK_OIDC_NAME_CLAIM"
	// EnvOIDCTenants lists the Microsoft Entra tenants whose accounts may
	// sign in: what a multi-tenant issuer is not used without.
	EnvOIDCTenants = "SHIPWICK_OIDC_TENANTS"
)

// SignIn is the OpenID Connect provider people sign in to the dashboard
// with, and the client the agent is registered as there.
type SignIn struct {
	Issuer   string
	ClientID string
	// ClientSecret is the raw value of SHIPWICK_OIDC_CLIENT_SECRET. It goes
	// to the provider's token endpoint and must never be logged or returned.
	ClientSecret string
	Scopes       []string
	GroupsClaim  string
	// RedirectURL is the dashboard's callback: the only place the provider
	// is asked to send anyone back to.
	RedirectURL string
	// NameClaim is the ID token claim people are named by: "email" unless
	// the operator chose another.
	NameClaim string
	// Tenants are the tenant ids allowed to sign in, in lowercase, or
	// oidc.AnyTenant alone; empty for a provider that is one issuer.
	Tenants []string
}

var (
	// What a scope and a claim name are made of; a namespaced claim such as
	// https://example.com/groups is a name too.
	oidcScope = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]{1,128}$`)
	oidcClaim = regexp.MustCompile(`^[A-Za-z0-9_:./-]{1,128}$`)
	// A client id is whatever the provider issued; it goes into a URL and a
	// header, so visible ASCII and nothing else.
	oidcClientID = regexp.MustCompile(`^[\x21-\x7E]{1,256}$`)
)

func (c *Config) loadSignIn(getenv func(string) string) error {
	issuer := strings.TrimSpace(getenv(EnvOIDCIssuer))
	if issuer == "" {
		for _, name := range []string{EnvOIDCClientID, EnvOIDCClientSecret, EnvOIDCScopes, EnvOIDCGroupsClaim, EnvOIDCRedirectURL, EnvOIDCNameClaim, EnvOIDCTenants} {
			if strings.TrimSpace(getenv(name)) != "" {
				return fmt.Errorf("%s is set but %s is not", name, EnvOIDCIssuer)
			}
		}
		return nil
	}
	if err := oidc.ValidateIssuer(issuer); err != nil {
		return fmt.Errorf("%s: %w", EnvOIDCIssuer, err)
	}
	s := &SignIn{
		Issuer:       issuer,
		ClientID:     strings.TrimSpace(getenv(EnvOIDCClientID)),
		ClientSecret: strings.TrimSpace(getenv(EnvOIDCClientSecret)),
		Scopes:       strings.Fields(strings.ReplaceAll(valueOr(getenv(EnvOIDCScopes), "openid email profile"), ",", " ")),
		GroupsClaim:  valueOr(strings.TrimSpace(getenv(EnvOIDCGroupsClaim)), "groups"),
		NameClaim:    valueOr(strings.TrimSpace(getenv(EnvOIDCNameClaim)), api.DefaultNameClaim),
	}
	if !oidcClientID.MatchString(s.ClientID) {
		return fmt.Errorf("%s: the client id the provider issued for Shipwick is needed next to %s", EnvOIDCClientID, EnvOIDCIssuer)
	}
	// The value is a secret: the error names the rule, not the value.
	if s.ClientSecret == "" || strings.ContainsFunc(s.ClientSecret, func(c rune) bool { return c < ' ' || c == 0x7f }) {
		return fmt.Errorf("%s: the client secret the provider issued for Shipwick is needed next to %s; register Shipwick there as a confidential (web) client", EnvOIDCClientSecret, EnvOIDCIssuer)
	}
	for _, scope := range s.Scopes {
		if !oidcScope.MatchString(scope) {
			return fmt.Errorf("%s: invalid scope %q (expected names separated by spaces, e.g. \"openid email profile\")", EnvOIDCScopes, scope)
		}
	}
	if !slices.Contains(s.Scopes, "openid") {
		return fmt.Errorf("%s must contain openid: without it the provider issues no ID token", EnvOIDCScopes)
	}
	if !oidcClaim.MatchString(s.GroupsClaim) {
		return fmt.Errorf("%s: invalid value %q (expected the name of the ID token claim that lists groups, e.g. groups)", EnvOIDCGroupsClaim, s.GroupsClaim)
	}
	if err := s.loadNames(getenv); err != nil {
		return err
	}

	switch redirect := strings.TrimSpace(getenv(EnvOIDCRedirectURL)); {
	case redirect != "":
		u, err := url.Parse(redirect)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Path != api.SignInCallbackPath || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !loopback(u.Hostname()) {
			return fmt.Errorf("%s: only for a dashboard on the developer's machine, e.g. http://localhost:3000%s; on a server set %s and leave this unset",
				EnvOIDCRedirectURL, api.SignInCallbackPath, EnvDashboardDomain)
		}
		s.RedirectURL = redirect
	case c.DashboardDomain != "":
		s.RedirectURL = "https://" + c.DashboardDomain + api.SignInCallbackPath
	default:
		return fmt.Errorf("%s needs the dashboard people sign in to: set %s as well", EnvOIDCIssuer, EnvDashboardDomain)
	}
	c.SignIn = s
	return nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
