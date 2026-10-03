// Package outbound is how the agent and the CLI reach servers outside their
// own installation: through the proxy the environment names, and trusting the
// certificate authorities the operator added to the system's.
//
// Both are settings of the whole process, read once at startup: Check
// validates the proxy variables, Trust loads the authorities, and every
// client that leaves the server takes its transport from Transport, or the
// two halves from Proxy and TLSConfig.
package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// EnvCAFile names a PEM file of certificate authorities that are trusted in
// addition to the system's.
const EnvCAFile = "SHIPWICK_CA_FILE"

// proxyVariables are read by net/http in this order of precedence for a
// request over HTTPS and over HTTP: upper case before lower.
var proxyVariables = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"}

// Check reports a proxy variable net/http would refuse, at startup instead of
// on the first request. A proxy's URL may carry a password: the error names
// the variable and the rule, never the value.
func Check(getenv func(string) string) error {
	for _, name := range proxyVariables {
		if _, err := parseProxy(getenv(name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// ProxyHost is the proxy requests over HTTPS go through, as host:port, for a
// log line or a status: "" when there is none. Credentials in the URL are
// left out.
func ProxyHost(getenv func(string) string) string {
	for _, name := range proxyVariables {
		if u, err := parseProxy(getenv(name)); err == nil && u != nil {
			return u.Host
		}
	}
	return ""
}

// parseProxy reads a proxy variable the way net/http does: a URL, or a bare
// host:port that is taken as http.
func parseProxy(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if u, err := url.Parse("http://" + value); err == nil && u.Host != "" && !strings.Contains(value, "://") {
			return u, nil
		}
		return nil, errors.New("not a proxy address; expected a URL such as http://proxy.example.com:3128")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, errors.New("the proxy's scheme must be http, https or socks5; expected a URL such as http://proxy.example.com:3128")
}

// Proxy picks the proxy for a request: the one the environment names
// (HTTPS_PROXY, HTTP_PROXY, unless NO_PROXY covers the host), and none for a
// name inside the installation.
func Proxy(req *http.Request) (*url.URL, error) {
	return proxyFor(req, http.ProxyFromEnvironment)
}

func proxyFor(req *http.Request, fromEnvironment func(*http.Request) (*url.URL, error)) (*url.URL, error) {
	if inNetwork(req.URL.Hostname()) {
		return nil, nil
	}
	u, err := fromEnvironment(req)
	if err != nil {
		// net/http quotes the variable's value, password included.
		return nil, errors.New("the proxy in HTTPS_PROXY or HTTP_PROXY is not a proxy address")
	}
	return u, nil
}

// inNetwork reports a name without a dot: a container or an application on
// one of the installation's own networks, which Docker's resolver answers
// and a proxy elsewhere could neither resolve nor reach. Nobody has to list
// those in NO_PROXY.
func inNetwork(host string) bool {
	return host != "" && !strings.Contains(host, ".") && net.ParseIP(host) == nil
}

// RefusedError is a proxy's answer to CONNECT when it is not 200: the request
// never left for its destination.
type RefusedError struct {
	Proxy  string // host:port
	Target string // host:port the proxy was asked to connect to
	Status string // "403 Forbidden"
}

func (e *RefusedError) Error() string {
	advice := "check that the proxy allows this destination"
	if strings.HasPrefix(e.Status, "407") {
		advice = "it wants a user and a password it accepts, given as http://user:password@host:port in HTTPS_PROXY"
	}
	return fmt.Sprintf("the proxy %s refused to connect to %s (%s): %s", e.Proxy, e.Target, e.Status, advice)
}

// ProxyRefused is a Transport.OnProxyConnectResponse. Without it net/http reports a
// refused CONNECT as the bare text of the status — "Forbidden" — which reads
// as the destination's answer.
func ProxyRefused(_ context.Context, proxyURL *url.URL, connectReq *http.Request, connectRes *http.Response) error {
	if connectRes.StatusCode == http.StatusOK {
		return nil
	}
	// net/http puts the destination of a CONNECT in Host, not in the URL.
	return &RefusedError{Proxy: proxyURL.Host, Target: connectReq.Host, Status: connectRes.Status}
}

// roots are the authorities Trust loaded; nil: the system's alone.
var roots atomic.Pointer[x509.CertPool]

// Trust adds the authorities in the PEM file at path to the ones every
// transport from this package trusts. An empty path changes nothing.
func Trust(path string) error {
	if path == "" {
		return nil
	}
	pool, err := LoadCAFile(path)
	if err != nil {
		return err
	}
	roots.Store(pool)
	return nil
}

// TLSConfig is the TLS half of Transport, for a client that builds its own:
// nil when no authority was added.
func TLSConfig() *tls.Config {
	pool := roots.Load()
	if pool == nil {
		return nil
	}
	return &tls.Config{RootCAs: pool}
}

// Transport is a transport for requests that leave the installation. Each
// call returns a new one, with a connection pool of its own.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	Configure(t)
	return t
}

// Configure gives a transport the proxy and the authorities, and leaves the
// rest of it as it is.
func Configure(t *http.Transport) {
	t.Proxy = Proxy
	t.OnProxyConnectResponse = ProxyRefused
	t.TLSClientConfig = TLSConfig()
}
