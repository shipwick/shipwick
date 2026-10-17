package deploy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// A deployment is a success when its replicas answer their health check. What
// a visitor gets is another matter: an application that sends its bare
// domain to www, deployed with `redirects: [www.…]` that sends www back,
// passes every check and serves nobody. So once a deployment is done, the
// agent asks the application's own hostname once, the way a visitor would —
// through the proxy — and reads the answer. One answer is judged, a redirect
// to a hostname of `redirects`; every other answer is recorded and left to
// the application. The outcome is one event of the deployment, after
// completed_at: nothing waits for it.

const (
	// redirectProbeTimeout bounds one request.
	redirectProbeTimeout = 5 * time.Second
	// redirectAttempts is how many times the hostname is asked while the
	// proxy cannot answer — it has no certificate for the hostname yet, and
	// the handshake fails. Three minutes with the default RedirectRetry: a
	// certificate takes seconds, or a minute or two through a DNS record.
	redirectAttempts     = 18
	defaultRedirectRetry = 10 * time.Second
)

// RedirectProbe asks GET https://<host><path>/ through the proxy at addr,
// with host as the server name and without following a redirect, and
// reports the status and the Location header. The certificate the proxy
// presents has to name the hostname, so that the answer read is the one the
// proxy gives for that name; its chain is not verified: the connection is
// to the agent's own proxy over the server's network, nothing is sent over
// it, and on a development machine the issuer is Caddy's own authority.
type RedirectProbe func(ctx context.Context, addr, host, path string) (status int, location string, err error)

// unverifiedChainTLS is the probe's client configuration: the certificate the
// proxy presents must name host, and its chain is not verified. The
// connection is to the agent's own proxy over the server's network, nothing is
// sent over it, and on a development machine the issuer is Caddy's own
// authority, which no root store knows.
func unverifiedChainTLS(host string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the proxy presented no certificate")
			}
			return cs.PeerCertificates[0].VerifyHostname(host)
		},
	}
}

func probeRedirect(ctx context.Context, addr, host, path string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, redirectProbeTimeout)
	defer cancel()
	transport := &http.Transport{
		// The URL names the application's hostname; the connection goes to
		// the proxy, which routes by the name.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: unverifiedChainTLS(host),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path+"/", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "shipwick-agent/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		// Caddy without a certificate for the name ends the handshake with
		// an alert, which is all a client sees of "not yet".
		if strings.Contains(err.Error(), "tls:") {
			return 0, "", fmt.Errorf("%w (%v)", ErrNoCertificate, err)
		}
		return 0, "", err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location"), nil
}

// checkRedirectLoop asks the application its own hostname after r has ended,
// and records what it found as an event of the deployment. Nothing is asked
// for a deployment that did not succeed, that runs nothing (a folder, an
// application deployed stopped), or whose hostname is not routed yet.
func (e *Engine) checkRedirectLoop(ctx context.Context, r *rollout) {
	d := r.d
	if e.opts.Proxy == nil || d.Status != api.StatusActive || r.dormant || d.StaticDigest != "" {
		return
	}
	hosts := hostnamesOf(d.Spec)
	if hosts.domain == "" || spec.IsWildcard(hosts.domain) {
		return
	}
	if ready, _ := e.hostnameReady(hosts.domain); !ready {
		return
	}
	probe, addr, retry := e.opts.RedirectProbe, e.opts.ProxyTLSAddr, e.opts.RedirectRetry
	if probe == nil {
		probe = probeRedirect
	}
	if addr == "" {
		addr = DefaultProxyTLSAddr
	}
	if retry == 0 {
		retry = defaultRedirectRetry
	}

	var status int
	var location string
	var err error
	for attempt := 1; ; attempt++ {
		status, location, err = probe(ctx, addr, hosts.domain, hosts.path)
		if err == nil || ctx.Err() != nil || attempt == redirectAttempts {
			break
		}
		select {
		case <-time.After(retry):
		case <-ctx.Done():
		}
	}
	if ctx.Err() != nil {
		// Shutting down: the agent that starts next does not ask again, and
		// a half-answer is worth less than none.
		return
	}

	address := "https://" + hosts.domain + hosts.path + "/"
	target, loop := api.RedirectLoop(status, location, hosts.redirects)
	switch {
	case err != nil:
		e.event(ctx, d, api.LevelWarn, api.EventProbe, fmt.Sprintf("%s could not be asked through the proxy: %v", address, err))
	case loop:
		e.event(ctx, d, api.LevelWarn, api.EventProbe,
			fmt.Sprintf("%s sends its visitors to %s, which `redirects` sends back: every request loops. Make %s the `domain` and %s a redirect, or stop the application redirecting",
				address, target, target, hosts.domain))
	default:
		msg := fmt.Sprintf("%s answers HTTP %d through the proxy", address, status)
		if api.IsRedirect(status) && location != "" {
			msg += " to " + cutAt(location, 200)
		}
		e.event(ctx, d, api.LevelInfo, api.EventProbe, msg)
	}
}

// cutAt shortens s to n characters for an event: a Location is the
// application's to make as long as it likes.
func cutAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
