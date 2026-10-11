package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

// cloudflareAgent answers the two requests the Cloudflare checks make: the
// check on request, and an application's detail with its certificates.
type cloudflareAgent struct {
	verifyStatus int // 0: the check is not offered (ENDPOINT_NOT_FOUND)
	verifyResult api.CloudflareStatus
	verified     int
	certificates []api.HostnameCertificate
}

func (a *cloudflareAgent) serve(t *testing.T) *client.Client {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/server/cloudflare/verify", func(w http.ResponseWriter, r *http.Request) {
		a.verified++
		switch a.verifyStatus {
		case 0:
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint"})
		case 200:
			respond(w, 200, a.verifyResult)
		default:
			respondError(w, a.verifyStatus, api.Error{Code: api.CodeCloudflareCheckedRecently, Message: "checked recently", Details: map[string]any{"retry_after_seconds": 40}})
		}
	})
	mux.HandleFunc("GET /api/v1/applications/{name}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, api.ApplicationDetail{Application: api.Application{Name: r.PathValue("name")}, Certificates: a.certificates})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cl, err := client.New(srv.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// cloudflareReport runs the checks against the fake agent and returns what
// doctor printed.
func cloudflareReport(t *testing.T, agent *cloudflareAgent, info api.Server, apps []api.Application) (string, *report) {
	t.Helper()
	var out strings.Builder
	c, _ := newRoot(Options{Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	c.now = func() time.Time { return time.Date(2026, 10, 11, 9, 3, 0, 0, time.UTC) }
	r := &report{c: c}
	c.cloudflareChecks(context.Background(), r, agent.serve(t), info, apps)
	return out.String(), r
}

var checkedAt = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func TestDoctorSaysNothingAboutCloudflareWhenTheAgentHasNoTokenOrIsOlder(t *testing.T) {
	agent := &cloudflareAgent{}
	apps := []api.Application{{Name: "web", Domain: "web.example.com"}}
	out, r := cloudflareReport(t, agent, api.Server{}, apps)
	if out != "" || r.problems != 0 || r.hints != 0 {
		t.Errorf("printed %q for an agent that reports nothing", out)
	}
	if agent.verified != 0 {
		t.Error("an agent without a token was asked to check one")
	}
}

func TestDoctorReportsATokenThatCanDoItsJob(t *testing.T) {
	ok := api.CloudflareStatus{CheckedAt: checkedAt, OK: true, Address: "2a01:4f8::1",
		Zones: []api.CloudflareZone{{Name: "example.com", DNSEdit: true}, {Name: "shipwick.com", DNSEdit: true}}, Problems: []string{}}
	agent := &cloudflareAgent{}
	info := api.Server{Cloudflare: &ok, Token: api.TokenIdentity{Role: api.RoleRead}}
	apps := []api.Application{{Name: "web", Domain: "web.example.com", Aliases: []string{"app.shipwick.com"}}}
	out, r := cloudflareReport(t, agent, info, apps)
	assertInOrder(t, out, []string{"✓ Cloudflare: the token can edit the DNS records of example.com, shipwick.com (checked 3 minutes ago)\n"})
	if r.problems != 0 || r.hints != 0 || agent.verified != 0 {
		t.Errorf("problems = %d, hints = %d, checks asked = %d; a read token asks for no check", r.problems, r.hints, agent.verified)
	}
}

func TestDoctorAsksAnAdminTokensAgentToCheckAgainAndFallsBackToTheLastCheck(t *testing.T) {
	stale := api.CloudflareStatus{CheckedAt: checkedAt, OK: true, Zones: []api.CloudflareZone{{Name: "example.com", DNSEdit: true}}, Problems: []string{}}
	fresh := api.CloudflareStatus{CheckedAt: checkedAt.Add(3 * time.Minute), Zones: []api.CloudflareZone{{Name: "example.com"}},
		Problems: []string{"the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)"}}
	agent := &cloudflareAgent{verifyStatus: 200, verifyResult: fresh}
	info := api.Server{Cloudflare: &stale, Token: api.TokenIdentity{Role: api.RoleAdmin}}
	apps := []api.Application{{Name: "web", Domain: "web.example.com"}}

	out, r := cloudflareReport(t, agent, info, apps)
	assertInOrder(t, out, []string{"✗ Cloudflare: the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions) (checked just now)\n"})
	if r.problems != 1 || agent.verified != 1 {
		t.Errorf("problems = %d, checks asked = %d", r.problems, agent.verified)
	}

	// Checked less than a minute ago: the agent's last result, said so.
	agent.verifyStatus = 429
	out, r = cloudflareReport(t, agent, info, apps)
	assertInOrder(t, out, []string{"✓ Cloudflare: the token can edit the DNS records of example.com (checked 3 minutes ago)\n"})
	if r.problems != 0 || r.hints != 0 {
		t.Errorf("problems = %d, hints = %d", r.problems, r.hints)
	}

	// An agent that offers no check on request: the last result, quietly.
	agent.verifyStatus = 0
	out, _ = cloudflareReport(t, agent, info, apps)
	if !strings.Contains(out, "✓ Cloudflare: the token can edit the DNS records of example.com (checked 3 minutes ago)") || strings.Contains(out, "could not be checked") {
		t.Errorf("out = %q", out)
	}
}

func TestDoctorTellsATokenProblemThatStopsACertificateFromOneThatDoesNotYet(t *testing.T) {
	noEdit := api.CloudflareStatus{CheckedAt: checkedAt, Address: "2a01:4f8::1",
		Zones:    []api.CloudflareZone{{Name: "example.com", DNSEdit: true}, {Name: "shipwick.com"}},
		Problems: []string{"the token cannot edit DNS records of shipwick.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)"}}
	info := api.Server{Cloudflare: &noEdit, Token: api.TokenIdentity{Role: api.RoleRead}}

	// A hostname served from the zone: its certificate is not coming.
	out, r := cloudflareReport(t, &cloudflareAgent{}, info, []api.Application{{Name: "web", Domain: "web.example.com", Redirects: []string{"www.shipwick.com"}}})
	if !strings.HasPrefix(out, "✗ Cloudflare: the token cannot edit DNS records of shipwick.com") || r.problems != 1 {
		t.Errorf("out = %q, problems = %d", out, r.problems)
	}

	// Nothing served from it yet: worth a look.
	out, r = cloudflareReport(t, &cloudflareAgent{}, info, []api.Application{{Name: "web", Domain: "web.example.com"}})
	if !strings.HasPrefix(out, "! Cloudflare: the token cannot edit DNS records of shipwick.com") || r.problems != 0 || r.hints != 1 {
		t.Errorf("out = %q, problems = %d, hints = %d", out, r.problems, r.hints)
	}

	// A problem of the token itself: a problem as soon as anything is served.
	refused := api.CloudflareStatus{CheckedAt: checkedAt, Zones: []api.CloudflareZone{}, Problems: []string{"Cloudflare refuses the token from this server's address 2a01:4f8::1: an IP filter on the token must name that address, or the token is not valid (Cloudflare answered HTTP 403, code 10000: Authentication error)"}}
	info.Cloudflare = &refused
	out, r = cloudflareReport(t, &cloudflareAgent{}, info, []api.Application{{Name: "web", Domain: "web.example.com"}})
	if !strings.HasPrefix(out, "✗ Cloudflare: Cloudflare refuses the token from this server's address 2a01:4f8::1") || r.problems != 1 {
		t.Errorf("out = %q, problems = %d", out, r.problems)
	}
	out, r = cloudflareReport(t, &cloudflareAgent{}, info, nil)
	if !strings.HasPrefix(out, "! Cloudflare: Cloudflare refuses the token") || r.hints != 1 || r.problems != 0 {
		t.Errorf("out = %q, problems = %d, hints = %d", out, r.problems, r.hints)
	}
}

func TestDoctorNamesAHostnameOutsideTheTokensZones(t *testing.T) {
	ok := api.CloudflareStatus{CheckedAt: checkedAt, OK: true, Zones: []api.CloudflareZone{{Name: "example.com", DNSEdit: true}}, Problems: []string{}}
	info := api.Server{Cloudflare: &ok, Token: api.TokenIdentity{Role: api.RoleRead}}
	apps := []api.Application{{Name: "web", Domain: "web.example.com", Aliases: []string{"*.example.com"}}, {Name: "shop", Domain: "shop.example.org", Redirects: []string{"www.example.org"}}}
	out, r := cloudflareReport(t, &cloudflareAgent{}, info, apps)
	assertInOrder(t, out, []string{
		"✓ Cloudflare: the token can edit the DNS records of example.com (checked 3 minutes ago)",
		"✗ Cloudflare: shop.example.org is not in a zone the token sees (example.com), so its certificate cannot be obtained through DNS. Give the token Zone → Zone → Read and Zone → DNS → Edit on its zone",
		"✗ Cloudflare: www.example.org is not in a zone the token sees (example.com)",
	})
	if r.problems != 2 || strings.Contains(out, "*.example.com") {
		t.Errorf("problems = %d; a wildcard under the zone is in it:\n%s", r.problems, out)
	}
}

func TestDoctorReportsWhyACertificateDidNotCome(t *testing.T) {
	notAfter := checkedAt.Add(60 * 24 * time.Hour)
	agent := &cloudflareAgent{certificates: []api.HostnameCertificate{
		{Hostname: "web.example.com", Status: api.CertObtaining, Message: "the proxy has no certificate for it yet: Let's Encrypt could not connect to the server on port 443 (tls-alpn-01): a firewall in front of it?",
			Failure: &api.CertificateFailure{At: checkedAt, Challenge: "tls-alpn-01", Reason: "Let's Encrypt could not connect to the server on port 443 (tls-alpn-01): a firewall in front of it?", Detail: "203.0.113.10: Timeout during connect (likely firewall problem)"}},
		{Hostname: "www.example.com", Status: api.CertOK, Issuer: "Let's Encrypt E7", NotAfter: &notAfter,
			Failure: &api.CertificateFailure{At: checkedAt, Challenge: "dns-01", Reason: "the DNS record for the challenge could not be created: the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit"}},
		{Hostname: "old.example.com", Status: api.CertOK, Issuer: "Let's Encrypt E7", NotAfter: &notAfter},
	}}
	apps := []api.Application{{Name: "web", Domain: "web.example.com", Aliases: []string{"www.example.com", "old.example.com"}}, {Name: "worker"}}
	out, r := cloudflareReport(t, agent, api.Server{}, apps)
	assertInOrder(t, out, []string{
		"✗ web.example.com has no certificate: Let's Encrypt could not connect to the server on port 443 (tls-alpn-01): a firewall in front of it?\n",
		"✗ The certificate for www.example.com is not being renewed: the DNS record for the challenge could not be created: the token cannot edit DNS records of example.com: it needs Zone → DNS → Edit\n",
	})
	if r.problems != 2 || strings.Contains(out, "old.example.com") {
		t.Errorf("problems = %d:\n%s", r.problems, out)
	}
}

func TestRenderExplainsTheCloudflareCodes(t *testing.T) {
	notConfigured := &client.APIError{Status: 409, Code: api.CodeCloudflareNotConfigured, Message: "no token"}
	if got := Render(notConfigured); !strings.Contains(got, "SHIPWICK_CLOUDFLARE_API_TOKEN") || !strings.Contains(got, "docker compose up -d") {
		t.Errorf("Render = %q", got)
	}
	recent := &client.APIError{Status: 429, Code: api.CodeCloudflareCheckedRecently, Message: "recent"}
	if got := Render(recent); !strings.Contains(got, "less than a minute ago") || !strings.Contains(got, "shipwick doctor") {
		t.Errorf("Render = %q", got)
	}
	// The envelope of a check as the client decodes it.
	var status api.CloudflareStatus
	if err := json.Unmarshal([]byte(`{"checked_at":"2026-10-11T09:00:00Z","ok":false,"address":"2a01:4f8::1","zones":[{"name":"example.com","dns_edit":false}],"problems":["the token is not valid"]}`), &status); err != nil || status.Zones[0].DNSEdit || status.Problems[0] != "the token is not valid" {
		t.Errorf("decoded %+v, %v", status, err)
	}
}
