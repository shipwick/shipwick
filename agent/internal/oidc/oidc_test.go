package oidc_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/oidc"
	"github.com/shipwick/shipwick/agent/internal/oidc/oidctest"
)

const (
	nonce    = "nonce-nonce-nonce-nonce-nonce-00"
	verifier = "verifier-verifier-verifier-verifier-verifier-00"
)

type fixture struct {
	t        *testing.T
	idp      *oidctest.Provider
	provider *oidc.Provider
	clock    time.Time
	logs     *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), logs: &bytes.Buffer{}}
	now := func() time.Time { return f.clock }
	f.idp = oidctest.New(t, now)
	f.provider = oidc.New(oidc.Config{Issuer: f.idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		Scopes: []string{"openid", "email"}, GroupsClaim: "groups", RedirectURL: oidctest.RedirectURL,
		Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: now})
	return f
}

var ada = oidctest.User{Email: "Ada@Example.com", Groups: []string{"developers", "platform"}}

// signIn takes ada through the provider and returns what the agent makes of
// the ID token.
func (f *fixture) signIn() (oidc.Claims, error) {
	f.t.Helper()
	code := f.idp.Authorize(ada, nonce, oidctest.Challenge(verifier))
	raw, err := f.provider.Exchange(context.Background(), code, verifier)
	if err != nil {
		return oidc.Claims{}, err
	}
	return f.provider.Verify(context.Background(), raw, nonce)
}

func kindOf(err error) oidc.Kind {
	var oe *oidc.Error
	if !errors.As(err, &oe) {
		return -1
	}
	return oe.Kind
}

func TestASignInYieldsWhatTheProviderSaysAboutThePerson(t *testing.T) {
	for _, alg := range []string{"RS256", "ES256"} {
		f := newFixture(t)
		if alg == "ES256" {
			f.idp.UseES256()
		}
		claims, err := f.signIn()
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		if claims.Email != "Ada@Example.com" || claims.EmailVerified != nil || !reflect.DeepEqual(claims.Groups, []string{"developers", "platform"}) || claims.Subject == "" {
			t.Errorf("%s: claims = %+v", alg, claims)
		}
		// One discovery, one key set, one token request: the second sign-in
		// fetches neither of the first two again.
		if _, err := f.signIn(); err != nil {
			t.Fatalf("%s, second sign-in: %v", alg, err)
		}
		want := []string{"GET /.well-known/openid-configuration", "POST /token", "GET /keys", "POST /token"}
		if got := f.idp.Requests(); !slices.Equal(got, want) {
			t.Errorf("%s: requests = %v, want %v", alg, got, want)
		}
	}
}

