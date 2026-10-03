package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/certs"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A certificate of the operator's own belongs to the server, not to an
// application: it is stored under a hostname, and whatever hostname it covers
// — of any application, or of Shipwick itself — is served with it. For those
// hostnames the proxy asks no authority, so they do not wait for DNS either:
// the gate in dns.go protects an authority's rate limit, and there is none.

// InvalidCertificateError is a certificate or key that cannot serve the
// hostname it was supplied for. Reason is a sentence and never quotes the
// input.
type InvalidCertificateError struct{ Reason string }

func (e *InvalidCertificateError) Error() string { return e.Reason }

// WildcardError is returned when a deployment asks for a wildcard hostname
// that no certificate can be had for: an authority issues one only through
// the DNS challenge.
type WildcardError struct {
	Field string // where deploy.yaml names it: "domain", "aliases[0]"
	Host  string
}

func (e *WildcardError) Error() string {
	return fmt.Sprintf("%s: no certificate can be obtained for %s without the DNS challenge", e.Field, e.Host)
}

// Fields shapes the error like a validation error: to the user it is one.
func (e *WildcardError) Fields() []spec.FieldError {
	return []spec.FieldError{{
		Field:    e.Field,
		Message:  "a certificate for a wildcard is issued only through a DNS record, and the agent is not set up for that",
		Expected: fmt.Sprintf("SHIPWICK_CLOUDFLARE_API_TOKEN on the agent, or a certificate of your own: shipwick cert set '%s' --cert fullchain.pem --key privkey.pem", e.Host),
	}}
}

// suppliedCertificate is a stored certificate with what its chain says.
type suppliedCertificate struct {
	stored store.Certificate
	info   certs.Info
}

// certificateCache holds the stored certificates between changes. Routing is
// synced every tick; reading and decrypting every key each time would be work
// for nothing, and the engine is the only one who writes them.
type certificateCache struct {
	mu     sync.Mutex
	loaded bool
	list   []suppliedCertificate
}

func (c *certificateCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded, c.list = false, nil
}

// suppliedCertificates returns the stored certificates, from the cache when
// it is current.
func (e *Engine) suppliedCertificates(ctx context.Context) ([]suppliedCertificate, error) {
	e.certs.mu.Lock()
	defer e.certs.mu.Unlock()
	if e.certs.loaded {
		return e.certs.list, nil
	}
	stored, err := e.store.ListCertificates(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]suppliedCertificate, 0, len(stored))
	for _, c := range stored {
		info, err := certs.Describe(c.CertPEM)
		if err != nil {
			return nil, fmt.Errorf("the stored certificate for %s: %w", c.Hostname, err)
		}
		list = append(list, suppliedCertificate{stored: c, info: info})
	}
	e.certs.loaded, e.certs.list = true, list
	return list, nil
}

// suppliedFor names the stored certificate that covers host, reading only the
// cache: like hostnameReady, it is called where nothing may wait.
func (e *Engine) suppliedFor(host string) (storedUnder string, ok bool) {
	e.certs.mu.Lock()
	defer e.certs.mu.Unlock()
	for _, c := range e.certs.list {
		if certs.Covers(c.info.Subjects, host) {
			return c.stored.Hostname, true
		}
	}
	return "", false
}

// decidedWithoutLookup is the verdict on a hostname that DNS has no say in:
// one a supplied certificate covers, and a wildcard, which cannot be looked
// up at all.
func (e *Engine) decidedWithoutLookup(host string) (ready bool, why string, decided bool) {
	if _, ok := e.suppliedFor(host); ok {
		return true, "", true
	}
	if !spec.IsWildcard(host) {
		return false, "", false
	}
	if e.opts.DNSChallenge {
		return true, "", true
	}
	// Reached only when the certificate a deployment relied on was removed:
	// a deployment that asks for this is refused (checkWildcards).
	return false, fmt.Sprintf("is a wildcard, and no certificate covers it: set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent, or supply one with shipwick cert set '%s'", host), true
}

