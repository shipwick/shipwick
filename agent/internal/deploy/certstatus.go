package deploy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// The proxy obtains and renews certificates on its own and says little about
// it. What the agent can know, it learns the way a browser would: it connects
// to the proxy with the hostname as the server name and looks at the
// certificate it is handed. Nothing is verified — a certificate from the
// proxy's own authority on a development machine is reported like one from
// Let's Encrypt, with its issuer — because the question is not whether to
// trust the proxy but what a visitor is going to see.

const (
	// certTick is how often the hostnames are gone through; most of the time
	// none of them is due.
	certTick = 5 * time.Second
	// certCheckEvery is how long a look at a hostname's certificate is good
	// for. A hostname that has none yet is looked at again sooner: somebody
	// is waiting for it, and a certificate usually takes seconds.
	certCheckEvery   = time.Minute
	certRetryPending = 10 * time.Second
	certProbeTimeout = 3 * time.Second

	// certWarnWindow is how much life a certificate has left when it is
	// reported as expiring, and certUrgentWindow when it is reported again.
	// Caddy renews with a third of the lifetime to go — thirty days, for
	// ninety-day certificates — so reaching either means renewal has been
	// failing for a while.
	certWarnWindow   = 14 * 24 * time.Hour
	certUrgentWindow = 3 * 24 * time.Hour
)

// ProbedCertificate is what the proxy presented for a hostname.
type ProbedCertificate struct {
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
}

// ErrNoCertificate is a CertificateProbe's answer when the proxy was reached
// but has no certificate for the hostname: the handshake failed, or what it
// presented is for another name.
var ErrNoCertificate = errors.New("the proxy has no certificate for this hostname")

// CertificateProbe connects to the proxy at addr with serverName and reports
// the certificate it presents. Any error other than ErrNoCertificate means
// the proxy could not be asked.
type CertificateProbe func(ctx context.Context, addr, serverName string) (ProbedCertificate, error)

// DefaultProxyTLSAddr is where the proxy accepts TLS in the compose setup.
const DefaultProxyTLSAddr = "caddy:443"

func probeCertificate(ctx context.Context, addr, serverName string) (ProbedCertificate, error) {
	ctx, cancel := context.WithTimeout(ctx, certProbeTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return ProbedCertificate{}, err
	}
	defer raw.Close()
	// Not verified on purpose: the certificate is reported, not relied on,
	// and nothing is sent over the connection.
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
	if err := conn.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return ProbedCertificate{}, ctx.Err()
		}
		return ProbedCertificate{}, fmt.Errorf("%w: %v", ErrNoCertificate, err)
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || certs[0].VerifyHostname(serverName) != nil {
		return ProbedCertificate{}, ErrNoCertificate
	}
	return ProbedCertificate{Issuer: issuerName(certs[0]), NotBefore: certs[0].NotBefore, NotAfter: certs[0].NotAfter}, nil
}

// issuerName is the issuer as people know it: "Let's Encrypt E7", "Caddy
// Local Authority - ECC Intermediate".
func issuerName(c *x509.Certificate) string {
	org := strings.Join(c.Issuer.Organization, " ")
	switch cn := c.Issuer.CommonName; {
	case org == "":
		return cn
	case cn == "":
		return org
	case strings.Contains(cn, org):
		return cn
	default:
		return org + " " + cn
	}
}

// certWatch remembers the last look at every hostname's certificate.
type certWatch struct {
	mu     sync.Mutex
	states map[string]*certState
}

type certState struct {
	status    string
	issuer    string
	notAfter  time.Time
	message   string
	checkedAt time.Time
	// pending: the hostname has been seen without a certificate, so the one
	// it gets is news. A hostname first seen with one is not — after an
	// agent restart that is every hostname.
	pending bool
	// warned is how far the expiry has been reported: 0 not, 1 as expiring,
	// 2 as about to.
	warned int
}

func newCertWatch() *certWatch {
	return &certWatch{states: map[string]*certState{}}
}

