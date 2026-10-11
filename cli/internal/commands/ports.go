package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/cloudflare"
)

// portState is what one TCP connect to a port on the server found out. The
// three failures mean three different things to the person reading them: a
// refusal comes from the server itself (nothing listens), silence comes
// from something in front of it, and anything else is a path problem.
type portState int

const (
	portOpen portState = iota
	portRefused
	portTimedOut
	portUnreachable
)

// classifyDial reads a dial error. A timeout is read from the error's own
// word for it, which the context's deadline and the dialer's agree on; a
// refusal is the errno, which Windows spells with its own number.
func classifyDial(err error) portState {
	if err == nil {
		return portOpen
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return portTimedOut
	}
	if isRefused(err) {
		return portRefused
	}
	return portUnreachable
}

// wsaConnRefused is WSAECONNREFUSED: syscall carries no name for it, and
// syscall.ECONNREFUSED on Windows is an invented number nothing returns.
const wsaConnRefused = syscall.Errno(10061)

func isRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return runtime.GOOS == "windows" && errors.Is(err, wsaConnRefused)
}

// isTLSFailure reports an error from the handshake rather than from the
// connection: something answered on the port, and it was not what a client
// of this hostname expects.
func isTLSFailure(err error) bool {
	var (
		verify   *tls.CertificateVerificationError
		record   tls.RecordHeaderError
		alert    tls.AlertError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &alert) ||
		errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// providerFirewall is the sentence for a port that answers with silence on a
// server that was just set up. The firewall on the server itself does not
// drop packets to a port Docker publishes (see the handbook, Preparing a
// server); the one that does sits in front, at the provider.
const providerFirewall = "a firewall in front of the server drops the packets — the provider's firewall (Hetzner Cloud Firewall, an AWS security group, a DigitalOcean cloud firewall). Open 80 and 443 TCP and 443 UDP there; certificates cannot be obtained until then"

// probePort connects once to port on host, sends nothing, and says what it
// found in a line of the report: open, refused, or silence with the firewall
// named.
func probePort(ctx context.Context, local localOptions, r *report, host string, port int) portState {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	err := local.dial(ctx, address)
	state := classifyDial(err)
	switch state {
	case portOpen:
		r.ok("Port %d open on %s", port, host)
	case portTimedOut:
		r.problem("Port %d on %s timed out after %s: %s", port, host, portCheckTimeout, providerFirewall)
	case portRefused:
		r.problem("Port %d on %s refused the connection: nothing listens there. The proxy is not up; on the server: cd /opt/shipwick && docker compose ps", port, host)
	default:
		r.problem("Port %d on %s could not be reached: %s. Check the address, and the route from this machine to it", port, host, cause(err))
	}
	return state
}

// probeInstalledPorts is the probe `server install` runs when the installer
// is done: the two ports Caddy has to be reachable on, tried from where the
// command runs. A port that is silent right after a healthy agent is a
// firewall at the provider, and the sooner that is said the fewer minutes
// go into Caddy's log. A hostname that resolves to Cloudflare's proxy is
// reached through it, and the server's own ports are then usually open to
// Cloudflare alone (see the handbook, Behind Cloudflare only): a probe from
// here would blame a firewall for doing its job, so none is made.
func (c *cli) probeInstalledPorts(ctx context.Context, local localOptions, t sshTarget, opts installOptions) {
	for _, h := range []string{opts.agentDomain, opts.dashboardDomain} {
		if h == "" {
			continue
		}
		if resolved, err := local.lookupHost(ctx, h); err == nil && cloudflare.Proxied(resolved) {
			c.ui.Println(c.ui.Styled(ui.Yellow, "!") + " Behind Cloudflare only: " + h + " resolves to Cloudflare's proxy, so ports 80 and 443 were not probed from here. Certificates then need a Cloudflare token on the agent; check with: shipwick doctor")
			return
		}
	}
	host := t.host
	if net.ParseIP(host) == nil {
		resolved, err := local.lookupHost(ctx, host)
		if err != nil || len(resolved) == 0 {
			c.ui.Println("Ports 80 and 443 were not checked: " + host + " does not resolve from here.")
			return
		}
		host = resolved[0]
	}
	r := &report{c: c}
	silent := 0
	for _, port := range []int{80, 443} {
		if probePort(ctx, local, r, host, port) == portTimedOut {
			silent++
		}
	}
	if silent > 0 {
		c.ui.Println("  Once the ports are open, Caddy obtains the certificates by itself; check with: shipwick doctor")
	}
}

// unreachableAgent is doctor's first line when the agent did not answer at
// all, with the cause told apart: silence, a refusal, a handshake that
// failed, or something else. Each is a different thing to do next.
func unreachableAgent(where, agentURL string, err error, timeout time.Duration) string {
	loopback := false
	if u, parseErr := url.Parse(agentURL); parseErr == nil {
		host := u.Hostname()
		ip := net.ParseIP(host)
		loopback = host == "localhost" || (ip != nil && ip.IsLoopback())
	}
	switch {
	case classifyDial(err) == portTimedOut:
		return fmt.Sprintf("The agent at %s did not answer within %s: nothing at that address accepted the connection. On a server that was just installed, %s. Otherwise the address is wrong, or the server is down",
			where, timeout, providerFirewall)
	case classifyDial(err) == portRefused && loopback:
		return fmt.Sprintf("The agent at %s refused the connection: nothing listens there. On a laptop, a remote server is reached over HTTPS at its hostname, or through a tunnel: ssh -L 9000:127.0.0.1:9000 user@server", where)
	case classifyDial(err) == portRefused:
		return fmt.Sprintf("The agent at %s refused the connection: nothing listens on that port. The proxy is not running; on the server: cd /opt/shipwick && docker compose ps", where)
	case isTLSFailure(err):
		return fmt.Sprintf("The agent at %s answered, but the TLS handshake failed: %s. The proxy has no certificate for this hostname yet, or serves another: it needs the hostname's DNS record pointing at the server and ports 80 and 443 open from the internet, then a minute. What stands in the way is in Caddy's log, on the server: docker logs shipwick-caddy-1",
			where, cause(err))
	}
	return fmt.Sprintf("The agent at %s cannot be reached: %s. Is it running? A remote server is reached over HTTPS at its hostname, or through a tunnel: ssh -L 9000:127.0.0.1:9000 user@server",
		where, cause(err))
}

// healthTimeout is how long doctor gives the agent's health check: the
// client's own limit covers a slow request, not a silence on connect, and a
// firewall that drops packets would hold the screen for half a minute.
const healthTimeout = 10 * time.Second
