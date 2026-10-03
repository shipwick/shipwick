package api

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

// Signing in: a person proves who they are to the company's OpenID Connect
// provider, a rule on the agent says what that person may do here, and a
// session carries the answer for a working day.

const (
	// CodeSignInNotConfigured means the agent has no provider: tokens are the
	// only way in. Answered 409.
	CodeSignInNotConfigured = "SIGN_IN_NOT_CONFIGURED"
	// CodeSignInFailed means the provider or the agent refused the sign-in
	// itself: the code, the nonce or the ID token; details: {reason}, one of
	// the SignIn* reasons. Answered 401.
	CodeSignInFailed = "SIGN_IN_FAILED"
	// CodeSignInUnavailable means the provider could not be reached or did
	// not answer like one. Answered 502.
	CodeSignInUnavailable = "SIGN_IN_UNAVAILABLE"
	// CodeAccessNotGranted means the person is who they say and no rule
	// gives them a role; details: {email}. Answered 403.
	CodeAccessNotGranted = "ACCESS_NOT_GRANTED"
	// CodeSessionExpired is CodeTokenExpired for a session; details:
	// {name, expired_at}. Answered 401.
	CodeSessionExpired = "SESSION_EXPIRED"
	// CodeSessionEnded means the session was ended before its time;
	// details: {name, reason}, one of the SessionEnded* reasons. Answered
	// 401, and only to a caller who presented the session itself.
	CodeSessionEnded = "SESSION_ENDED"
)

// Why a sign-in failed, in the details of CodeSignInFailed.
const (
	SignInCodeRejected     = "code_rejected"      // the provider refused the code: used before, expired, or not for this verifier
	SignInInvalidIDToken   = "invalid_id_token"   // signature, issuer, audience or times
	SignInNonceMismatch    = "nonce_mismatch"     // the ID token answers another sign-in
	SignInNonceReused      = "nonce_reused"       // this sign-in was completed before
	SignInEmailMissing     = "email_missing"      // the ID token names no usable e-mail address
	SignInEmailNotVerified = "email_not_verified" // the provider says the address is not verified
)

// Why a session was ended, in the details of CodeSessionEnded.
const (
	SessionEndedRuleRemoved   = "rule_removed"   // no rule gives the person access any more
	SessionEndedAccessChanged = "access_changed" // the rules give the person something else now
	SessionEndedSignedOut     = "signed_out"     // an admin ended it
)

// ActorUser is the kind of caller a signed-in person is; the name is the
// e-mail address.
const ActorUser = "user"

// SessionPrefix starts every session the agent issues, as TokenPrefix starts
// a token.
const SessionPrefix = "sws_"

// SessionLifetime is how long a session lasts, from the sign-in and whatever
// its use: longer than a working day, so nobody signs in twice in one, and
// shorter than the night between two, so that each day starts with what the
// provider says about the person that day.
const SessionLifetime = 10 * time.Hour

// SignInCallbackPath is where the provider sends the browser back to, on the
// dashboard's hostname.
const SignInCallbackPath = "/auth/callback"

// SignInConfig is what the dashboard needs to send a browser to the provider:
// GET /auth. All of it is public; the client secret stays in the agent.
type SignInConfig struct {
	Configured            bool     `json:"configured"`
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	ClientID              string   `json:"client_id"`
	Scopes                []string `json:"scopes"`
	RedirectURI           string   `json:"redirect_uri"`
}

// SignInRequest is what the dashboard's server hands over when the provider
// has sent the browser back: POST /auth/exchange.
type SignInRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	Nonce        string `json:"nonce"`
	RedirectURI  string `json:"redirect_uri"`
}

// SignedIn answers a sign-in. Session is presented like a token; it is the
// only copy, the agent keeps its hash.
type SignedIn struct {
	Session  string        `json:"session"`
	Identity TokenIdentity `json:"identity"`
}

// SignInStatus says, in GET /server, whether people can sign in.
type SignInStatus struct {
	Configured bool   `json:"configured"`
	Issuer     string `json:"issuer"`
}

// Kinds of access rule, the most specific first.
const (
	AccessEmail  = "email"  // one address: ada@example.com
	AccessGroup  = "group"  // a group the provider names in the ID token
	AccessDomain = "domain" // every address at a domain: example.com
)

// MaxAccessRules is how many rules the table holds. They are read on every
// request a session makes.
const MaxAccessRules = 200

// AccessRule gives a role to an address, a group or a domain.
type AccessRule struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Role    Role   `json:"role"`
	// Applications limit a deploy rule as they limit a deploy token; empty:
	// not limited.
	Applications []string  `json:"applications"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    string    `json:"created_by"`
}

// GrantAccessRequest is the body of POST /access/rules. A rule for the same
// kind and subject is replaced.
type GrantAccessRequest struct {
	Kind         string   `json:"kind"`
	Subject      string   `json:"subject"`
	Role         Role     `json:"role"`
	Applications []string `json:"applications,omitempty"`
}

// Session is a person signed in right now, as GET /access/sessions lists it.
type Session struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	Role         Role       `json:"role"`
	Applications []string   `json:"applications"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"` // to the minute; null until first used
}

// SignedOut answers DELETE /access/sessions/{email}.
type SignedOut struct {
	Sessions int `json:"sessions"`
}

