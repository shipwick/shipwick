package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cloudflare"
)

// Why a certificate did not come is in Caddy's log and nowhere else: its
// admin API does not say, and the handshake that certstatus.go makes only
// shows that there is none. So the agent reads the log. Caddy's own messages
// go to its standard error (the access log has its standard output, see
// proxy.Build), and the agent follows that stream the way it follows the
// access log, keeps the last failure it saw for each hostname, and puts it
// where the certificate's status is shown — the application's detail, its
// events, `shipwick status` and `doctor` — in a sentence that says what to
// look at, with Caddy's own words next to it.

// proxyErrorFollower is a Runtime that can follow what the proxy writes to
// its standard error; *docker.Runtime is one.
type proxyErrorFollower interface {
	FollowErrors(ctx context.Context, id string, since time.Time, emit func(line []byte)) error
}

// maxFailureDetail bounds what one log line can make the agent remember.
const maxFailureDetail = 400

// certFailure is one failure as the proxy logged it.
type certFailure struct {
	at        time.Time
	challenge string // tls-alpn-01, http-01, dns-01, or ""
	problem   string // the ACME problem type; "" when the authority did not answer
	detail    string // the authority's detail, or the error text
	issuer    string // as Caddy names it: acme-v02.api.letsencrypt.org-directory
}

// caddyTLSEntry is the part of a Caddy log line about certificates that is
// read: CertMagic's "could not get certificate from issuer" and "will retry",
// and acmez's "challenge failed" and "validating authorization".
type caddyTLSEntry struct {
	TS            float64         `json:"ts"`
	Level         string          `json:"level"`
	Logger        string          `json:"logger"`
	Msg           string          `json:"msg"`
	Identifier    string          `json:"identifier"`
	Issuer        string          `json:"issuer"`
	Error         string          `json:"error"`
	ChallengeType string          `json:"challenge_type"`
	Problem       json.RawMessage `json:"problem"`
}

const acmeErrorPrefix = "urn:ietf:params:acme:error:"

var (
	// obtainError is the error of a retry line: "[host] Obtain: what went wrong".
	obtainError = regexp.MustCompile(`^\[([^\]]+)\] (?:Obtain|Renew): (.*)$`)
	// acmeProblem is an authority's answer as acmez prints it:
	// "HTTP 400 urn:ietf:params:acme:error:connection - the detail".
	acmeProblem = regexp.MustCompile(`HTTP \d{3} (urn:ietf:params:acme:error:[A-Za-z]+) - (.*)$`)
	// orderSuffix is what CertMagic appends to a failure: the order's URL
	// and the authority's, neither of which helps anyone here.
	orderSuffix = regexp.MustCompile(` \((?:order|ca)=[^)]*\)`)
)

// parseProxyErrorLine reads one line of the proxy's standard error and
// reports the hostname it is about, when it is about a certificate that
// could not be obtained or renewed. Everything else — and Caddy says a great
// deal — is reported as not ok.
func parseProxyErrorLine(line []byte) (host string, f certFailure, ok bool) {
	var raw caddyTLSEntry
	if err := json.Unmarshal(line, &raw); err != nil || raw.Level != "error" || !strings.HasPrefix(raw.Logger, "tls.") || raw.TS <= 0 {
		return "", certFailure{}, false
	}
	// Microseconds, rounded: a float of seconds since 1970 carries no more,
	// and truncating would turn .1 into .099999.
	sec, frac := math.Modf(raw.TS)
	f.at = time.Unix(int64(sec), int64(math.Round(frac*1e6))*1000).UTC()
	f.issuer, f.challenge = raw.Issuer, raw.ChallengeType
	host = strings.ToLower(raw.Identifier)
	switch raw.Msg {
	case "could not get certificate from issuer":
		f.detail = raw.Error
	case "challenge failed", "validating authorization":
		var problem struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(raw.Problem, &problem); err == nil {
			f.problem, f.detail = problem.Type, problem.Detail
		} else {
			// A problem logged as text rather than as an object.
			var text string
			_ = json.Unmarshal(raw.Problem, &text)
			f.detail = text
		}
	case "will retry", "final attempt; giving up":
		m := obtainError.FindStringSubmatch(raw.Error)
		if m == nil {
			return "", certFailure{}, false
		}
		host, f.detail = strings.ToLower(m[1]), m[2]
	default:
		return "", certFailure{}, false
	}
	if host == "" {
		return "", certFailure{}, false
	}
	f.detail = strings.TrimSpace(orderSuffix.ReplaceAllString(f.detail, ""))
	if m := acmeProblem.FindStringSubmatch(f.detail); m != nil && f.problem == "" {
		f.problem, f.detail = m[1], m[2]
	}
	if f.challenge == "" {
		f.challenge = challengeIn(f.detail)
	}
	if len(f.detail) > maxFailureDetail {
		f.detail = f.detail[:maxFailureDetail] + "…"
	}
	return host, f, true
}

