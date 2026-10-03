package proxy

import (
	"errors"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/certs"
	"github.com/shipwick/shipwick/pkg/cloudflare"
)

// TLS is what the proxy is told about certificates, next to the routes. The
// zero value is Caddy on its own: a certificate for every hostname, obtained
// over HTTP or TLS from the server itself.
type TLS struct {
	// CloudflareToken, when set, has certificates obtained through the DNS
	// challenge with Cloudflare's API: the only way while Cloudflare's proxy
	// stands in front of the server, and the only way to a wildcard. It is a
	// credential. It reaches Caddy inside the configuration and must not
	// appear anywhere else: see scrub.
	CloudflareToken string
	// Certificates are the ones the operator supplied. The hostnames they
	// cover are served with them, and Caddy asks no authority for those.
	Certificates []Certificate
}

// Certificate is a certificate chain and its key, both PEM, as the agent
// checked and stored them.
type Certificate struct {
	// Hostname is the name it is stored under; it tags the certificate in
	// the configuration and orders the list.
	Hostname string
	// Subjects are the DNS names of the chain's first certificate.
	Subjects []string
	CertPEM  string
	KeyPEM   string
}

func (t TLS) covers(host string) bool {
	for _, c := range t.Certificates {
		if certs.Covers(c.Subjects, host) {
			return true
		}
	}
	return false
}

// app is Caddy's tls app, or nil when there is nothing to say and Caddy's
// defaults apply.
func (t TLS) app() obj {
	app := obj{}
	if t.CloudflareToken != "" {
		// One policy without subjects: it applies to every hostname. With a
		// DNS provider configured Caddy uses the DNS challenge alone, so a
		// certificate no longer depends on the server being reachable from
		// the authority — which it is not, behind Cloudflare's proxy.
		app["automation"] = obj{"policies": []any{obj{
			"issuers": []any{obj{
				"module": "acme",
				"challenges": obj{"dns": obj{"provider": obj{
					"name":      "cloudflare",
					"api_token": t.CloudflareToken,
				}}},
			}},
		}}}
	}
	if len(t.Certificates) > 0 {
		certs := append([]Certificate(nil), t.Certificates...)
		sort.Slice(certs, func(i, j int) bool { return certs[i].Hostname < certs[j].Hostname })
		loaded := make([]any, 0, len(certs))
		for _, c := range certs {
			loaded = append(loaded, obj{"certificate": c.CertPEM, "key": c.KeyPEM, "tags": []string{c.Hostname}})
		}
		app["certificates"] = obj{"load_pem": loaded}
	}
	if len(app) == 0 {
		return nil
	}
	return app
}

// apply adds to the server what follows from t: which of its hostnames Caddy
// must not obtain a certificate for, and whose word about the visitor's
// address it takes.
func (t TLS) apply(server obj, hosts []string) {
	var supplied []string
	for _, h := range hosts {
		if t.covers(h) {
			supplied = append(supplied, h)
		}
	}
	if len(supplied) > 0 {
		sort.Strings(supplied)
		// Caddy would skip them on its own, finding a loaded certificate
		// that matches; said here, it does not depend on that.
		server["automatic_https"] = obj{"skip_certificates": supplied}
	}
	if t.CloudflareToken != "" {
		// Behind Cloudflare every connection comes from Cloudflare, and the
		// visitor's address is in the X-Forwarded-For it sends. Caddy passes
		// that header on to applications only from proxies it trusts; from
		// anyone else it is replaced, since anyone can send one.
		server["trusted_proxies"] = obj{"source": "static", "ranges": cloudflare.Ranges()}
	}
}

// scrub keeps the token out of an error. Caddy's Cloudflare module quotes the
// token it was given when it does not like its form, and an error from
// loading a configuration ends up in the log and in GET /server.
func (t TLS) scrub(err error) error {
	if err == nil || t.CloudflareToken == "" || !strings.Contains(err.Error(), t.CloudflareToken) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.CloudflareToken, "[token]"))
}

// UseCloudflare has certificates obtained through the DNS challenge with
// token, a Cloudflare API token. Call it before the first Sync.
func (c *Caddy) UseCloudflare(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tls.CloudflareToken = token
}

// SetCertificates replaces the certificates the operator supplied. The next
// Sync loads them.
func (c *Caddy) SetCertificates(certs []Certificate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tls.Certificates = certs
}

// splitWildcards separates the hostnames that name one host from the
// wildcards among them.
func splitWildcards(hosts []string) (exact, wildcard []string) {
	for _, h := range hosts {
		if strings.HasPrefix(h, "*.") {
			wildcard = append(wildcard, h)
		} else {
			exact = append(exact, h)
		}
	}
	return exact, wildcard
}
