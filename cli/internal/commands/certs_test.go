package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// certAgent serves the certificate endpoints and records what it was sent.
type certAgent struct {
	*fakeAgent
	certificates []api.Certificate
	setBodies    map[string]api.SetCertificateRequest // hostname → the body received
	setError     *api.Error
	removed      []string
	removeError  *api.Error
	missing      bool // an agent from before certificates: every endpoint is unknown
}

func newCertAgent(t *testing.T) *certAgent {
	t.Helper()
	a := &certAgent{fakeAgent: &fakeAgent{t: t, actionBodies: map[string]string{}}, setBodies: map[string]api.SetCertificateRequest{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/certificates", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, a.certificates)
	})
	mux.HandleFunc("PUT /api/v1/certificates/{hostname}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req api.SetCertificateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("PUT body is not the documented JSON: %v", err)
		}
		if a.setError != nil {
			respondError(w, 400, *a.setError)
			return
		}
		a.setBodies[r.PathValue("hostname")] = req
		respond(w, 200, api.Certificate{
			Hostname: r.PathValue("hostname"), Subjects: []string{"example.com", "*.example.com"}, Issuer: "Corp Issuing CA",
			NotBefore: fixedNow.AddDate(0, -1, 0), NotAfter: fixedNow.AddDate(0, 11, 0), CreatedAt: fixedNow, UpdatedAt: fixedNow,
		})
	})
	mux.HandleFunc("DELETE /api/v1/certificates/{hostname}", func(w http.ResponseWriter, r *http.Request) {
		a.removed = append(a.removed, r.PathValue("hostname"))
		if a.removeError != nil {
			respondError(w, 404, *a.removeError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		if a.missing {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

const (
	chainPEM = "-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----\n"
	keyPEM   = "-----BEGIN PRIVATE KEY-----\nhunter2\n-----END PRIVATE KEY-----\n"
)

// pemDir is a directory with a chain and its key, as a certificate authority
// hands them over.
func pemDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), []byte(chainPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCertSetSendsBothFilesAndReportsWhatTheServerStored(t *testing.T) {
	a := newCertAgent(t)
	out, errOut, err := a.run(pemDir(t), "cert", "set", "Example.com", "--cert", "fullchain.pem", "--key", "privkey.pem")
	if err != nil {
		t.Fatalf("cert set: %v\n%s", err, out)
	}
	sent, ok := a.setBodies["example.com"]
	if !ok || sent.Certificate != chainPEM || sent.Key != keyPEM {
		t.Errorf("the agent received %+v under %v, want both files as they are under the lower-case hostname", sent, a.requests)
	}
	assertInOrder(t, out, []string{
		"Stored the certificate for example.com",
		"Issuer", "Corp Issuing CA",
		"Expires", fixedNow.AddDate(0, 11, 0).Format("2006-01-02"),
		"Covers", "example.com, *.example.com",
		"It is not renewed for you",
	})
	if strings.Contains(out+errOut, "hunter2") {
		t.Errorf("the key was echoed:\n%s%s", out, errOut)
	}
}

func TestCertSetStoresAWildcardUnderTheWildcard(t *testing.T) {
	a := newCertAgent(t)
	if _, _, err := a.run(pemDir(t), "cert", "set", "*.example.com", "--cert", "fullchain.pem", "--key", "privkey.pem"); err != nil {
		t.Fatalf("cert set: %v", err)
	}
	if _, ok := a.setBodies["*.example.com"]; !ok {
		t.Errorf("requests = %v, want the certificate under *.example.com", a.requests)
	}
}

func TestCertSetRefusesBadInputBeforeContactingTheAgent(t *testing.T) {
	a := newCertAgent(t)
	dir := pemDir(t)
	os.WriteFile(filepath.Join(dir, "empty.pem"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "big.pem"), []byte(strings.Repeat("x", api.MaxCertificatePEMBytes+1)), 0o600)

	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"cert", "set", "not a hostname", "--cert", "fullchain.pem", "--key", "privkey.pem"}, "not a valid hostname"},
		{[]string{"cert", "set", "a.*.example.com", "--cert", "fullchain.pem", "--key", "privkey.pem"}, "a wildcard is one leading label"},
		{[]string{"cert", "set", "example.com", "--cert", "fullchain.pem"}, "both files are needed"},
		{[]string{"cert", "set", "*.example.com", "--key", "privkey.pem"}, "shipwick cert set '*.example.com' --cert fullchain.pem --key privkey.pem"},
		{[]string{"cert", "set", "example.com", "--cert", "nope.pem", "--key", "privkey.pem"}, "--cert: "},
		{[]string{"cert", "set", "example.com", "--cert", "fullchain.pem", "--key", "nope.pem"}, "--key: "},
		{[]string{"cert", "set", "example.com", "--cert", "fullchain.pem", "--key", "empty.pem"}, "empty.pem is empty"},
		{[]string{"cert", "set", "example.com", "--cert", "big.pem", "--key", "privkey.pem"}, "larger than 64 KB"},
		{[]string{"cert", "set", "example.com", "--cert", ".", "--key", "privkey.pem"}, "is a directory"},
		{[]string{"cert", "set"}, "arg"},
	} {
		a.requests = nil
		_, _, err := a.run(dir, tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.args, err, tt.want)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", tt.args, a.requests)
		}
	}
}

func TestCertSetExplainsARefusal(t *testing.T) {
	a := newCertAgent(t)
	a.setError = &api.Error{Code: api.CodeInvalidCertificate, Message: "the certificate does not cover example.org: it is for example.com"}
	_, _, err := a.run(pemDir(t), "cert", "set", "example.org", "--cert", "fullchain.pem", "--key", "privkey.pem")
	got := Render(err)
	assertInOrder(t, got, []string{
		"The server refused the certificate: the certificate does not cover example.org: it is for example.com.",
		"--cert is the chain in PEM, the hostname's own certificate first",
	})
	if strings.Contains(got, "hunter2") {
		t.Errorf("the key was echoed: %s", got)
	}
}

func TestCertList(t *testing.T) {
	a := newCertAgent(t)
	a.certificates = []api.Certificate{
		{Hostname: "*.example.com", Subjects: []string{"*.example.com", "example.com"}, Issuer: "Corp Issuing CA", NotAfter: fixedNow.AddDate(0, 11, 0)},
		{Hostname: "old.example.org", Subjects: []string{"old.example.org"}, Issuer: "Corp Issuing CA", NotAfter: fixedNow.Add(-48 * time.Hour)},
		{Hostname: "soon.example.org", Subjects: []string{"soon.example.org"}, Issuer: "R11", NotAfter: fixedNow.Add(12*24*time.Hour + time.Hour)},
	}
	out, _, err := a.run(t.TempDir(), "cert", "ls")
	if err != nil {
		t.Fatalf("cert ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("unexpected table:\n%s", out)
	}
	assertInOrder(t, lines[0], []string{"HOSTNAME", "ISSUER", "EXPIRES", "COVERS"})
	assertInOrder(t, lines[1], []string{"*.example.com", "Corp Issuing CA", fixedNow.AddDate(0, 11, 0).Format("2006-01-02"), "*.example.com, example.com"})
	assertInOrder(t, lines[2], []string{"old.example.org", fixedNow.Add(-48*time.Hour).Format("2006-01-02") + " (expired)", "old.example.org"})
	assertInOrder(t, lines[3], []string{"soon.example.org", "R11", "(in 12 days)", "soon.example.org"})
	if strings.Contains(lines[1], "(") {
		t.Errorf("a certificate that is months from its end needs no countdown: %s", lines[1])
	}

	a.certificates = nil
	out, _, _ = a.run(t.TempDir(), "cert", "list")
	if !strings.Contains(out, "No certificates of your own") || !strings.Contains(out, "shipwick cert set") {
		t.Errorf("an empty list should say what to do next:\n%s", out)
	}
}

func TestCertRemoveAsksFirstOffATerminalOnlyWithYes(t *testing.T) {
	a := newCertAgent(t)
	_, _, err := a.run(t.TempDir(), "cert", "rm", "example.com")
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(a.removed) != 0 {
		t.Fatalf("without --yes off a terminal: err = %v, removed = %v", err, a.removed)
	}

	out, _, err := a.run(t.TempDir(), "cert", "rm", "*.Example.com", "--yes")
	if err != nil || !strings.Contains(out, "Removed the certificate for *.example.com") {
		t.Fatalf("cert rm --yes: %v\n%s", err, out)
	}
	if len(a.removed) != 1 || a.removed[0] != "*.example.com" {
		t.Errorf("removed = %v", a.removed)
	}

	a.removeError = &api.Error{Code: api.CodeNotFound, Message: "not found"}
	_, _, err = a.run(t.TempDir(), "cert", "remove", "gone.example.com", "-y")
	if err == nil || !strings.Contains(err.Error(), "there is no certificate stored under gone.example.com") || !strings.Contains(err.Error(), "shipwick cert ls") {
		t.Errorf("an unknown hostname: err = %v", err)
	}
}

func TestCertCommandsSayWhenTheAgentIsTooOld(t *testing.T) {
	a := newCertAgent(t)
	a.missing = true
	for _, args := range [][]string{
		{"cert", "ls"},
		{"cert", "set", "example.com", "--cert", "fullchain.pem", "--key", "privkey.pem"},
		{"cert", "rm", "example.com", "--yes"},
	} {
		_, _, err := a.run(pemDir(t), args...)
		if got := Render(err); !strings.Contains(got, "older than this shipwick") {
			t.Errorf("%v: %q, want it to say the agent is older", args, got)
		}
	}
}

// cloudflareLookup resolves the agent's hostname to the server and every
// other hostname to Cloudflare's proxy.
func cloudflareLookup(_ context.Context, host string) ([]string, error) {
	if host == "agent.example.com" {
		return []string{"203.0.113.10"}, nil
	}
	return []string{"104.21.5.6", "2606:4700:3030::6815:506"}, nil
}

func TestDoctorNamesCloudflaresProxyLikeTheAgentDoes(t *testing.T) {
	var out strings.Builder
	c, _ := newRoot(Options{Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	local := localOptions{lookupHost: cloudflareLookup}

	r := &report{c: c}
	addrs := c.serverAddresses(context.Background(), r, local, "https://agent.example.com")
	c.checkDNS(context.Background(), r, local, "api.example.com", addrs)
	c.checkDNS(context.Background(), r, local, "api.example.com", nil)
	const want = "✗ api.example.com resolves to Cloudflare's proxy (104.21.5.6, 2606:4700:3030::6815:506), not to the server: turn the proxy off for this record (DNS only), or set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent to keep it on"
	if got := strings.Count(out.String(), want); got != 2 || r.problems != 2 {
		t.Errorf("want the agent's sentence with and without a known server address (%d times, %d problems):\n%s", got, r.problems, out.String())
	}
}

func TestDoctorAcceptsCloudflaresProxyWhenTheAgentUsesTheDNSChallenge(t *testing.T) {
	var out strings.Builder
	c, _ := newRoot(Options{Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	local := localOptions{lookupHost: func(ctx context.Context, host string) ([]string, error) {
		if host == "missing.example.com" {
			return nil, notFound(host)
		}
		return cloudflareLookup(ctx, "any.example.com")
	}}

	r := &report{c: c, dnsChallenge: true}
	addrs := c.serverAddresses(context.Background(), r, local, "https://agent.example.com")
	if addrs != nil {
		t.Errorf("server addresses = %v; Cloudflare's addresses are not the server's", addrs)
	}
	c.checkDNS(context.Background(), r, local, "api.example.com", addrs)
	c.checkDNS(context.Background(), r, local, "missing.example.com", addrs)
	assertInOrder(t, out.String(), []string{
		"✓ agent.example.com → Cloudflare's proxy (104.21.5.6, 2606:4700:3030::6815:506)",
		"! The server's own address is not known behind Cloudflare's proxy: ports 80 and 443 and the records' targets are not checked",
		"✓ api.example.com → Cloudflare's proxy (104.21.5.6, 2606:4700:3030::6815:506)",
		"✗ missing.example.com does not resolve. Create an A record for it pointing at the server\n",
	})
	if r.problems != 1 || r.hints != 1 {
		t.Errorf("problems = %d, hints = %d", r.problems, r.hints)
	}
}

func TestDoctorReadsTheDNSChallengeFromTheAgentAndSkipsWildcards(t *testing.T) {
	fetched := 0
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"104.21.5.6"}, nil },
		func(r *http.Request) (*http.Response, error) {
			fetched++
			return answer(200), nil
		})
	f.server.Proxy.DNSChallenge = true
	f.app.Domain = "*.example.com"
	f.app.Aliases = []string{"example.com"}
	f.app.Redirects = nil

	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ *.example.com is a wildcard: it has no single record or address to check",
		"✓ example.com → Cloudflare's proxy (104.21.5.6)",
	})
	if fetched != 0 {
		t.Errorf("https://*.example.com/ is not an address, yet %d requests were made", fetched)
	}
}

func TestDoctorPointsOutSuppliedCertificatesNearTheirEnd(t *testing.T) {
	var out strings.Builder
	c, _ := newRoot(Options{Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	r := &report{c: c}
	r.suppliedCertificates([]api.Certificate{
		{Hostname: "fine.example.com", NotAfter: fixedNow.AddDate(0, 6, 0)},
		{Hostname: "*.example.com", NotAfter: fixedNow.Add(10 * 24 * time.Hour)},
		{Hostname: "old.example.com", NotAfter: fixedNow.Add(-time.Hour)},
	}, fixedNow)

	soon := fixedNow.Add(10 * 24 * time.Hour).Format("2006-01-02")
	assertInOrder(t, out.String(), []string{
		"! The certificate you supplied for *.example.com expires on " + soon + ". Replace it before then with: shipwick cert set '*.example.com' --cert <file> --key <file>",
		"✗ The certificate you supplied for old.example.com expired on " + fixedNow.Add(-time.Hour).Format("2006-01-02") + ", and browsers refuse it. Replace it with: shipwick cert set old.example.com",
	})
	if strings.Contains(out.String(), "fine.example.com") || r.problems != 1 || r.hints != 1 {
		t.Errorf("problems = %d, hints = %d; a certificate months from its end is not worth a line:\n%s", r.problems, r.hints, out.String())
	}
}