// challengeIn names the challenge a failure's text is about, when it says.
func challengeIn(text string) string {
	switch {
	case strings.Contains(text, "tls-alpn-01"):
		return "tls-alpn-01"
	case strings.Contains(text, "http-01"):
		return "http-01"
	case strings.Contains(text, "dns-01"), strings.Contains(text, "temporary record"), strings.Contains(text, "propagat"), strings.Contains(text, "determine zone"):
		return "dns-01"
	}
	return ""
}

// noteProxyError records a failure the proxy logged against its hostname. A
// line older than the failure kept — Docker repeats the lines of the moment a
// stream was cut — is ignored. A line without the challenge inherits it from
// the failure a moment before: Caddy logs the authority's answer with the
// challenge first, and its own summary without, seconds later.
func (e *Engine) noteProxyError(line []byte) (host string, f certFailure, ok bool) {
	host, f, ok = parseProxyErrorLine(line)
	if !ok {
		return "", certFailure{}, false
	}
	w := e.certStatus
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.states[host]
	if st == nil {
		st = &certState{}
		w.states[host] = st
	}
	if prev := st.failure; prev != nil {
		if !f.at.After(prev.at) {
			return "", certFailure{}, false
		}
		if f.at.Sub(prev.at) < time.Minute {
			if f.challenge == "" {
				f.challenge = prev.challenge
			}
			if f.problem == "" && prev.problem != "" && strings.Contains(f.detail, prev.detail) {
				f.problem = prev.problem
			}
		}
	}
	st.failure = &f
	return host, f, true
}

// failureReason is the sentence for a failure: the cause first, then what to
// look at. The two that a real server produced read as "Let's Encrypt could
// not connect to the server on port 443 (tls-alpn-01): a firewall in front
// of it?" and "the DNS record for the challenge could not be created: the
// token cannot edit DNS records of example.com: it needs Zone → DNS → Edit
// (DNS Settings, DNS Firewall and DNS Views are other permissions)".
func (e *Engine) failureReason(host string, f certFailure) string {
	ca := e.authorityName(f.issuer)
	port := "443"
	if f.challenge == "http-01" {
		port = "80"
	}
	problem := strings.TrimPrefix(f.problem, acmeErrorPrefix)
	switch {
	case strings.Contains(f.detail, "adding temporary record"), strings.Contains(f.detail, "presenting for challenge"), strings.Contains(f.detail, "could not determine zone"):
		return "the DNS record for the challenge could not be created: " + e.dnsRecordReason(host, f.detail)
	case strings.Contains(f.detail, "propagat"):
		return "the DNS record for the challenge was created but did not become visible in time: " + f.detail
	case problem == "connection":
		return fmt.Sprintf("%s could not connect to the server on port %s (%s): a firewall in front of it?", ca, port, f.challenge)
	case problem == "dns" && f.challenge == "dns-01":
		return fmt.Sprintf("%s did not find the DNS record for the challenge (dns-01): %s", ca, f.detail)
	case problem == "dns":
		return fmt.Sprintf("%s could not look up the hostname (%s): %s", ca, f.challenge, f.detail)
	case problem == "unauthorized" && f.challenge == "dns-01":
		return fmt.Sprintf("%s found the wrong TXT record for the challenge (dns-01): %s; another DNS provider, or an older record, may be answering for the zone", ca, f.detail)
	case problem == "unauthorized", problem == "incorrectResponse", problem == "tls":
		return fmt.Sprintf("%s was answered by something other than this server (%s): %s; a proxy in front of the server, such as Cloudflare's, needs %s on the agent", ca, f.challenge, f.detail, cloudflareTokenVariable)
	case problem == "rateLimited":
		return fmt.Sprintf("%s refuses for now, its rate limit is reached: %s; it lifts within the hour", ca, f.detail)
	case problem != "":
		return fmt.Sprintf("%s refused (%s): %s", ca, problem, f.detail)
	case strings.Contains(f.detail, "no solvers available"):
		return "the certificate authority offers no challenge the proxy can solve: " + f.detail
	}
	return fmt.Sprintf("%s did not issue it: %s", ca, f.detail)
}

