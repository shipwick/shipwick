package config

import (
	"reflect"
	"strings"
	"testing"
)

func signInEnv(overrides map[string]string) func(string) string {
	env := map[string]string{
		EnvCaddyAdmin:       "unix//run/caddy/admin.sock",
		EnvDashboardDomain:  "dashboard.example.com",
		EnvOIDCIssuer:       "https://accounts.example.com/realms/company",
		EnvOIDCClientID:     "shipwick",
		EnvOIDCClientSecret: "s3cr3t-of-the-client",
	}
	for k, v := range overrides {
		env[k] = v
	}
	return func(name string) string { return env[name] }
}

func TestSignInIsConfiguredWithAProviderAndTheDashboardsHostname(t *testing.T) {
	cfg, err := Load(signInEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &SignIn{Issuer: "https://accounts.example.com/realms/company", ClientID: "shipwick", ClientSecret: "s3cr3t-of-the-client",
		Scopes: []string{"openid", "email", "profile"}, GroupsClaim: "groups", RedirectURL: "https://dashboard.example.com/auth/callback", NameClaim: "email"}
	if !reflect.DeepEqual(cfg.SignIn, want) {
		t.Errorf("SignIn = %+v, want %+v", cfg.SignIn, want)
	}

	cfg, err = Load(signInEnv(map[string]string{EnvOIDCScopes: "openid,email groups", EnvOIDCGroupsClaim: "https://example.com/groups",
		EnvDashboardDomain: "", EnvCaddyAdmin: "", EnvOIDCRedirectURL: "http://localhost:3000/auth/callback", EnvOIDCIssuer: "http://localhost:8080/realms/dev"}))
	if err != nil {
		t.Fatalf("Load for development: %v", err)
	}
	if !reflect.DeepEqual(cfg.SignIn.Scopes, []string{"openid", "email", "groups"}) || cfg.SignIn.GroupsClaim != "https://example.com/groups" ||
		cfg.SignIn.RedirectURL != "http://localhost:3000/auth/callback" {
		t.Errorf("SignIn = %+v", cfg.SignIn)
	}

	if cfg, err := Load(func(string) string { return "" }); err != nil || cfg.SignIn != nil {
		t.Errorf("without the variables: SignIn = %+v, %v; want none", cfg.SignIn, err)
	}
}

func TestSignInConfigurationIsRefusedWithTheVariableToChange(t *testing.T) {
	for _, tt := range []struct {
		why  string
		env  map[string]string
		want string
	}{
		{"no issuer", map[string]string{EnvOIDCIssuer: ""}, EnvOIDCClientID + " is set but " + EnvOIDCIssuer + " is not"},
		{"plain http to the network", map[string]string{EnvOIDCIssuer: "http://accounts.example.com"}, EnvOIDCIssuer + ": must use https"},
		{"no client id", map[string]string{EnvOIDCClientID: ""}, EnvOIDCClientID},
		{"no client secret", map[string]string{EnvOIDCClientSecret: ""}, EnvOIDCClientSecret},
		{"a secret with a line break", map[string]string{EnvOIDCClientSecret: "s3cr3t\nmore"}, EnvOIDCClientSecret},
		{"scopes without openid", map[string]string{EnvOIDCScopes: "email profile"}, "must contain openid"},
		{"a scope that is not one", map[string]string{EnvOIDCScopes: `openid "email"`}, EnvOIDCScopes},
		{"a claim that is not a name", map[string]string{EnvOIDCGroupsClaim: "my groups"}, EnvOIDCGroupsClaim},
		{"no dashboard", map[string]string{EnvDashboardDomain: ""}, "set " + EnvDashboardDomain},
		{"a redirect to somewhere else", map[string]string{EnvOIDCRedirectURL: "https://evil.example.com/auth/callback"}, EnvOIDCRedirectURL},
		{"a redirect to another path", map[string]string{EnvOIDCRedirectURL: "http://localhost:3000/elsewhere"}, EnvOIDCRedirectURL},
	} {
		_, err := Load(signInEnv(tt.env))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tt.why, err, tt.want)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: the error repeats the client secret: %v", tt.why, err)
		}
	}
}