func TestAnIDTokenIsBelievedOnlyIfEverythingAboutItIsRight(t *testing.T) {
	for _, tt := range []struct {
		why    string
		break_ func(f *fixture, claims map[string]any)
		kind   oidc.Kind
		says   string
	}{
		{"another sign-in's nonce", func(_ *fixture, c map[string]any) { c["nonce"] = "nonce-of-another-sign-in-0000000" }, oidc.NonceMismatch, "another sign-in"},
		{"no nonce", func(_ *fixture, c map[string]any) { delete(c, "nonce") }, oidc.NonceMismatch, "another sign-in"},
		{"issued for another client", func(_ *fixture, c map[string]any) { c["aud"] = "another-app" }, oidc.InvalidToken, "another client"},
		{"this client among others, issued to another", func(_ *fixture, c map[string]any) {
			c["aud"] = []string{"another-app", oidctest.ClientID}
			c["azp"] = "another-app"
		}, oidc.InvalidToken, "another client"},
		{"several audiences and no authorized party", func(_ *fixture, c map[string]any) { c["aud"] = []string{"another-app", oidctest.ClientID} }, oidc.InvalidToken, "another client"},
		{"another issuer", func(_ *fixture, c map[string]any) { c["iss"] = "https://accounts.elsewhere.example" }, oidc.InvalidToken, "issued by"},
		{"expired", func(f *fixture, c map[string]any) { c["exp"] = f.clock.Add(-2 * time.Minute).Unix() }, oidc.InvalidToken, "expired"},
		{"no expiry", func(_ *fixture, c map[string]any) { delete(c, "exp") }, oidc.InvalidToken, "expired"},
		{"issued long ago", func(f *fixture, c map[string]any) {
			c["iat"] = f.clock.Add(-30 * time.Minute).Unix()
			c["exp"] = f.clock.Add(time.Hour).Unix()
		}, oidc.InvalidToken, "too long ago"},
		{"no issue time", func(_ *fixture, c map[string]any) { delete(c, "iat") }, oidc.InvalidToken, "future"},
		{"dated in the future", func(f *fixture, c map[string]any) { c["iat"] = f.clock.Add(5 * time.Minute).Unix() }, oidc.InvalidToken, "future"},
		{"not valid yet", func(f *fixture, c map[string]any) { c["nbf"] = f.clock.Add(5 * time.Minute).Unix() }, oidc.InvalidToken, "not valid yet"},
		{"nobody", func(_ *fixture, c map[string]any) { c["sub"] = "" }, oidc.InvalidToken, "nobody"},
		{"unsigned", func(_ *fixture, c map[string]any) { c["__alg"] = "none" }, oidc.InvalidToken, "RS256 and ES256"},
		{"signed with a shared secret", func(_ *fixture, c map[string]any) { c["__alg"] = "HS256" }, oidc.InvalidToken, "RS256 and ES256"},
	} {
		f := newFixture(t)
		f.idp.Mutate(func(c map[string]any) { tt.break_(f, c) })
		_, err := f.signIn()
		if err == nil || kindOf(err) != tt.kind || !strings.Contains(err.Error(), tt.says) {
			t.Errorf("%s: err = %v (kind %d); want kind %d saying %q", tt.why, err, kindOf(err), tt.kind, tt.says)
		}
		// The same provider, left alone, is believed: the test broke one thing.
		f.idp.Mutate(nil)
		if _, err := f.signIn(); err != nil {
			t.Errorf("%s: the unbroken sign-in fails too: %v", tt.why, err)
		}
	}
}

func TestClocksMayDifferByAMinute(t *testing.T) {
	f := newFixture(t)
	f.idp.Mutate(func(c map[string]any) {
		c["iat"] = f.clock.Add(30 * time.Second).Unix()
		c["exp"] = f.clock.Add(-30 * time.Second).Unix()
	})
	if _, err := f.signIn(); err != nil {
		t.Errorf("half a minute of skew: %v", err)
	}
}

func TestATokenSignedWithAKeyTheProviderDoesNotPublishIsRefused(t *testing.T) {
	f := newFixture(t)
	f.idp.SignWithAnotherKey(t)
	if _, err := f.signIn(); kindOf(err) != oidc.InvalidToken || !strings.Contains(err.Error(), "signature") {
		t.Errorf("err = %v; want the signature refused", err)
	}

	// A signature is checked against the key the token names; a token that
	// claims a P-256 key for an RSA signature finds no key at all.
	f = newFixture(t)
	if _, err := f.signIn(); err != nil {
		t.Fatal(err)
	}
	code := f.idp.Authorize(ada, nonce, oidctest.Challenge(verifier))
	raw, err := f.provider.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(raw, ".")
	for _, forged := range []string{
		parts[0] + "." + parts[1] + ".",
		parts[0] + "." + parts[1] + "." + parts[2][:len(parts[2])-4] + "AAAA",
		parts[0] + "." + parts[1],
		"e30." + parts[1] + "." + parts[2],
		"not a token",
	} {
		if _, err := f.provider.Verify(context.Background(), forged, nonce); kindOf(err) != oidc.InvalidToken {
			t.Errorf("Verify(%.20s…) = %v; want it refused", forged, err)
		}
	}
	// The payload of one token under the signature of another.
	code = f.idp.Authorize(oidctest.User{Email: "mallory@example.com"}, nonce, oidctest.Challenge(verifier))
	other, _ := f.provider.Exchange(context.Background(), code, verifier)
	if _, err := f.provider.Verify(context.Background(), parts[0]+"."+strings.Split(other, ".")[1]+"."+parts[2], nonce); kindOf(err) != oidc.InvalidToken {
		t.Errorf("a swapped payload: %v; want it refused", err)
	}
}

