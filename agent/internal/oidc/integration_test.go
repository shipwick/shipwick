//go:build integration

package oidc_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/oidc/oidctest"
)

// The tests here sign in at a real Keycloak in development mode:
//
//	docker run --rm -p 127.0.0.1:8081:8080 \
//	  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
//	  -e KC_HOSTNAME=http://127.0.0.1:8081 quay.io/keycloak/keycloak:26.0 start-dev
//	SHIPWICK_TEST_KEYCLOAK=http://127.0.0.1:8081 go test -tags integration ./agent/internal/oidc/
//
// They are skipped without SHIPWICK_TEST_KEYCLOAK. The admin is admin/admin
// unless SHIPWICK_TEST_KEYCLOAK_ADMIN and _PASSWORD say otherwise.
const (
	keycloakRealm    = "shipwick-integration"
	keycloakRedirect = "https://dashboard.example.com/auth/callback"
	keycloakSecret   = "integration-client-secret"
)

type keycloak struct {
	t    *testing.T
	base string
}

func (k keycloak) request(method, target, bearer string, body any, form url.Values) (int, []byte, http.Header) {
	k.t.Helper()
	var r io.Reader
	contentType := ""
	switch {
	case form != nil:
		r, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	case body != nil:
		raw, _ := json.Marshal(body)
		r, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequest(method, target, r)
	if err != nil {
		k.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatalf("keycloak: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header
}

// newKeycloak makes a realm with two confidential clients, a group and a
// user in it, and removes the realm with the test.
func newKeycloak(t *testing.T) keycloak {
	base := strings.TrimSuffix(os.Getenv("SHIPWICK_TEST_KEYCLOAK"), "/")
	if base == "" {
		t.Skip("SHIPWICK_TEST_KEYCLOAK is not set")
	}
	k := keycloak{t: t, base: base}
	user, password := os.Getenv("SHIPWICK_TEST_KEYCLOAK_ADMIN"), os.Getenv("SHIPWICK_TEST_KEYCLOAK_PASSWORD")
	if user == "" {
		user, password = "admin", "admin"
	}
	_, body, _ := k.request("POST", base+"/realms/master/protocol/openid-connect/token", "", nil,
		url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {user}, "password": {password}})
	var admin struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &admin); admin.AccessToken == "" {
		t.Fatalf("no admin token from Keycloak: %s", body)
	}
	realms := base + "/admin/realms"
	must := func(status int, body []byte, header http.Header) http.Header {
		t.Helper()
		if status >= 300 {
			t.Fatalf("keycloak setup: %d %s", status, body)
		}
		return header
	}
	k.request("DELETE", realms+"/"+keycloakRealm, admin.AccessToken, nil, nil)
	must(k.request("POST", realms, admin.AccessToken, map[string]any{"realm": keycloakRealm, "enabled": true}, nil))
	t.Cleanup(func() { k.request("DELETE", realms+"/"+keycloakRealm, admin.AccessToken, nil, nil) })

	mapper := []map[string]any{{"name": "groups", "protocol": "openid-connect", "protocolMapper": "oidc-group-membership-mapper",
		"config": map[string]string{"claim.name": "groups", "full.path": "false", "id.token.claim": "true"}}}
	for _, id := range []string{"shipwick", "another-app"} {
		must(k.request("POST", realms+"/"+keycloakRealm+"/clients", admin.AccessToken, map[string]any{"clientId": id, "secret": keycloakSecret,
			"publicClient": false, "standardFlowEnabled": true, "redirectUris": []string{keycloakRedirect}, "protocolMappers": mapper}, nil))
	}
	last := func(h http.Header) string { return h.Get("Location")[strings.LastIndex(h.Get("Location"), "/")+1:] }
	group := last(must(k.request("POST", realms+"/"+keycloakRealm+"/groups", admin.AccessToken, map[string]any{"name": "developers"}, nil)))
	ada := last(must(k.request("POST", realms+"/"+keycloakRealm+"/users", admin.AccessToken, map[string]any{"username": "ada", "email": "ada@example.com",
		"emailVerified": true, "enabled": true, "firstName": "Ada", "lastName": "Test",
		"credentials": []map[string]any{{"type": "password", "value": "ada-password", "temporary": false}}}, nil)))
	must(k.request("PUT", realms+"/"+keycloakRealm+"/users/"+ada+"/groups/"+group, admin.AccessToken, nil, nil))
	return k
}

func (k keycloak) issuer() string { return k.base + "/realms/" + keycloakRealm }

func (k keycloak) provider(clientID string, now func() time.Time) *oidc.Provider {
	return oidc.New(oidc.Config{Issuer: k.issuer(), ClientID: clientID, ClientSecret: keycloakSecret, Scopes: []string{"openid", "email", "profile"},
		GroupsClaim: "groups", RedirectURL: keycloakRedirect, Now: now})
}

func randomValue(t *testing.T) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

var keycloakLoginForm = regexp.MustCompile(`<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// signIn is the browser: the authorization request, Keycloak's login form,
// and the code from the redirect, which is read and not followed.
func (k keycloak) signIn(p *oidc.Provider, nonce, verifier string) string {
	k.t.Helper()
	endpoint, err := p.AuthorizationEndpoint(context.Background())
	if err != nil {
		k.t.Fatalf("discovery: %v", err)
	}
	u, _ := url.Parse(endpoint)
	u.RawQuery = url.Values{"response_type": {"code"}, "client_id": {p.ClientID()}, "redirect_uri": {p.RedirectURL()}, "scope": {strings.Join(p.Scopes(), " ")},
		"state": {randomValue(k.t)}, "nonce": {nonce}, "code_challenge": {oidctest.Challenge(verifier)}, "code_challenge_method": {"S256"}}.Encode()

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(u.String())
	if err != nil {
		k.t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := keycloakLoginForm.FindSubmatch(page)
	if m == nil {
		k.t.Fatalf("no login form: %d %.300s", resp.StatusCode, page)
	}
	resp, err = browser.PostForm(html.UnescapeString(string(m[1])), url.Values{"username": {"ada"}, "password": {"ada-password"}, "credentialId": {""}})
	if err != nil {
		k.t.Fatal(err)
	}
	resp.Body.Close()
	callback, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || callback.Query().Get("code") == "" || !strings.HasPrefix(callback.String(), keycloakRedirect+"?") {
		k.t.Fatalf("no callback: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return callback.Query().Get("code")
}

func TestSigningInAtKeycloak(t *testing.T) {
	k := newKeycloak(t)
	ctx := context.Background()
	clock := time.Now
	p := k.provider("shipwick", func() time.Time { return clock() })
	nonce, verifier := randomValue(t), randomValue(t)

	code := k.signIn(p, nonce, verifier)
	raw, err := p.Exchange(ctx, code, verifier)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	claims, err := p.Verify(ctx, raw, nonce)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Email != "ada@example.com" || claims.EmailVerified == nil || !*claims.EmailVerified || fmt.Sprint(claims.Groups) != "[developers]" {
		t.Errorf("claims = %+v", claims)
	}

	// The code is spent, and another would be worthless without its verifier.
	if _, err := p.Exchange(ctx, code, verifier); kindOf(err) != oidc.CodeRejected {
		t.Errorf("the code a second time: %v", err)
	}
	if _, err := p.Exchange(ctx, k.signIn(p, nonce, verifier), randomValue(t)); kindOf(err) != oidc.CodeRejected {
		t.Errorf("a code with another verifier: %v", err)
	}

	// The same token, which Keycloak signed and the agent just believed, is
	// refused for another nonce, once its time is up, and by another client.
	if _, err := p.Verify(ctx, raw, randomValue(t)); kindOf(err) != oidc.NonceMismatch {
		t.Errorf("another nonce: %v", err)
	}
	clock = func() time.Time { return time.Now().Add(30 * time.Minute) }
	if _, err := p.Verify(ctx, raw, nonce); kindOf(err) != oidc.InvalidToken || !strings.Contains(err.Error(), "expired") {
		t.Errorf("half an hour later: %v", err)
	}
	clock = time.Now
	if _, err := k.provider("another-app", time.Now).Verify(ctx, raw, nonce); kindOf(err) != oidc.InvalidToken || !strings.Contains(err.Error(), "another client") {
		t.Errorf("verified as another client: %v", err)
	}
	// And a token Keycloak issued to another client is not one for this one.
	other := k.provider("another-app", time.Now)
	foreign, err := other.Exchange(ctx, k.signIn(other, nonce, verifier), verifier)
	if err != nil {
		t.Fatalf("Exchange as another client: %v", err)
	}
	if _, err := p.Verify(ctx, foreign, nonce); kindOf(err) != oidc.InvalidToken || !strings.Contains(err.Error(), "another client") {
		t.Errorf("another client's ID token: %v", err)
	}
	// A code issued to another client is refused at the token endpoint.
	if _, err := p.Exchange(ctx, k.signIn(other, nonce, verifier), verifier); kindOf(err) != oidc.CodeRejected {
		t.Errorf("another client's code: %v", err)
	}

	wrong := oidc.New(oidc.Config{Issuer: k.issuer(), ClientID: "shipwick", ClientSecret: "not-the-secret", RedirectURL: keycloakRedirect})
	if _, err := wrong.Exchange(ctx, k.signIn(p, nonce, verifier), verifier); kindOf(err) != oidc.Unavailable || !strings.Contains(err.Error(), "SHIPWICK_OIDC_CLIENT_SECRET") {
		t.Errorf("a wrong client secret: %v", err)
	}
}
