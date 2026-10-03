package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"time"
)

// Claims is what a verified ID token says about the person.
type Claims struct {
	Subject string
	// Email is as the provider wrote it; EmailVerified is nil when the
	// provider does not say.
	Email         string
	EmailVerified *bool
	Groups        []string
}

const (
	// maxIDToken bounds an ID token: one that lists a few hundred groups
	// stays far below it.
	maxIDToken = 64 << 10
	// clockSkew is how far the provider's clock and the agent's may differ.
	clockSkew = time.Minute
	// maxAge is how old an ID token may be when it is presented. One that
	// comes straight from the token endpoint is seconds old; an older one
	// was kept from an earlier sign-in.
	maxAge = 10 * time.Minute
	// maxGroups bounds the groups kept from a token.
	maxGroups = 500
	// minRSABits refuses keys that are too short to sign for anyone.
	minRSABits = 2048
)

// key is one of the provider's signing keys.
type key struct {
	id  string
	rsa *rsa.PublicKey
	ec  *ecdsa.PublicKey
}

type jwks struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	} `json:"keys"`
}

// keysFor returns the provider's keys, fetching them when the cache is old,
// and at once when it does not know id — the provider has rotated its keys —
// though not more often than keyRefetchEvery.
func (p *Provider) keysFor(ctx context.Context, id string) ([]key, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	known := func() bool {
		for _, k := range p.keys {
			if k.id == id {
				return true
			}
		}
		return false
	}
	fresh := p.keys != nil && now.Sub(p.keysAt) < cacheFor
	if fresh && (id == "" || known() || now.Sub(p.refetched) < keyRefetchEvery) {
		return p.keys, nil
	}
	p.refetched = now

	var set jwks
	if err := p.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		return nil, failed(Unavailable, "the sign-in provider's keys could not be read: %v", err)
	}
	keys := []key{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch {
		case k.Kty == "RSA":
			n, e := decodeInt(k.N), decodeInt(k.E)
			if n == nil || e == nil || n.BitLen() < minRSABits || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
				continue
			}
			keys = append(keys, key{id: k.Kid, rsa: &rsa.PublicKey{N: n, E: int(e.Int64())}})
		case k.Kty == "EC" && k.Crv == "P-256":
			x, errX := base64.RawURLEncoding.DecodeString(k.X)
			y, errY := base64.RawURLEncoding.DecodeString(k.Y)
			if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			// Parsing the point refuses one that is not on the curve.
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue
			}
			keys = append(keys, key{id: k.Kid, ec: pub})
		}
	}
	p.keys, p.keysAt = keys, now
	return p.keys, nil
}

func decodeInt(s string) *big.Int {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return new(big.Int).SetBytes(raw)
}

// audience is the aud claim: one client id, or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*a = []string{one}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(a))
}

// flag is a boolean some providers send as the string "true".
type flag bool

func (f *flag) UnmarshalJSON(data []byte) error {
	*f = flag(bytes.Equal(data, []byte("true")) || bytes.Equal(data, []byte(`"true"`)))
	return nil
}

type idToken struct {
	Issuer        string   `json:"iss"`
	Subject       string   `json:"sub"`
	Audience      audience `json:"aud"`
	Party         string   `json:"azp"`
	Expires       *float64 `json:"exp"`
	IssuedAt      *float64 `json:"iat"`
	NotBefore     *float64 `json:"nbf"`
	Nonce         string   `json:"nonce"`
	Email         string   `json:"email"`
	EmailVerified *flag    `json:"email_verified"`
}