func TestAKeyTheProviderRotatedInIsFetchedAtOnceButNotForEveryToken(t *testing.T) {
	f := newFixture(t)
	if _, err := f.signIn(); err != nil {
		t.Fatal(err)
	}
	keyFetches := func() int {
		n := 0
		for _, r := range f.idp.Requests() {
			if r == "GET /keys" {
				n++
			}
		}
		return n
	}

	f.clock = f.clock.Add(2 * time.Minute)
	f.idp.RotateKey("key-2")
	if _, err := f.signIn(); err != nil {
		t.Fatalf("after a rotation: %v", err)
	}
	if n := keyFetches(); n != 2 {
		t.Errorf("%d key fetches, want 2: the new key id fetches the keys again", n)
	}
	// Another unknown id right away does not: one fetch a minute.
	f.idp.RotateKey("key-3")
	if _, err := f.signIn(); kindOf(err) != oidc.InvalidToken {
		t.Errorf("a second rotation within the minute: %v", err)
	}
	if n := keyFetches(); n != 2 {
		t.Errorf("%d key fetches, want 2 still", n)
	}
	f.clock = f.clock.Add(61 * time.Second)
	if _, err := f.signIn(); err != nil {
		t.Errorf("a minute later: %v", err)
	}
	// And after an hour everything is fetched anew, known key or not.
	before := len(f.idp.Requests())
	f.clock = f.clock.Add(61 * time.Minute)
	if _, err := f.signIn(); err != nil {
		t.Fatal(err)
	}
	if got := f.idp.Requests()[before:]; !slices.Equal(got, []string{"GET /.well-known/openid-configuration", "POST /token", "GET /keys"}) {
		t.Errorf("requests after an hour = %v", got)
	}
}

func TestACodeIsRedeemedOnceAndOnlyWithItsVerifier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	code := f.idp.Authorize(ada, nonce, oidctest.Challenge(verifier))
	if _, err := f.provider.Exchange(ctx, code, verifier); err != nil {
		t.Fatal(err)
	}
	_, err := f.provider.Exchange(ctx, code, verifier)
	if kindOf(err) != oidc.CodeRejected || !strings.Contains(err.Error(), "used before") {
		t.Errorf("a code used twice: %v", err)
	}

	// A code read off a callback, presented without the verifier it belongs to.
	code = f.idp.Authorize(ada, nonce, oidctest.Challenge(verifier))
	if _, err := f.provider.Exchange(ctx, code, "another-verifier-another-verifier-another-0000"); kindOf(err) != oidc.CodeRejected {
		t.Errorf("a code with another verifier: %v", err)
	}
	// The provider's own words go to the log, not into the answer.
	if strings.Contains(err.Error(), "Code not valid") || !strings.Contains(f.logs.String(), "Code not valid") {
		t.Errorf("err = %v, logs = %s", err, f.logs)
	}
}

