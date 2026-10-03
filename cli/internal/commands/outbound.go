package commands

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/outbound"
)

// useOutbound gives every request this process makes — to the agent, to
// GitHub, to an application's front page — the proxy of the environment and
// the certificate authorities in SHIPWICK_CA_FILE. The clients of this
// package take the default transport, so that is what is configured.
func (c *cli) useOutbound() error {
	path := strings.TrimSpace(c.getenv(outbound.EnvCAFile))
	if err := outbound.Trust(path); err != nil {
		return fmt.Errorf("%s: %w", outbound.EnvCAFile, err)
	}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		outbound.Configure(transport)
	}
	return nil
}

// network reports what stands between the server and the internet, and the
// one mismatch that explains a pull that fails: an agent behind a proxy next
// to a Docker daemon that knows of none.
func (r *report) network(n *api.NetworkStatus) {
	// An agent from before 0.6 does not say.
	if n == nil {
		return
	}
	switch {
	case n.Proxy != "" && !n.DockerProxy:
		r.hint("The agent goes through the proxy %s, and the Docker daemon on the server has none configured: images are pulled by the daemon, not by the agent. If pulls fail, add \"proxies\" to /etc/docker/daemon.json on the server and restart Docker", n.Proxy)
	case n.Proxy != "":
		r.ok("The agent and the Docker daemon go through a proxy (%s)", n.Proxy)
	}
	if n.ACMEDirectory != "" {
		r.ok("Certificates are obtained from %s", n.ACMEDirectory)
	}
}

// describeNetwork is the line of `server status`; "" when the server reaches
// the internet the plain way.
func describeNetwork(n *api.NetworkStatus) string {
	if n == nil {
		return ""
	}
	var parts []string
	if n.Proxy != "" {
		parts = append(parts, "proxy "+n.Proxy)
	}
	if n.CAFile {
		parts = append(parts, "certificate authorities of its own")
	}
	if len(n.DNSResolvers) > 0 {
		parts = append(parts, "DNS "+strings.Join(n.DNSResolvers, ", "))
	}
	if n.ACMEDirectory != "" {
		parts = append(parts, "certificates from "+n.ACMEDirectory)
	}
	return strings.Join(parts, " · ")
}
