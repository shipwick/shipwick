package commands

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cloudflare"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

func (c *cli) doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the setup end to end and say what to fix",
		Long: `Check the setup end to end: the versions of this shipwick and of the agent,
whether the agent answers and accepts the token, Docker and the proxy on the
server, its active alerts, whether its own state is backed up, certificates of
your own that run out, ports 80 and 443, and for every application with a
domain whether DNS points at the server and https://<domain>/ answers.

Each line says what to do about it. The command exits non-zero when something
is broken (✗), not for things merely worth a look (!).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.doctor(cmd.Context())
		},
	}
}

// report counts what doctor found, so the end of the screen can sum it up.
type report struct {
	c               *cli
	problems, hints int
	// dnsChallenge is what the agent said about its certificates: obtained
	// through a DNS record, so a hostname behind Cloudflare's proxy is in
	// order and "DNS only" is no longer advice to give.
	dnsChallenge bool
	// supplied are the certificates the operator gave the server, and looked
	// the hostnames already looked up: applications that share one by path
	// get one line for it.
	supplied []api.Certificate
	looked   map[string]bool
}

// suppliedFor reports whether a certificate of the operator's own is what
// hostname is served with.
func (r *report) suppliedFor(hostname string) bool {
	_, parent, _ := strings.Cut(hostname, ".")
	for _, cert := range r.supplied {
		for _, subject := range cert.Subjects {
			if strings.EqualFold(subject, hostname) || strings.EqualFold(subject, "*."+parent) {
				return true
			}
		}
	}
	return false
}

// dnsOnly ends the advice to create a record: unproxied, unless certificates
// do not depend on that.
func (r *report) dnsOnly() string {
	if r.dnsChallenge {
		return ""
	}
	return ", DNS only (not proxied)"
}

// behindCloudflare reports a hostname that resolves to Cloudflare's proxy, in
// the agent's words: fine when certificates come through DNS, and otherwise
// the one case where the record is right and a switch is what to change.
func (r *report) behindCloudflare(hostname string, addrs []string) {
	if r.dnsChallenge {
		r.ok("%s → Cloudflare's proxy (%s)", hostname, strings.Join(addrs, ", "))
		return
	}
	r.problem("%s resolves to Cloudflare's proxy (%s), not to the server: turn the proxy off for this record (DNS only), or set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent to keep it on",
		hostname, strings.Join(addrs, ", "))
}

func (r *report) ok(format string, args ...any) { r.c.ui.Success(format, args...) }

func (r *report) hint(format string, args ...any) {
	r.hints++
	r.c.ui.Println(r.c.ui.Styled(ui.Yellow, "!") + " " + fmt.Sprintf(format, args...))
}

func (r *report) problem(format string, args ...any) {
	r.problems++
	r.c.ui.Failure(format, args...)
}

// suppliedCertificates reports the certificates the operator supplied that
// are near their end or past it. Nothing renews those but the operator, and
// an expired one keeps being served.
func (r *report) suppliedCertificates(certificates []api.Certificate, now time.Time) {
	for _, cert := range certificates {
		replace := fmt.Sprintf("shipwick cert set %s --cert <file> --key <file>", shellQuote(cert.Hostname))
		switch left := cert.NotAfter.Sub(now); {
		case left <= 0:
			r.problem("The certificate you supplied for %s expired on %s, and browsers refuse it. Replace it with: %s", cert.Hostname, cert.NotAfter.UTC().Format("2006-01-02"), replace)
		case left < expiresSoon:
			r.hint("The certificate you supplied for %s expires on %s. Replace it before then with: %s", cert.Hostname, cert.NotAfter.UTC().Format("2006-01-02"), replace)
		}
	}
}

// finish sums up and turns problems into a non-zero exit; the lines above
// already said everything.
func (r *report) finish() error {
	r.c.ui.Println()
	switch {
	case r.problems == 0 && r.hints == 0:
		r.c.ui.Println("Everything checks out.")
	case r.problems == 0:
		r.c.ui.Println(fmt.Sprintf("No problems; %s worth a look.", plural(r.hints, "thing")))
	default:
		r.c.ui.Println(fmt.Sprintf("%s found.", plural(r.problems, "problem")))
		return ErrReported
	}
	return nil
}

// release reports where a version stands against the latest release. The
// lookup may have failed; that is one word on the line, not an error.
func (r *report) release(what, current, latest, upgrade string) {
	switch {
	case latest == "":
		r.ok("%s %s (could not check for a newer release)", what, current)
	case !parses(current):
		r.ok("%s %s, a development build; the latest release is %s", what, current, latest)
	case version.Compare(current, latest) < 0:
		r.hint("%s %s; %s is available. Upgrade with: %s", what, current, latest, upgrade)
	default:
		r.ok("%s %s, the latest release", what, current)
	}
}

func (c *cli) doctor(ctx context.Context) error {
	r := &report{c: c}
	local := c.local.withDefaults()

	latest := c.latestReleaseQuietly(ctx)
	r.release("shipwick", c.upgrade.withDefaults().version, latest, "shipwick upgrade")

	target, err := c.resolve()
	if err != nil {
		r.problem("%s", strings.ReplaceAll(err.Error(), "\n\n", ". "))
		return r.finish()
	}
	cl, err := client.New(target.URL, target.Token)
	if err != nil {
		r.problem("%s", err)
		return r.finish()
	}
	health, err := cl.Health(ctx)
	if err != nil {
		r.problem("The agent at %s cannot be reached: %s. Is it running? A remote server is reached over HTTPS at its hostname, or through a tunnel: ssh -L 9000:127.0.0.1:9000 user@server",
			c.describeServer(cl.URL()), cause(err))
		return r.finish()
	}
	r.release("Agent "+c.describeServer(cl.URL())+" runs", health.Version, latest, installerCommand+" (on the server)")

	info, err := cl.Server(ctx)
	switch {
	case client.IsCode(err, api.CodeTokenExpired):
		r.problem("%s", strings.Join(strings.Fields(Render(err)), " "))
		return r.finish()
	case client.IsCode(err, api.CodeUnauthorized):
		r.problem("The agent rejected the API token. Save a valid one with: shipwick login")
		return r.finish()
	case err != nil:
		r.problem("The agent did not answer GET /server: %s", cause(err))
		return r.finish()
	case info.Token.Name != "":
		r.token(info.Token, c.now())
	default:
		r.ok("Token accepted")
	}
	if info.DockerVersion != "" {
		r.ok("Docker %s on the server", info.DockerVersion)
	} else {
		r.hint("The agent did not report a Docker version; see: shipwick server status")
	}
	r.network(info.Network)
	switch p := info.Proxy; {
	case !p.Enabled:
		r.hint("Proxy not configured: domains are not served. Set SHIPWICK_CADDY_ADMIN on the agent")
	case !p.Reachable:
		r.problem("Proxy unreachable: %s. Check the caddy container on the server: docker logs shipwick-caddy-1", p.Error)
	default:
		r.ok("Proxy serving %s", plural(p.Routes, "route"))
		if p.PlainLookups {
			r.hint("The proxy is not Shipwick's image of this version: a name lookup Docker leaves unanswered holds every request for seconds. On the server, run the installer again; an image of your own is built from Dockerfile.caddy")
		}
	}
	r.alerts(info.Alerts)
	r.dnsChallenge = info.Proxy.DNSChallenge
	// An agent from before supplied certificates has none to report.
	if supplied, err := cl.Certificates(ctx); err == nil {
		r.supplied = supplied
		r.suppliedCertificates(supplied, c.now())
	}
	r.checkStateBackup(info.Backups, c.now())

	serverAddrs := c.serverAddresses(ctx, r, local, cl.URL())
	for _, port := range []int{80, 443} {
		if len(serverAddrs) == 0 {
			break
		}
		address := net.JoinHostPort(serverAddrs[0], strconv.Itoa(port))
		if err := local.dial(ctx, address); err != nil {
			r.problem("Port %d is not reachable on %s: open it in the server's firewall; certificates are issued and renewed through ports 80 and 443", port, serverAddrs[0])
			continue
		}
		r.ok("Port %d open on %s", port, serverAddrs[0])
	}

	apps, err := cl.Applications(ctx)
	if err != nil {
		r.problem("The applications could not be listed: %s", cause(err))
		return r.finish()
	}
	for _, app := range apps {
		if app.Domain == "" {
			continue
		}
		hostnames := append(append([]string{app.Domain}, app.Aliases...), app.Redirects...)
		for _, h := range hostnames {
			if spec.IsWildcard(h) {
				r.ok("%s is a wildcard: it has no single record or address to check", h)
				continue
			}
			if r.looked[h] {
				continue
			}
			if r.looked == nil {
				r.looked = map[string]bool{}
			}
			r.looked[h] = true
			c.checkDNS(ctx, r, local, h, serverAddrs)
		}
		if !spec.IsWildcard(app.Domain) {
			c.checkHTTPS(ctx, r, local, app)
		}
	}
	return r.finish()
}

// serverAddresses learns the server's addresses from the agent's own
// hostname: that is what every application's DNS must point at. Through a
// tunnel the agent is 127.0.0.1, and the server's address stays unknown.
func (c *cli) serverAddresses(ctx context.Context, r *report, local localOptions, agentURL string) []string {
	u, err := url.Parse(agentURL)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	switch {
	case host == "localhost", ip != nil && ip.IsLoopback():
		r.hint("The agent is reached through %s (a tunnel, or this machine), so the server's public address is not known: ports and DNS targets are not checked", host)
		return nil
	case ip != nil:
		return []string{host}
	}
	addrs, err := local.lookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		r.problem("%s, the agent's hostname, does not resolve, yet the agent answered: DNS may be set only on this machine. Create an A record for it%s", host, r.dnsOnly())
		return nil
	}
	if cloudflare.Proxied(addrs) {
		// The addresses are Cloudflare's, not the server's: nothing can be
		// dialled or compared through them.
		r.behindCloudflare(host, addrs)
		if r.dnsChallenge {
			r.hint("The server's own address is not known behind Cloudflare's proxy: ports 80 and 443 and the records' targets are not checked")
		}
		return nil
	}
	r.ok("%s → %s", host, strings.Join(addrs, ", "))
	return addrs
}

func (c *cli) checkDNS(ctx context.Context, r *report, local localOptions, hostname string, serverAddrs []string) {
	addrs, err := local.lookupHost(ctx, hostname)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound, err == nil && len(addrs) == 0:
		if len(serverAddrs) > 0 {
			r.problem("%s does not resolve. Create an A record %s → %s%s", hostname, hostname, serverAddrs[0], r.dnsOnly())
		} else {
			r.problem("%s does not resolve. Create an A record for it pointing at the server%s", hostname, r.dnsOnly())
		}
	case err != nil:
		r.hint("%s could not be looked up: %s", hostname, cause(err))
	case cloudflare.Proxied(addrs) && !overlap(addrs, serverAddrs):
		r.behindCloudflare(hostname, addrs)
	case len(serverAddrs) > 0 && !overlap(addrs, serverAddrs):
		r.problem("%s → %s, which is not the server (%s). Point the record at the server",
			hostname, strings.Join(addrs, ", "), strings.Join(serverAddrs, ", "))
	default:
		r.ok("%s → %s", hostname, strings.Join(addrs, ", "))
	}
}

func (c *cli) checkHTTPS(ctx context.Context, r *report, local localOptions, app api.Application) {
	address := "https://" + app.Domain + app.Path + "/"
	// A redirect is an answer too; what it points at is the application's
	// business.
	noFollow := *local.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		r.problem("%s: %s", address, err)
		return
	}
	req.Header.Set("User-Agent", "shipwick/"+version.Version)
	resp, err := noFollow.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) && r.suppliedFor(app.Domain) {
			r.hint("%s is served with the certificate you supplied, which this machine does not trust: %s. A certificate from an authority of your own is trusted only where that authority is installed", address, cause(certErr.Err))
			return
		}
		if errors.As(err, &certErr) {
			r.problem("%s has no valid certificate yet: %s. Caddy obtains one on the first request once DNS points at the server; try again in a minute", address, cause(certErr.Err))
			return
		}
		r.problem("%s does not answer: %s", address, cause(err))
		return
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusBadGateway, resp.StatusCode == http.StatusServiceUnavailable:
		r.hint("%s answers HTTP %d: the proxy is up, but %s is not answering behind it. Look at: shipwick logs %s", address, resp.StatusCode, app.Name, app.Name)
	case resp.StatusCode >= 500:
		r.hint("%s answers HTTP %d. Look at: shipwick logs %s", address, resp.StatusCode, app.Name)
	default:
		r.ok("%s answers HTTP %d", address, resp.StatusCode)
	}
}

// latestReleaseQuietly is `upgrade --check`'s lookup for a screen that must
// not wait on GitHub: a few seconds, and "" when it did not answer.
func (c *cli) latestReleaseQuietly(ctx context.Context) string {
	opts := c.upgrade
	if opts.http == nil {
		opts.http = &http.Client{Timeout: 5 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := opts.withDefaults().latestRelease(ctx)
	if err != nil {
		return ""
	}
	return tag
}

// cause is an error in one line, for a line that already says where.
func cause(err error) string {
	var unreachable *client.UnreachableError
	if errors.As(err, &unreachable) {
		err = unreachable.Err
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	// Windows' socket errors end in a period; the sentence around them has
	// its own.
	return strings.TrimSuffix(strings.SplitN(err.Error(), "\n", 2)[0], ".")
}

func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// publicResolvers are asked before the system's: a record that exists at
// Cloudflare, Google and Quad9 exists for the visitors, whatever this
// machine's own resolver still remembers.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// publicResolversSilent is set once none of them could be reached.
var publicResolversSilent atomic.Bool

// publicLookupHost resolves host through the public resolvers, believing the
// first that knows it and "no such host" from all of them; when none can be
// reached, the system's resolver decides.
func publicLookupHost(ctx context.Context, host string) ([]string, error) {
	// Behind a firewall that lets no DNS out they answer nothing, each for as
	// long as it is given: found out once, not for every hostname.
	if publicResolversSilent.Load() {
		return net.DefaultResolver.LookupHost(ctx, host)
	}
	var notFound, unreachable error
	for _, server := range publicResolvers {
		r := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 2 * time.Second}
				return d.DialContext(ctx, network, server)
			},
		}
		askCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		addrs, err := r.LookupHost(askCtx, host)
		cancel()
		if err == nil && len(addrs) > 0 {
			return addrs, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			notFound = err
			continue
		}
		unreachable = err
	}
	if notFound != nil {
		return nil, notFound
	}
	if unreachable != nil {
		publicResolversSilent.Store(true)
		return net.DefaultResolver.LookupHost(ctx, host)
	}
	return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
}
