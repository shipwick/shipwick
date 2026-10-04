// Package oidc is the agent's side of signing in with an OpenID Connect
// provider: it finds the provider's endpoints and keys, redeems an
// authorization code with the client secret, and verifies the ID token that
// comes back. It issues nothing and decides nothing about access; who the
// person is, is all it says.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/pkg/outbound"
)

// Kind says what went wrong, for the caller to answer with.
type Kind int

const (
	// Unavailable: the provider could not be reached, did not answer like a
	// provider, or refused the agent's own credentials.
	Unavailable Kind = iota
	// CodeRejected: the provider refused the authorization code.
	CodeRejected
	// InvalidToken: the ID token is not one this agent may believe.
	InvalidToken
	// NonceMismatch: the ID token is genuine and answers another sign-in.
	NonceMismatch
	// TenantNotAllowed: the ID token is genuine, and for an account of a
	// tenant the operator did not list.
	TenantNotAllowed
)

// Error is a failed sign-in step. Its message never contains the client
// secret, the code or a token.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

func failed(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Config is the provider and the client the agent is registered as there.
type Config struct {
	Issuer   string
	ClientID string
	// ClientSecret is sent to the provider's token endpoint and nowhere else.
	ClientSecret string
	Scopes       []string
	// GroupsClaim names the ID token claim that lists the person's groups.
	GroupsClaim string
	// NameClaim names the ID token claim the person is known by; "email"
	// when empty.
	NameClaim string
	// Tenants are the directories whose accounts may sign in, by the tid
	// claim of their tokens, in lowercase; AnyTenant alone accepts every
	// one. A provider that serves several tenants under one address
	// (see tenants.go) is not used without them.
	Tenants []string
	// RedirectURL is where the provider sends the browser back to: the
	// dashboard's callback.
	RedirectURL string

	Logger *slog.Logger
	// Now is time.Now outside tests.
	Now func() time.Time
}

const (
	requestTimeout = 10 * time.Second
	// maxResponse bounds what is read from the provider: a discovery
	// document, a key set and a token response are a few kilobytes each.
	maxResponse = 1 << 20
	// The discovery document and the keys are fetched again after an hour:
	// a key the provider withdrew is trusted for that long at most. A key id
	// the cache does not know fetches them at once, but not more often than
	// keyRefetchEvery, or tokens with invented key ids would turn every
	// sign-in attempt into a request to the provider.
	cacheFor        = time.Hour
	keyRefetchEvery = time.Minute
)

// Provider talks to one OpenID Connect provider.
type Provider struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger
	now  func() time.Time

	mu        sync.Mutex
	doc       *discovery
	docAt     time.Time
	keys      []key
	keysAt    time.Time
	refetched time.Time
}