// certWindows are the two thresholds for one certificate. A certificate that
// lives for hours or days — the proxy's own authority issues those for
// *.localhost — would be "expiring" from the day it is issued if it were
// measured in weeks; it gets a quarter of its lifetime, which is past the
// point where Caddy renews.
func certWindows(c ProbedCertificate) (warn, urgent time.Duration) {
	warn = min(certWarnWindow, c.NotAfter.Sub(c.NotBefore)/4)
	return warn, min(certUrgentWindow, warn/4)
}

// checkCertificates looks at the certificate of every hostname the proxy
// serves that is due for a look, and reports what changed: an event when a
// hostname that had no certificate gets one, an event and a notification
// when one comes close to its end. Time is a parameter so tests can drive it.
func (e *Engine) checkCertificates(ctx context.Context, now time.Time) {
	if e.opts.Proxy == nil {
		return
	}
	plans, err := e.routingPlans(ctx)
	if err != nil {
		e.log.Warn("certificates: compute routes", "error", err)
		return
	}
	probe, addr := e.opts.CertificateProbe, e.opts.ProxyTLSAddr
	if probe == nil {
		probe = probeCertificate
	}
	if addr == "" {
		addr = DefaultProxyTLSAddr
	}

	w := e.certStatus
	served := map[string]bool{}
	for _, p := range plans {
		for _, host := range p.hosts.list() {
			served[host] = true
			w.mu.Lock()
			prev := w.states[host]
			if prev == nil {
				prev = &certState{}
				w.states[host] = prev
			}
			if _, waiting := e.waitingForDNS(p.hosts.domain, host); waiting {
				// Not in the proxy, so there is nothing to ask it; and when
				// it gets there, it is looked at right away.
				prev.pending, prev.checkedAt = true, time.Time{}
				w.mu.Unlock()
				continue
			}
			every := certCheckEvery
			if prev.status == api.CertObtaining {
				every = certRetryPending
			}
			due := prev.checkedAt.IsZero() || now.Sub(prev.checkedAt) >= every
			w.mu.Unlock()
			if !due {
				continue
			}
			cert, err := probe(ctx, addr, probeName(host))
			if ctx.Err() != nil {
				return
			}
			e.noteCertificate(ctx, p, host, cert, err, now)
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for host := range w.states {
		if !served[host] {
			delete(w.states, host)
		}
	}
}

// noteCertificate records one look at a hostname's certificate and tells
// whoever should know about what it found.
func (e *Engine) noteCertificate(ctx context.Context, p routingPlan, host string, cert ProbedCertificate, probeErr error, now time.Time) {
	app := store.Application{ID: p.appID, Name: p.app}
	w := e.certStatus
	w.mu.Lock()
	st := w.states[host]
	if st == nil {
		st = &certState{}
		w.states[host] = st
	}
	st.checkedAt = now
	var obtained bool
	var warn int
	switch {
	case errors.Is(probeErr, ErrNoCertificate):
		st.status, st.issuer, st.notAfter = api.CertObtaining, "", time.Time{}
		st.message = "the proxy has no certificate for it yet; HTTPS connections to it fail until it does"
		st.pending = true
	case probeErr != nil:
		// What was known stays known: the proxy restarting is not news
		// about a certificate.
		st.status, st.message = api.CertUnknown, "the proxy could not be asked: "+probeErr.Error()
	default:
		obtained, st.pending = st.pending, false
		st.issuer, st.notAfter = cert.Issuer, cert.NotAfter
		left := cert.NotAfter.Sub(now)
		window, urgent := certWindows(cert)
		switch {
		case left > window:
			st.status, st.message, st.warned = api.CertOK, "", 0
		default:
			st.status, st.message = api.CertExpiring, expiryMessage(left, cert.NotAfter)
			level := 1
			if left <= urgent {
				level = 2
			}
			if level > st.warned {
				st.warned, warn = level, level
			}
		}
	}
	message, issuer, notAfter := st.message, st.issuer, st.notAfter
	w.mu.Unlock()

	if obtained {
		e.appEvent(ctx, app, fmt.Sprintf("Certificate for %s obtained from %s, valid until %s", host, issuer, notAfter.UTC().Format("2006-01-02")))
	}
	if warn > 0 {
		advice := renewalAdvice
		if storedUnder, ok := e.suppliedFor(host); ok {
			advice = fmt.Sprintf(suppliedAdvice, storedUnder)
		}
		text := fmt.Sprintf("The certificate for %s %s. %s", host, message, advice)
		e.log.Warn(text, "app", app.Name)
		if err := e.store.AddEvent(context.WithoutCancel(ctx), app.ID, nil, api.LevelWarn, api.EventApp, text, now); err != nil {
			e.log.Warn("could not record event", "app", app.Name, "error", err)
		}
		e.notify(ctx, notify.Event{Kind: notify.CertificateExpiring, Application: app.Name, Message: app.Name + ": " + lowerFirst(text), At: now})
	}
}

// renewalAdvice follows the news that a certificate is running out.
const renewalAdvice = "The proxy renews certificates by itself, so renewal is failing; its log says why: docker logs shipwick-caddy-1"

// suppliedAdvice is the same for a certificate the operator supplied, which
// nothing renews.
const suppliedAdvice = "It was supplied with `shipwick cert set` and the proxy does not renew it; replace it: shipwick cert set %s --cert fullchain.pem --key privkey.pem"

// probeName is the server name a hostname's certificate is asked for under.
// A wildcard is no name a client can send: a name under it is, and gets the
// certificate the wildcard is served with.
func probeName(host string) string {
	if spec.IsWildcard(host) {
		return "certificate-check" + strings.TrimPrefix(host, "*")
	}
	return host
}

// expiryMessage completes "The certificate for <hostname> …".
func expiryMessage(left time.Duration, notAfter time.Time) string {
	date := notAfter.UTC().Format("2006-01-02")
	switch {
	case left <= 0:
		return "expired on " + date
	case left < 48*time.Hour:
		return fmt.Sprintf("expires in %s, on %s", plural(int(left/time.Hour), "hour"), date)
	}
	return fmt.Sprintf("expires in %s, on %s", plural(int(left/(24*time.Hour)), "day"), date)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// waitingForDNS reports whether host is kept out of the proxy by the DNS
// gate, and why. The gate decides this, not a handshake: a hostname that
// does not point here cannot be asked about. An alias or a redirect waits
// for its application's domain as well as for itself (see routesFor).
func (e *Engine) waitingForDNS(domain, host string) (why string, waiting bool) {
	if ready, why := e.hostnameReady(host); !ready {
		return why, true
	}
	if ready, why := e.hostnameReady(domain); !ready {
		return fmt.Sprintf("waits for %s, which %s", domain, why), true
	}
	return "", false
}

// certificates is the certificate status of every hostname of an application,
// from the last look at each.
func (e *Engine) certificates(hosts hostnames) []api.HostnameCertificate {
	out := []api.HostnameCertificate{}
	for _, host := range hosts.list() {
		c := api.HostnameCertificate{Hostname: host, Status: api.CertUnknown}
		if e.opts.Proxy == nil {
			c.Message = "this agent has no reverse proxy configured"
			out = append(out, c)
			continue
		}
		if why, waiting := e.waitingForDNS(hosts.domain, host); waiting {
			c.Status, c.Message = api.CertWaitingForDNS, why
			out = append(out, c)
			continue
		}
		e.certStatus.mu.Lock()
		st := e.certStatus.states[host]
		switch {
		case st == nil || st.status == "":
			c.Message = "not checked yet"
		default:
			c.Status, c.Issuer, c.Message = st.status, st.issuer, st.message
			if !st.notAfter.IsZero() {
				notAfter := st.notAfter.UTC()
				c.NotAfter = &notAfter
			}
		}
		e.certStatus.mu.Unlock()
		out = append(out, c)
	}
	return out
}
