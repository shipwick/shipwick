package commands

import (
	"context"
	"strings"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cloudflare"
)

// cloudflareChecks closes doctor's screen with the two things a server behind
// Cloudflare could not see about itself: why a certificate did not come, in
// the proxy's words, and whether the agent's Cloudflare token can do its job.
// An agent before 0.9 reports neither, and the lines stay away.
func (c *cli) cloudflareChecks(ctx context.Context, r *report, cl *client.Client, info api.Server, apps []api.Application) {
	hostnames := c.certificateFailures(ctx, r, cl, apps)
	c.cloudflareToken(ctx, r, cl, info, hostnames)
}

// certificateFailures asks every application with a domain about its
// certificates and reports the ones the proxy could not obtain or renew, with
// the reason it logged. It returns the hostnames served, for the token check.
func (c *cli) certificateFailures(ctx context.Context, r *report, cl *client.Client, apps []api.Application) []string {
	var hostnames []string
	for _, app := range apps {
		if app.Domain == "" {
			continue
		}
		hostnames = append(append(append(hostnames, app.Domain), app.Aliases...), app.Redirects...)
		detail, err := cl.Application(ctx, app.Name)
		if err != nil {
			r.hint("%s could not be asked about its certificates: %s", app.Name, cause(err))
			continue
		}
		for _, cert := range detail.Certificates {
			if cert.Failure == nil {
				continue
			}
			if cert.NotAfter == nil {
				r.problem("%s has no certificate: %s", cert.Hostname, cert.Failure.Reason)
			} else {
				r.problem("The certificate for %s is not being renewed: %s", cert.Hostname, cert.Failure.Reason)
			}
		}
	}
	return hostnames
}

// cloudflareToken reports what the agent found out about its Cloudflare
// token. With an admin token the agent is asked to check again now; a check
// less than a minute old is the one GET /server already carries. A served
// hostname in a zone the token cannot edit is a problem — its certificate is
// not coming; anything else wrong with the token is a problem once hostnames
// are served, and a hint until then.
func (c *cli) cloudflareToken(ctx context.Context, r *report, cl *client.Client, info api.Server, hostnames []string) {
	status := info.Cloudflare
	if status == nil {
		return
	}
	when := "checked " + agoWords(c.now().Sub(status.CheckedAt))
	if info.Token.Role == api.RoleAdmin {
		switch fresh, err := cl.VerifyCloudflare(ctx); {
		case err == nil:
			status, when = &fresh, "checked just now"
		case client.IsCode(err, api.CodeCloudflareCheckedRecently), client.IsCode(err, api.CodeEndpointNotFound):
		default:
			r.hint("Cloudflare: the token could not be checked again (%s); what follows was %s", cause(err), when)
		}
	}

	zones := make([]string, 0, len(status.Zones))
	for _, z := range status.Zones {
		zones = append(zones, z.Name)
	}
	if status.OK {
		r.ok("Cloudflare: the token can edit the DNS records of %s (%s)", strings.Join(zones, ", "), when)
	}
	for _, p := range status.Problems {
		if cloudflareProblemMatters(p, *status, hostnames) {
			r.problem("Cloudflare: %s (%s)", p, when)
		} else {
			r.hint("Cloudflare: %s (%s)", p, when)
		}
	}

	// A hostname in a zone the token does not see gets no certificate while
	// the token is set, whatever else is in order. Said only when the zones
	// could be listed; and not for a hostname whose certificate the
	// operator supplied, which asks nothing of Cloudflare.
	if len(zones) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, h := range hostnames {
		if seen[h] || r.suppliedFor(h) || cloudflare.ZoneOf(h, zones) != "" {
			continue
		}
		seen[h] = true
		r.problem("Cloudflare: %s is not in a zone the token sees (%s), so its certificate cannot be obtained through DNS. Give the token Zone → Zone → Read and Zone → DNS → Edit on its zone", h, strings.Join(zones, ", "))
	}
}

// cloudflareProblemMatters says whether a problem of the token's is one of
// the server's: a zone the token cannot edit matters when a hostname is
// served from it, and anything wrong with the token itself when anything is
// served at all.
func cloudflareProblemMatters(problem string, status api.CloudflareStatus, hostnames []string) bool {
	for _, z := range status.Zones {
		if z.DNSEdit || !strings.Contains(problem, z.Name) {
			continue
		}
		for _, h := range hostnames {
			if cloudflare.ZoneOf(h, []string{z.Name}) != "" {
				return true
			}
		}
		return false
	}
	return len(hostnames) > 0
}

// agoWords is how long ago something happened, in words.
func agoWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day") + " ago"
}