// Grant is what the rules give one person.
type Grant struct {
	Role         Role
	Applications []string
}

// Equal reports whether two grants allow the same.
func (g Grant) Equal(other Grant) bool {
	return g.Role == other.Role && slices.Equal(g.Applications, other.Applications)
}

// ResolveAccess finds what the rules give a person. The most specific kind
// of rule that matches decides, and the others are not looked at: a rule for
// the address, else the rules for the person's groups, else the rule for the
// address's domain. A rule for an address can therefore give one person less
// than their group has, as well as more. Of several groups the highest role
// counts; where that is deploy, a group without a limit lifts it, and limits
// add up. Nobody without a matching rule gets anything.
func ResolveAccess(rules []AccessRule, email string, groups []string) (Grant, bool) {
	_, domain, _ := strings.Cut(email, "@")
	var (
		byGroup  []AccessRule
		byDomain *AccessRule
	)
	for i, r := range rules {
		switch {
		case r.Kind == AccessEmail && r.Subject == email:
			return grantOf(r), true
		case r.Kind == AccessGroup && slices.Contains(groups, r.Subject):
			byGroup = append(byGroup, r)
		case r.Kind == AccessDomain && r.Subject == domain:
			byDomain = &rules[i]
		}
	}
	if len(byGroup) > 0 {
		best := grantOf(byGroup[0])
		for _, r := range byGroup[1:] {
			g := grantOf(r)
			switch {
			case g.Role != best.Role:
				if g.Role.Covers(best.Role) {
					best = g
				}
			case len(g.Applications) == 0 || len(best.Applications) == 0:
				best.Applications = []string{}
			default:
				for _, name := range g.Applications {
					if !slices.Contains(best.Applications, name) {
						best.Applications = append(best.Applications, name)
					}
				}
				slices.Sort(best.Applications)
			}
		}
		return best, true
	}
	if byDomain != nil {
		return grantOf(*byDomain), true
	}
	return Grant{}, false
}

func grantOf(r AccessRule) Grant {
	return Grant{Role: r.Role, Applications: append([]string{}, r.Applications...)}
}

// An address as rules and sessions hold it: lowercase ASCII, the characters
// company addresses are made of. It ends up in the audit trail, in logs and
// in a deployment's "by", so nothing else is let through.
var emailPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._%+'-]{0,63}@[a-z0-9.-]{1,253}$`)

// NormalizeEmail returns an e-mail address as rules are matched against it,
// or an error for something that is not one address.
func NormalizeEmail(address string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(address))
	_, domain, _ := strings.Cut(email, "@")
	if len(email) > 254 || !emailPattern.MatchString(email) || spec.ValidateDomain(domain) != nil {
		return "", errors.New("not an e-mail address; use one like ada@example.com")
	}
	return email, nil
}

// maxGroupName bounds a group's name: Microsoft Entra sends ids, Keycloak
// paths, Okta whatever the directory calls the group.
const maxGroupName = 256

// ValidGroup reports whether a group's name can stand in a rule: printable,
// and what the provider would send unchanged.
func ValidGroup(name string) bool {
	if name == "" || len(name) > maxGroupName || strings.TrimSpace(name) != name {
		return false
	}
	for _, c := range name {
		if c < ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// ValidateAccessSubject checks who a rule is for and returns the subject as
// it is stored: an address and a domain in lowercase, a group as it is.
func ValidateAccessSubject(kind, subject string) (string, error) {
	switch kind {
	case AccessEmail:
		return NormalizeEmail(subject)
	case AccessDomain:
		domain := strings.ToLower(strings.TrimSpace(subject))
		if err := spec.ValidateDomain(domain); err != nil || strings.HasPrefix(domain, "*") {
			return "", fmt.Errorf("invalid domain %q: use the part after the @, e.g. example.com", subject)
		}
		return domain, nil
	case AccessGroup:
		if !ValidGroup(subject) {
			return "", fmt.Errorf("invalid group: use the name as the provider sends it, at most %d characters", maxGroupName)
		}
		return subject, nil
	}
	return "", fmt.Errorf("invalid kind %q: use email, group or domain", kind)
}

// ParseAccessSubject reads who a rule is for as the CLI writes it:
// "ada@example.com", "*@example.com" or "group:platform".
func ParseAccessSubject(s string) (kind, subject string, err error) {
	switch {
	case strings.HasPrefix(s, "group:"):
		kind, subject = AccessGroup, strings.TrimPrefix(s, "group:")
	case strings.HasPrefix(s, "*@"):
		kind, subject = AccessDomain, strings.TrimPrefix(s, "*@")
	case strings.Contains(s, "@"):
		kind, subject = AccessEmail, s
	default:
		return "", "", fmt.Errorf("%q is neither an address, a domain nor a group: use ada@example.com, *@example.com or group:platform", s)
	}
	subject, err = ValidateAccessSubject(kind, subject)
	return kind, subject, err
}

// Who is the rule's subject as ParseAccessSubject reads it.
func (r AccessRule) Who() string {
	switch r.Kind {
	case AccessGroup:
		return "group:" + r.Subject
	case AccessDomain:
		return "*@" + r.Subject
	}
	return r.Subject
}