// discovery is the part of /.well-known/openid-configuration the agent uses.
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`

	// template is Issuer where it stands for many issuers, one per tenant,
	// and "" for a provider that is one issuer.
	template string
}

// New returns a provider for cfg, which ValidateIssuer has accepted. Nothing
// is fetched until it is needed: the agent starts whether or not the
// provider is up.
func New(cfg Config) *Provider {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{
		cfg: cfg,
		log: cfg.Logger,
		now: cfg.Now,
		// A redirect is never followed: the client secret goes to the
		// endpoint the discovery document names, not to wherever that
		// endpoint points next.
		//
		// The provider is reached the way the webhook and the bucket are:
		// through the proxy, and trusting the authorities of SHIPWICK_CA_FILE.
		// A provider inside the company, with a certificate the company
		// issued, is the usual case there.
		http: &http.Client{Timeout: requestTimeout, Transport: outbound.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// ClientID, Scopes and RedirectURL are what an authorization request carries.
func (p *Provider) ClientID() string    { return p.cfg.ClientID }
func (p *Provider) Scopes() []string    { return slices.Clone(p.cfg.Scopes) }
func (p *Provider) RedirectURL() string { return p.cfg.RedirectURL }

// ValidateURL checks a URL the agent is to send requests to: absolute, and
// https — plain http only towards the machine itself or a private network,
// the rule the webhook follows.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return errors.New("must be an absolute URL such as https://accounts.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocal(u.Hostname()) {
			return errors.New("must use https; http is allowed only for localhost and private addresses")
		}
	default:
		return errors.New("must use https")
	}
	return nil
}

// ValidateIssuer checks an issuer URL: ValidateURL, and neither query nor
// fragment, which an issuer cannot have.
func ValidateIssuer(raw string) error {
	if err := ValidateURL(raw); err != nil {
		return err
	}
	if u, _ := url.Parse(raw); u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be the issuer URL itself, without a query or a fragment")
	}
	return nil
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// AuthorizationEndpoint is where a browser is sent to sign in.
func (p *Provider) AuthorizationEndpoint(ctx context.Context) (string, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	return doc.AuthorizationEndpoint, nil
}

func (p *Provider) discover(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.doc != nil && p.now().Sub(p.docAt) < cacheFor {
		return p.doc, nil
	}
	var doc discovery
	if err := p.getJSON(ctx, strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
		return nil, failed(Unavailable, "the sign-in provider's configuration could not be read: %v", err)
	}
	// A document that names another issuer describes another provider: its
	// keys would sign for whoever it is.
	switch {
	case doc.Issuer == p.cfg.Issuer:
	case TenantTemplate(p.cfg.Issuer) != "" && doc.Issuer == TenantTemplate(p.cfg.Issuer):
		// The one case in which the document may name another issuer than
		// the configured one: see tenants.go.
		if len(p.cfg.Tenants) == 0 {
			return nil, failed(Unavailable, "the sign-in provider signs in the accounts of every Microsoft Entra tenant, and the agent is not told which may sign in here: set SHIPWICK_OIDC_TENANTS to their tenant ids")
		}
		doc.template = doc.Issuer
	default:
		return nil, failed(Unavailable, "the sign-in provider calls itself %q, and the agent is configured with %q: set SHIPWICK_OIDC_ISSUER to the issuer exactly as the provider states it", printable(doc.Issuer), p.cfg.Issuer)
	}
	for name, endpoint := range map[string]string{"authorization_endpoint": doc.AuthorizationEndpoint, "token_endpoint": doc.TokenEndpoint, "jwks_uri": doc.JWKSURI} {
		if err := ValidateURL(endpoint); err != nil {
			return nil, failed(Unavailable, "the sign-in provider's configuration is not usable: %s %v", name, err)
		}
	}
	p.doc, p.docAt = &doc, p.now()
	return p.doc, nil
}

// getJSON fetches a document from the provider. Errors name the host and
// what happened, never a body.
func (p *Provider) getJSON(ctx context.Context, endpoint string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", req.URL.Host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return transportError(err)
	}
	if len(body) > maxResponse {
		return fmt.Errorf("%s answered with more than %d bytes", req.URL.Host, maxResponse)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s did not answer with JSON", req.URL.Host)
	}
	return nil
}

// transportError drops the URL a *url.Error repeats: the token endpoint's
// carries nothing secret, and the rule is easier kept without exceptions.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// tokenResponse is what the token endpoint answers: the ID token, or an
// OAuth error.
type tokenResponse struct {
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange redeems an authorization code for an ID token. The verifier is
// the one whose challenge went out with the authorization request: the
// provider refuses a code presented without it, which is what makes a code
// read off a callback worthless.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURL},
		"code_verifier": {verifier},
	}
	// Basic authentication is what every provider must support; a provider
	// that lists its methods without it gets the secret in the form.
	inForm := len(doc.TokenAuthMethods) > 0 && !slices.Contains(doc.TokenAuthMethods, "client_secret_basic") &&
		slices.Contains(doc.TokenAuthMethods, "client_secret_post")
	if inForm {
		form.Set("client_id", p.cfg.ClientID)
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", failed(Unavailable, "the sign-in provider's token endpoint is not usable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if !inForm {
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", failed(Unavailable, "the sign-in provider could not be reached: %v", transportError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", failed(Unavailable, "the sign-in provider could not be reached: %v", transportError(err))
	}
	var answer tokenResponse
	// Not JSON: judged by the status below.
	_ = json.Unmarshal(body, &answer)

	switch {
	case resp.StatusCode == http.StatusOK && answer.IDToken != "":
		return answer.IDToken, nil
	case resp.StatusCode == http.StatusOK:
		return "", failed(Unavailable, "the sign-in provider answered without an ID token: the openid scope must be among SHIPWICK_OIDC_SCOPES and allowed for the client")
	case answer.Error == "invalid_grant":
		// The description is the provider's and may say anything: it goes
		// to the log, not to whoever sent the code.
		p.log.Warn("the sign-in provider refused an authorization code", "error", answer.Error, "description", printable(answer.ErrorDescription))
		return "", failed(CodeRejected, "the sign-in provider refused the code: it was used before, has expired, or belongs to another sign-in. Sign in again")
	case answer.Error == "invalid_client" || answer.Error == "unauthorized_client" || resp.StatusCode == http.StatusUnauthorized:
		p.log.Error("the sign-in provider refused the agent's client credentials", "error", printable(answer.Error), "description", printable(answer.ErrorDescription))
		return "", failed(Unavailable, "the sign-in provider refused the agent's client id or secret: check SHIPWICK_OIDC_CLIENT_ID and SHIPWICK_OIDC_CLIENT_SECRET on the agent")
	case answer.Error != "":
		p.log.Warn("the sign-in provider refused a token request", "error", printable(answer.Error), "description", printable(answer.ErrorDescription))
		return "", failed(CodeRejected, "the sign-in provider refused the sign-in (%s). Sign in again", printable(answer.Error))
	}
	return "", failed(Unavailable, "the sign-in provider's token endpoint answered %d", resp.StatusCode)
}

// printable is text from the provider as it may be logged or repeated: what
// is visible, at a length a line can take.
func printable(s string) string {
	var b strings.Builder
	for _, c := range s {
		if b.Len() >= 200 {
			break
		}
		if c < ' ' || c == 0x7f {
			c = ' '
		}
		b.WriteRune(c)
	}
	return b.String()
}