// dnsRecordReason completes "the DNS record for the challenge could not be
// created: " — with what the token check found, when it found something,
// since that is the sentence a person acts on; otherwise with Cloudflare's
// answer as the proxy quoted it.
func (e *Engine) dnsRecordReason(host, detail string) string {
	if problems := e.cloudflareProblems(); len(problems) > 0 {
		return strings.Join(problems, "; ")
	}
	if strings.Contains(detail, "could not determine zone") {
		return fmt.Sprintf("the zone of %s is not one the Cloudflare token sees; it needs Zone → Zone → Read and Zone → DNS → Edit on that zone", host)
	}
	if said, ok := cloudflare.Said(detail); ok {
		return said + "; the token's IP filter and its Zone → DNS → Edit permission are what to check (shipwick doctor checks them)"
	}
	return detail
}

// authorityName is the certificate authority as people know it, from the
// issuer Caddy names in the log, or from how the agent is set up when it
// does not.
func (e *Engine) authorityName(issuer string) string {
	switch {
	case strings.Contains(issuer, "letsencrypt"):
		return "Let's Encrypt"
	case strings.Contains(issuer, "zerossl"):
		return "ZeroSSL"
	case issuer == "" && e.opts.Network.ACMEDirectory == "":
		return "Let's Encrypt"
	}
	return "the certificate authority"
}

// explainFailure adds the failure to a hostname's certificate status. While
// the proxy has no certificate for the hostname, the message says why.
func (e *Engine) explainFailure(c *api.HostnameCertificate, f *certFailure) {
	if f == nil {
		return
	}
	reason := e.failureReason(c.Hostname, *f)
	c.Failure = &api.CertificateFailure{At: f.at, Challenge: f.challenge, Reason: reason, Detail: f.detail}
	if c.Status == api.CertObtaining {
		c.Message = "the proxy has no certificate for it yet: " + reason
	}
}

// reportCertificateFailures records an event for every served hostname whose
// certificate failed since it was last told — once per reason, not once per
// attempt: Caddy retries for a month.
func (e *Engine) reportCertificateFailures(ctx context.Context, plans []routingPlan, now time.Time) {
	w := e.certStatus
	for _, p := range plans {
		for _, host := range p.hosts.list() {
			w.mu.Lock()
			st := w.states[host]
			var failure *certFailure
			var renewal bool
			if st != nil {
				failure, renewal = st.failure, !st.notAfter.IsZero()
			}
			w.mu.Unlock()
			if failure == nil {
				continue
			}
			reason := e.failureReason(host, *failure)
			w.mu.Lock()
			told := st.failureTold == reason
			st.failureTold = reason
			w.mu.Unlock()
			if told {
				continue
			}
			verb := "obtained"
			if renewal {
				verb = "renewed"
			}
			app := store.Application{ID: p.appID, Name: p.app}
			text := fmt.Sprintf("Certificate for %s could not be %s: %s", host, verb, reason)
			e.log.Warn(text, "app", app.Name, "proxy_said", failure.detail)
			if err := e.store.AddEvent(context.WithoutCancel(ctx), app.ID, nil, api.LevelWarn, api.EventApp, text, now); err != nil {
				e.log.Warn("could not record event", "app", app.Name, "error", err)
			}
		}
	}
}

// startFailureWatch follows the proxy's standard error until the engine shuts
// down. A runtime that cannot follow it — none in production — is left alone.
func (e *Engine) startFailureWatch() {
	if _, ok := e.rt.(proxyErrorFollower); !ok {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		since := time.Now()
		for e.baseCtx.Err() == nil {
			since = e.followProxyErrorsOnce(e.baseCtx, since)
			select {
			case <-e.baseCtx.Done():
			case <-time.After(trafficRetry):
			}
		}
	}()
}

// followProxyErrorsOnce reads the proxy's standard error from `since` until
// it ends, and returns where the next reading has to start: the time of the
// last line about a certificate. A proxy that cannot be found is the access
// log's follower's to say; this one is quiet about it.
func (e *Engine) followProxyErrorsOnce(ctx context.Context, since time.Time) time.Time {
	follower, ok := e.rt.(proxyErrorFollower)
	if !ok {
		return since
	}
	id, err := e.rt.ProxyContainer(ctx)
	if err != nil {
		return since
	}
	last := since
	_ = follower.FollowErrors(ctx, id, since, func(line []byte) {
		if _, f, ok := e.noteProxyError(line); ok && f.at.After(last) {
			last = f.at
		}
	})
	return last
}
