package oidc_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/pkg/outbound"
)

// A provider inside the company has a certificate the company issued. The
// agent trusts it through SHIPWICK_CA_FILE, as it does for the webhook and the
// bucket; without that the discovery document cannot even be read.
func TestAProviderWithACertificateOfTheCompanysOwnIsTrustedThroughTheCAFile(t *testing.T) {
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/keys",
		})
	}))
	defer idp.Close()
	issuer = idp.URL

	config := oidc.Config{Issuer: issuer, ClientID: "shipwick", ClientSecret: "a-secret", RedirectURL: "https://dashboard.example.com/auth/callback"}
	if _, err := oidc.New(config).AuthorizationEndpoint(context.Background()); err == nil {
		t.Fatal("a certificate from an authority nobody named was trusted")
	}

	ca := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: idp.Certificate().Raw})
	if err := os.WriteFile(ca, block, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := outbound.Trust(ca); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	endpoint, err := oidc.New(config).AuthorizationEndpoint(context.Background())
	if err != nil || endpoint != issuer+"/authorize" {
		t.Fatalf("with the authority in the CA file: %q, %v", endpoint, err)
	}
}
