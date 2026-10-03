package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/shipwick/shipwick/pkg/outbound"
)

const (
	// EnvCAFile names a PEM file of certificate authorities the agent trusts
	// in addition to the system's, for the webhook and the bucket.
	EnvCAFile = outbound.EnvCAFile
	// EnvDNSResolvers says who is asked whether a hostname points at the
	// server: "system", or name servers by address. Unset: the public ones.
	EnvDNSResolvers = "SHIPWICK_DNS_RESOLVERS"
	// EnvACMEDirectory is the directory of the certificate authority the
	// proxy obtains certificates from, instead of Let's Encrypt.
	EnvACMEDirectory = "SHIPWICK_ACME_DIRECTORY"
)

// SystemResolver is the value of SHIPWICK_DNS_RESOLVERS that asks the
// server's own resolver.
const SystemResolver = "system"

// Outbound is how the agent, and the proxy it configures, reach what is
// outside the server when the way there is not the open internet.
type Outbound struct {
	// CAFile is the path of the PEM file; "" when none is set. It is read by
	// outbound.Trust, not here: Load reads no files.
	CAFile string
	// Proxy is the proxy the agent's own requests go through, host:port and
	// never more: its URL may hold a password. "" when the environment names
	// none.
	Proxy string
	// SystemDNS asks the server's resolver about hostnames; otherwise
	// DNSResolvers, each "address:port", and the public ones when it is empty.
	SystemDNS    bool
	DNSResolvers []string
	// ACMEDirectory is the directory URL; "" is the proxy's default.
	ACMEDirectory string
}

func (c *Config) loadOutbound(getenv func(string) string) error {
	if err := outbound.Check(getenv); err != nil {
		return err
	}
	c.Outbound.Proxy = outbound.ProxyHost(getenv)

	c.Outbound.CAFile = strings.TrimSpace(getenv(EnvCAFile))
	if f := c.Outbound.CAFile; f != "" && !filepath.IsAbs(f) {
		return fmt.Errorf("%s: %q is not an absolute path; give the file's full path as the agent sees it, such as /etc/shipwick/ca.pem", EnvCAFile, f)
	}

	switch raw := strings.TrimSpace(getenv(EnvDNSResolvers)); {
	case raw == "":
	case strings.EqualFold(raw, SystemResolver):
		c.Outbound.SystemDNS = true
	default:
		for _, field := range strings.Split(raw, ",") {
			server, err := resolverAddress(strings.TrimSpace(field))
			if err != nil {
				return fmt.Errorf("%s: %w (expected %q, or name servers by address such as \"10.0.0.2,10.0.0.3\")", EnvDNSResolvers, err, SystemResolver)
			}
			c.Outbound.DNSResolvers = append(c.Outbound.DNSResolvers, server)
		}
	}

	c.Outbound.ACMEDirectory = strings.TrimSpace(getenv(EnvACMEDirectory))
	if dir := c.Outbound.ACMEDirectory; dir != "" {
		if err := validateACMEDirectory(dir); err != nil {
			return fmt.Errorf("%s: %w", EnvACMEDirectory, err)
		}
		if c.CaddyAdmin == "" {
			return fmt.Errorf("%s needs a reverse proxy to obtain certificates from it: set %s as well", EnvACMEDirectory, EnvCaddyAdmin)
		}
	}
	return nil
}

// resolverAddress is a name server as the resolver dials it: an address, with
// port 53 unless another is given.
func resolverAddress(field string) (string, error) {
	host, port := field, "53"
	if h, p, err := net.SplitHostPort(field); err == nil {
		host, port = h, p
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("%q is not an IP address", field)
	}
	if n, err := net.LookupPort("udp", port); err != nil || n == 0 {
		return "", fmt.Errorf("%q has no valid port", field)
	}
	return net.JoinHostPort(host, port), nil
}

// validateACMEDirectory accepts the URL of an ACME directory. It goes into
// the proxy's configuration, where braces would be read as placeholders.
func validateACMEDirectory(dir string) error {
	u, err := url.Parse(dir)
	switch {
	case err != nil || u.Host == "" || u.Scheme != "https":
		return errors.New("expected the https URL of an ACME directory, such as https://ca.example.internal/acme/acme/directory")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return errors.New("the directory must be a plain URL, without credentials or a query")
	case strings.ContainsAny(dir, "{}\"\\ \t\r\n"):
		return errors.New("the directory's URL must not contain braces, quotes or spaces")
	}
	return nil
}