// Verify checks an ID token and returns what it says. The token is believed
// only if one of the provider's keys signed it (RS256 or ES256; a token that
// names another algorithm, "none" among them, is refused before any key is
// looked at), if it was issued by this provider for this client, is within
// its time and no older than maxAge, and carries the nonce of this sign-in.
func (p *Provider) Verify(ctx context.Context, raw, nonce string) (Claims, error) {
	invalid := func(why string) (Claims, error) {
		return Claims{}, failed(InvalidToken, "the sign-in provider's ID token was not accepted: %s", why)
	}
	parts := strings.Split(raw, ".")
	if len(raw) > maxIDToken || len(parts) != 3 {
		return invalid("it is not a signed token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if !decodeSegment(parts[0], &header) {
		return invalid("it is not a signed token")
	}
	if header.Alg != "RS256" && header.Alg != "ES256" {
		return invalid("it is signed with " + printable(header.Alg) + "; RS256 and ES256 are accepted")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return invalid("it is not a signed token")
	}
	keys, err := p.keysFor(ctx, header.Kid)
	if err != nil {
		return Claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !signedBy(keys, header.Alg, header.Kid, digest[:], signature) {
		return invalid("its signature is not by one of the provider's keys")
	}

	var token idToken
	if !decodeSegment(parts[1], &token) {
		return invalid("its claims cannot be read")
	}
	now := p.now()
	switch {
	case token.Issuer != p.cfg.Issuer:
		return invalid("it was issued by " + printable(token.Issuer))
	case !contains(token.Audience, p.cfg.ClientID):
		return invalid("it was issued for another client")
	// With several audiences, or wherever the provider names the party the
	// token was issued to, that party has to be this client.
	case len(token.Audience) > 1 && token.Party == "", token.Party != "" && token.Party != p.cfg.ClientID:
		return invalid("it was issued to another client")
	case token.Expires == nil || !now.Before(unix(*token.Expires).Add(clockSkew)):
		return invalid("it has expired")
	case token.IssuedAt == nil || unix(*token.IssuedAt).After(now.Add(clockSkew)):
		return invalid("it is dated in the future; compare the server's clock with the provider's")
	case now.Sub(unix(*token.IssuedAt)) > maxAge+clockSkew:
		return invalid("it was issued too long ago")
	case token.NotBefore != nil && unix(*token.NotBefore).After(now.Add(clockSkew)):
		return invalid("it is not valid yet")
	case token.Subject == "":
		return invalid("it names nobody")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(nonce)) != 1 {
		return Claims{}, failed(NonceMismatch, "the sign-in provider's ID token answers another sign-in than this one. Sign in again")
	}

	claims := Claims{Subject: token.Subject, Email: token.Email}
	if token.EmailVerified != nil {
		verified := bool(*token.EmailVerified)
		claims.EmailVerified = &verified
	}
	claims.Groups = groupsOf(parts[1], p.cfg.GroupsClaim)
	return claims, nil
}

func decodeSegment(segment string, v any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	return err == nil && json.Unmarshal(raw, v) == nil
}

func unix(seconds float64) time.Time {
	return time.Unix(int64(seconds), 0)
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// signedBy reports whether one of keys made signature over digest. A token
// that names its key is checked against that key alone; one that does not,
// against every key of the algorithm's kind.
func signedBy(keys []key, alg, id string, digest, signature []byte) bool {
	for _, k := range keys {
		if id != "" && k.id != id {
			continue
		}
		switch {
		case alg == "RS256" && k.rsa != nil:
			if rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, digest, signature) == nil {
				return true
			}
		case alg == "ES256" && k.ec != nil:
			// A JWT carries r and s side by side, 32 bytes each.
			if len(signature) != 64 {
				continue
			}
			r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
			if ecdsa.Verify(k.ec, digest, r, s) {
				return true
			}
		}
	}
	return false
}

// groupsOf reads the groups claim: a list of names, or one name. Anything
// else in it is skipped.
func groupsOf(payload, claim string) []string {
	groups := []string{}
	if claim == "" {
		return groups
	}
	var all map[string]json.RawMessage
	if !decodeSegment(payload, &all) {
		return groups
	}
	var list []json.RawMessage
	var one string
	switch {
	case json.Unmarshal(all[claim], &one) == nil:
		list = []json.RawMessage{all[claim]}
	case json.Unmarshal(all[claim], &list) != nil:
		return groups
	}
	for _, item := range list {
		var name string
		if json.Unmarshal(item, &name) == nil && name != "" && len(groups) < maxGroups && !contains(groups, name) {
			groups = append(groups, name)
		}
	}
	return groups
}