// checkWildcards refuses a wildcard hostname that could never be served: the
// proxy would ask for its certificate over HTTP, which no authority answers.
func (e *Engine) checkWildcards(ctx context.Context, app spec.App) error {
	if e.opts.DNSChallenge {
		return nil
	}
	for _, h := range hostnamesOf(app).all() {
		if !spec.IsWildcard(h.host) {
			continue
		}
		if _, err := e.suppliedCertificates(ctx); err != nil {
			return err
		}
		if _, ok := e.suppliedFor(h.host); !ok {
			return &WildcardError{Field: h.field, Host: h.host}
		}
	}
	return nil
}

// certificateNote is what a deployment's routing step adds when the hostname
// is served with a supplied certificate: nobody should wait for an authority
// that is not going to be asked.
func (e *Engine) certificateNote(host string) string {
	storedUnder, ok := e.suppliedFor(host)
	switch {
	case !ok:
		return ""
	case storedUnder == host:
		return ", with the certificate you supplied for it"
	}
	return ", with the certificate you supplied for " + storedUnder
}

// proxyCertificates is the stored certificates as the proxy loads them.
func proxyCertificates(list []suppliedCertificate) []proxy.Certificate {
	out := make([]proxy.Certificate, 0, len(list))
	for _, c := range list {
		out = append(out, proxy.Certificate{Hostname: c.stored.Hostname, Subjects: c.info.Subjects, CertPEM: c.stored.CertPEM, KeyPEM: c.stored.KeyPEM})
	}
	return out
}

func certificateView(c suppliedCertificate) api.Certificate {
	return api.Certificate{
		Hostname:  c.stored.Hostname,
		Subjects:  c.info.Subjects,
		Issuer:    c.info.Issuer,
		NotBefore: c.info.NotBefore,
		NotAfter:  c.info.NotAfter,
		CreatedAt: c.stored.CreatedAt,
		UpdatedAt: c.stored.UpdatedAt,
	}
}

// Certificates lists the supplied certificates, in hostname order, without
// their keys.
func (e *Engine) Certificates(ctx context.Context) ([]api.Certificate, error) {
	list, err := e.suppliedCertificates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Certificate, 0, len(list))
	for _, c := range list {
		out = append(out, certificateView(c))
	}
	return out, nil
}

// SetCertificate checks a certificate and its key, stores them under
// hostname, replacing what was there, and has the proxy serve them.
func (e *Engine) SetCertificate(ctx context.Context, hostname, certPEM, keyPEM string) (api.Certificate, error) {
	info, err := certs.Check(hostname, certPEM, keyPEM, time.Now())
	var refused *certs.Error
	if errors.As(err, &refused) {
		return api.Certificate{}, &InvalidCertificateError{Reason: refused.Reason}
	} else if err != nil {
		return api.Certificate{}, err
	}
	stored, err := e.store.SetCertificate(ctx, hostname, certPEM, keyPEM, time.Now())
	if err != nil {
		return api.Certificate{}, err
	}
	e.log.Info("certificate stored", "hostname", hostname, "expires", info.NotAfter.Format(time.RFC3339), "by", actorFrom(ctx))
	e.certificatesChanged(ctx)
	return certificateView(suppliedCertificate{stored: stored, info: info}), nil
}

// DeleteCertificate removes the certificate stored under hostname. The
// hostnames it covered go back to certificates the proxy obtains itself, and
// to waiting for DNS like any other.
func (e *Engine) DeleteCertificate(ctx context.Context, hostname string) error {
	if err := e.store.DeleteCertificate(ctx, hostname); err != nil {
		return err
	}
	e.log.Info("certificate removed", "hostname", hostname, "by", actorFrom(ctx))
	e.certificatesChanged(ctx)
	return nil
}

// certificatesChanged puts a change of the stored certificates into effect
// now rather than at the supervisor's next look. Which hostnames wait for DNS
// has just changed, and the sync finds out: a verdict owed to a certificate
// is never remembered as an answer from DNS (see hostnameCache.decide). A
// proxy that cannot be reached is the supervisor's to retry, as always.
func (e *Engine) certificatesChanged(ctx context.Context) {
	e.certs.forget()
	if err := e.SyncProxy(ctx); err != nil {
		e.log.Warn("could not update the proxy after a certificate changed; retrying in the background", "error", err)
	}
}
