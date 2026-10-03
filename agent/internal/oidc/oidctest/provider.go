// Package oidctest provides an in-memory OpenID Connect provider for tests.
// It serves a discovery document and its keys, hands out authorization codes
// the way a browser sign-in would, and redeems each once — checking the
// client's credentials, the redirect URI and the PKCE verifier as a real
// provider does, so that a client which sends them wrongly fails here too.
package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The client the provider knows.
const (
	ClientID     = "shipwick"
	ClientSecret = "client-secret-only-the-agent-holds"
	RedirectURL  = "https://dashboard.example.com/auth/callback"
)

// User is a person the provider can sign in.
type User struct {
	Subject string
	Email   string
	// EmailVerified is left out of the ID token when nil.
	EmailVerified *bool
	Groups        []string
}

// Provider is the fake. URL is its issuer.
type Provider struct {
	URL string

	mu       sync.Mutex
	now      func() time.Time
	alg      string
	keyID    string
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	codes    map[string]grant
	next     int
	mutate   func(claims map[string]any)
	down     bool
	postOnly bool
	requests []string
}

type grant struct {
	user             User
	nonce, challenge string
}

// One RSA key for the whole test binary: generating one takes longer than
// most tests do.
var (
	rsaOnce               sync.Once
	sharedRSA, strangeRSA *rsa.PrivateKey
)

func rsaKeys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	rsaOnce.Do(func() {
		var err error
		if sharedRSA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatalf("generate key: %v", err)
		}
		if strangeRSA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatalf("generate key: %v", err)
		}
	})
	return sharedRSA, strangeRSA
}

// New starts the provider; it stops with the test. now is the provider's
// clock.
func New(t testing.TB, now func() time.Time) *Provider {
	t.Helper()
	p := &Provider{now: now, alg: "RS256", keyID: "key-1", codes: map[string]grant{}}
	p.rsaKey, _ = rsaKeys(t)
	var err error
	if p.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// UseES256 makes the provider sign with its P-256 key from now on.
func (p *Provider) UseES256() { p.set(func() { p.alg = "ES256" }) }

// Mutate changes the claims of every ID token before it is signed; nil
// stops doing so.
func (p *Provider) Mutate(fn func(claims map[string]any)) { p.set(func() { p.mutate = fn }) }

// SignWithAnotherKey makes the provider sign with a key it does not publish,
// under the id of the one it does.
func (p *Provider) SignWithAnotherKey(t testing.TB) {
	_, strange := rsaKeys(t)
	p.set(func() { p.rsaKey = strange })
}

// RotateKey gives the published key a new id, as a provider that replaced
// its key would.
func (p *Provider) RotateKey(id string) { p.set(func() { p.keyID = id }) }

// Down makes every endpoint answer 503 until it is called with false.
func (p *Provider) Down(down bool) { p.set(func() { p.down = down }) }

// PostOnly makes the provider announce and accept the client secret in the
// form only.
func (p *Provider) PostOnly() { p.set(func() { p.postOnly = true }) }

func (p *Provider) set(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

// Requests lists the requests so far as "METHOD path".
func (p *Provider) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// Authorize is the browser's part: user signs in for an authorization
// request with this nonce and code challenge, and the code the provider
// would append to the redirect comes back.
func (p *Provider) Authorize(user User, nonce, challenge string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	code := "code-" + strconv.Itoa(p.next)
	p.codes[code] = grant{user: user, nonce: nonce, challenge: challenge}
	return code
}

// Challenge is the S256 code challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Method+" "+r.URL.Path)
	if p.down {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		methods := []string{"client_secret_basic", "client_secret_post"}
		if p.postOnly {
			methods = []string{"client_secret_post"}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"token_endpoint_auth_methods_supported": methods,
		})
	case "/keys":
		// The uncompressed point: 4, then x and y, 32 bytes each.
		point, _ := p.ecKey.PublicKey.Bytes()
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "use": "sig", "kid": p.keyID, "alg": "RS256",
				"n": b64(sharedRSA.N.Bytes()), "e": "AQAB"},
			{"kty": "EC", "use": "sig", "kid": p.keyID + "-ec", "crv": "P-256",
				"x": b64(point[1:33]), "y": b64(point[33:])},
		}})
	case "/token":
		p.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != ClientID || secret != ClientSecret || (p.postOnly && basic) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "Invalid client or Invalid client credentials"})
		return
	}
	code := r.PostForm.Get("code")
	g, ok := p.codes[code]
	// A code is redeemed once, whatever becomes of the attempt.
	delete(p.codes, code)
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != RedirectURL ||
		Challenge(r.PostForm.Get("code_verifier")) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Code not valid"})
		return
	}

	now := p.now()
	claims := map[string]any{
		"iss": p.URL, "aud": ClientID, "sub": g.user.Subject, "nonce": g.nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"email": g.user.Email,
	}
	if claims["sub"] == "" {
		claims["sub"] = "sub-" + g.user.Email
	}
	if g.user.EmailVerified != nil {
		claims["email_verified"] = *g.user.EmailVerified
	}
	if g.user.Groups != nil {
		claims["groups"] = g.user.Groups
	}
	if p.mutate != nil {
		p.mutate(claims)
	}
	writeJSON(w, http.StatusOK, map[string]string{"token_type": "Bearer", "access_token": "not-used", "id_token": p.sign(claims)})
}

// sign makes a JWT of claims with the provider's current key and algorithm.
func (p *Provider) sign(claims map[string]any) string {
	alg, kid := p.alg, p.keyID
	if a, ok := claims["__alg"].(string); ok {
		alg = a
		delete(claims, "__alg")
	}
	if alg == "ES256" {
		kid += "-ec"
	}
	header, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signed := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signed))
	var signature []byte
	switch alg {
	case "RS256":
		signature, _ = rsa.SignPKCS1v15(rand.Reader, p.rsaKey, crypto.SHA256, digest[:])
	case "ES256":
		r, s, _ := ecdsa.Sign(rand.Reader, p.ecKey, digest[:])
		signature = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	}
	// Any other algorithm, "none" among them, is left unsigned.
	return signed + "." + b64(signature)
}

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(fmt.Sprintf("oidctest: %v", err))
	}
}