func TestTheClientSecretReachesTheTokenEndpointAndNothingElse(t *testing.T) {
	for _, postOnly := range []bool{false, true} {
		f := newFixture(t)
		if postOnly {
			f.idp.PostOnly()
		}
		if _, err := f.signIn(); err != nil {
			t.Fatalf("post only %v: %v", postOnly, err)
		}
	}

	f := newFixture(t)
	wrong := oidc.New(oidc.Config{Issuer: f.idp.URL, ClientID: oidctest.ClientID, ClientSecret: "a-wrong-secret-that-must-not-be-seen",
		RedirectURL: oidctest.RedirectURL, Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	code := f.idp.Authorize(ada, nonce, oidctest.Challenge(verifier))
	_, err := wrong.Exchange(context.Background(), code, verifier)
	if kindOf(err) != oidc.Unavailable || !strings.Contains(err.Error(), "SHIPWICK_OIDC_CLIENT_SECRET") {
		t.Errorf("a wrong client secret: %v; want the operator pointed at the variable", err)
	}
	f.idp.Down(true)
	f.clock = f.clock.Add(2 * time.Hour)
	_, downErr := f.signIn()
	if kindOf(downErr) != oidc.Unavailable {
		t.Errorf("provider down: %v", downErr)
	}
	for _, secret := range []string{oidctest.ClientSecret, "a-wrong-secret-that-must-not-be-seen", verifier, code} {
		if strings.Contains(f.logs.String(), secret) || strings.Contains(err.Error()+downErr.Error(), secret) {
			t.Errorf("%q appears in a log line or an error:\n%s", secret, f.logs)
		}
	}
}

func TestAProviderThatCallsItselfSomethingElseIsNotUsed(t *testing.T) {
	f := newFixture(t)
	other := oidc.New(oidc.Config{Issuer: f.idp.URL + "/", ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: oidctest.RedirectURL})
	if _, err := other.AuthorizationEndpoint(context.Background()); kindOf(err) != oidc.Unavailable || !strings.Contains(err.Error(), "SHIPWICK_OIDC_ISSUER") {
		t.Errorf("err = %v; want the issuer mismatch named", err)
	}
	endpoint, err := f.provider.AuthorizationEndpoint(context.Background())
	if err != nil || endpoint != f.idp.URL+"/authorize" {
		t.Errorf("AuthorizationEndpoint = %q, %v", endpoint, err)
	}
}

func TestGroupsAreReadFromTheConfiguredClaim(t *testing.T) {
	f := newFixture(t)
	f.idp.Mutate(func(c map[string]any) { c["groups"] = "only-one" })
	if claims, err := f.signIn(); err != nil || !reflect.DeepEqual(claims.Groups, []string{"only-one"}) {
		t.Errorf("a single name: %+v, %v", claims, err)
	}
	f.idp.Mutate(func(c map[string]any) { c["groups"] = []any{"a", 7, "", "a", "b"} })
	if claims, err := f.signIn(); err != nil || !reflect.DeepEqual(claims.Groups, []string{"a", "b"}) {
		t.Errorf("a list with other things in it: %+v, %v", claims, err)
	}
	f.idp.Mutate(func(c map[string]any) { delete(c, "groups"); c["roles"] = []string{"x"}; c["email_verified"] = "true" })
	claims, err := f.signIn()
	if err != nil || len(claims.Groups) != 0 || claims.EmailVerified == nil || !*claims.EmailVerified {
		t.Errorf("no groups claim: %+v, %v", claims, err)
	}
	f.idp.Mutate(func(c map[string]any) { c["email_verified"] = false })
	if claims, _ := f.signIn(); claims.EmailVerified == nil || *claims.EmailVerified {
		t.Errorf("email_verified false: %+v", claims)
	}
}

func TestAnIssuerMustBeHTTPSUnlessItIsLocal(t *testing.T) {
	for _, ok := range []string{"https://accounts.google.com", "https://login.microsoftonline.com/tenant/v2.0", "http://localhost:8080/realms/shipwick",
		"http://127.0.0.1:5556/dex", "http://10.0.0.5/realms/x", "http://192.168.1.10:8080"} {
		if err := oidc.ValidateIssuer(ok); err != nil {
			t.Errorf("ValidateIssuer(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "accounts.google.com", "http://accounts.example.com", "http://keycloak:8080/realms/x", "ftp://example.com",
		"https://example.com/?tenant=1", "https://example.com/#x", "https://user:pass@example.com", "https://"} {
		if err := oidc.ValidateIssuer(bad); err == nil {
			t.Errorf("ValidateIssuer(%q) accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), "pass") {
			t.Errorf("the error repeats the URL: %v", err)
		}
	}
}
