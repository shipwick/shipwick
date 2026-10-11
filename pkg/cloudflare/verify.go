package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// A token that cannot do its job fails inside Caddy, weeks later, as
// "Authentication error (code 10000)" in a log nobody reads. The two ways a
// real token failed were an IP filter that named the server's IPv4 address
// while the server called from its IPv6 one, and a token with Zone → Zone →
// Read but another "DNS …" permission than Zone → DNS → Edit: it listed zones
// perfectly well and could not create a record. So the check does what the
// proxy is going to do: it asks Cloudflare whether it accepts the token from
// this server, lists the zones, and in each creates and at once removes a TXT
// record of its own — a write, the only sure test of DNS → Edit.

const (
	// DefaultAPI is Cloudflare's API; tests point a Verifier at a fake.
	DefaultAPI = "https://api.cloudflare.com/client/v4"
	// checkRecord is the name, under the zone, of the record a check creates
	// and removes. Its content names the server and the time, so that one
	// left behind by an agent that died between the two calls does not make
	// the next check fail as a duplicate.
	checkRecord = "_shipwick-check"
	checkTTL    = 60
	// zonesPerPage is Cloudflare's page size. One page is checked: a token
	// that sees more zones than that has more power than one server needs,
	// and the check says so instead of writing fifty-one records.
	zonesPerPage   = 50
	requestTimeout = 15 * time.Second
	// codeAuthentication is what Cloudflare answers, with HTTP 403, both to a
	// token it does not know and to a known token called from an address its
	// IP filter does not name. The two cannot be told apart from here.
	codeAuthentication = 10000
	permissions        = "Zone → Zone → Read and Zone → DNS → Edit"
	tokensPage         = "dash.cloudflare.com → My Profile → API Tokens"
)

// Verifier finds out what a Cloudflare API token can do. The zero value is
// not usable: Token is required.
type Verifier struct {
	Token string
	// API is Cloudflare's API, DefaultAPI when empty.
	API string
	// Server names the server in the check record's content.
	Server string
	// HTTP is the client requests go through; nil means a client of its own
	// with a timeout per request.
	HTTP *http.Client
	Now  func() time.Time
}

// Verify runs the check and reports what it found. Nothing of the token is
// in the result or in any error it carries: Cloudflare's answers are quoted
// by status and code, and a request's URL never holds the token.
func (v Verifier) Verify(ctx context.Context) api.CloudflareStatus {
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	status := api.CloudflareStatus{CheckedAt: now().UTC(), Zones: []api.CloudflareZone{}, Problems: []string{}}
	fail := func(format string, args ...any) api.CloudflareStatus {
		status.Problems = append(status.Problems, fmt.Sprintf(format, args...))
		return status
	}

	status.Address = v.trace(ctx)

	// The token itself: known, active, and accepted from this address.
	var verified struct {
		Status string `json:"status"`
	}
	resp, err := v.call(ctx, http.MethodGet, "/user/tokens/verify", nil, &verified)
	switch {
	case err != nil:
		return fail("Cloudflare's API could not be reached from this server: %v", err)
	case !resp.ok() && resp.has(codeAuthentication) && status.Address != "":
		return fail("Cloudflare refuses the token from this server's address %s: an IP filter on the token must name that address, or the token is not valid (%s)", status.Address, resp)
	case !resp.ok() && resp.has(codeAuthentication):
		return fail("Cloudflare refuses the token from this server: an IP filter on the token that does not name the server's address, or a token that is not valid (%s)", resp)
	case !resp.ok():
		return fail("the token is not valid (%s): create one at %s with %s", resp, tokensPage, permissions)
	case verified.Status != "active":
		return fail("the token is %s: enable it, or create a new one at %s", verified.Status, tokensPage)
	}

	// The zones it sees.
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	resp, err = v.call(ctx, http.MethodGet, "/zones?per_page="+fmt.Sprint(zonesPerPage), nil, &zones)
	switch {
	case err != nil:
		return fail("Cloudflare's API could not be reached from this server: %v", err)
	case !resp.ok():
		return fail("the token cannot list zones: it needs Zone → Zone → Read (%s)", resp)
	case len(zones) == 0:
		return fail("no zone is visible to the token: give it %s on the zones your hostnames are in", permissions)
	}
	if resp.ResultInfo.TotalPages > 1 {
		fail("the token sees more than %d zones, and only those were checked: limit it to the zones your hostnames are in", zonesPerPage)
	}

	// What it may do in each: a TXT record, created and removed.
	content := fmt.Sprintf("shipwick check from %s at %s", v.Server, status.CheckedAt.Format(time.RFC3339))
	for _, z := range zones {
		zone := api.CloudflareZone{Name: z.Name}
		record := map[string]any{"type": "TXT", "name": checkRecord + "." + z.Name, "content": content, "ttl": checkTTL}
		var created struct {
			ID string `json:"id"`
		}
		resp, err := v.call(ctx, http.MethodPost, "/zones/"+url.PathEscape(z.ID)+"/dns_records", record, &created)
		switch {
		case err != nil:
			fail("the DNS records of %s could not be checked: Cloudflare's API could not be reached: %v", z.Name, err)
		case !resp.ok() && (resp.Status == http.StatusForbidden || resp.has(codeAuthentication)):
			fail("the token cannot edit DNS records of %s: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)", z.Name)
		case !resp.ok():
			fail("a DNS record could not be created in %s (%s)", z.Name, resp)
		default:
			zone.DNSEdit = true
			resp, err := v.call(ctx, http.MethodDelete, "/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(created.ID), nil, nil)
			if err != nil || !resp.ok() {
				said := fmt.Sprintf("%v", err)
				if err == nil {
					said = resp.String()
				}
				fail("the check record %s.%s was created and could not be removed: delete that TXT record by hand (%s)", checkRecord, z.Name, said)
			}
		}
		status.Zones = append(status.Zones, zone)
	}
	status.OK = len(status.Problems) == 0
	return status
}

// response is Cloudflare's envelope, with the HTTP status it came with.
type response struct {
	Status  int
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (r *response) ok() bool { return r.Success && r.Status/100 == 2 }

func (r *response) has(code int) bool {
	for _, e := range r.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

// String quotes the answer the way the sentences need it: "Cloudflare
// answered HTTP 403, code 10000: Authentication error".
func (r *response) String() string {
	s := fmt.Sprintf("Cloudflare answered HTTP %d", r.Status)
	for i, e := range r.Errors {
		if i == 0 {
			s += fmt.Sprintf(", code %d: %s", e.Code, e.Message)
		} else {
			s += fmt.Sprintf("; code %d: %s", e.Code, e.Message)
		}
	}
	return s
}

// call makes one request with the token and decodes the envelope; a
// successful result is decoded into out when it is not nil. An error is a
// request that got no answer; an answer that says no is in the response.
func (v Verifier) call(ctx context.Context, method, path string, body any, out any) (*response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, v.api()+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := v.client().Do(req)
	if err != nil {
		return nil, scrub(err, v.Token)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading Cloudflare's answer: %w", err)
	}
	resp := &response{Status: res.StatusCode}
	// An answer that is not the envelope — a gateway's page — is reported
	// by its status alone.
	_ = json.Unmarshal(data, resp)
	if resp.ok() && out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return nil, fmt.Errorf("Cloudflare's answer to %s %s could not be read: %w", method, path, err)
		}
	}
	return resp, nil
}

// trace asks Cloudflare which address this request came from: the address
// an IP filter on the token has to name, which is not always the one the
// operator has in mind — a server with IPv6 calls from it. No token is sent.
// "" when it could not be learned; the check goes on without it.
func (v Verifier) trace(ctx context.Context) string {
	u, err := url.Parse(v.api())
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Scheme+"://"+u.Host+"/cdn-cgi/trace", nil)
	if err != nil {
		return ""
	}
	res, err := v.client().Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	for _, line := range strings.Split(string(data), "\n") {
		if ip, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(ip)
		}
	}
	return ""
}

func (v Verifier) api() string {
	if v.API == "" {
		return DefaultAPI
	}
	return strings.TrimRight(v.API, "/")
}

func (v Verifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

// scrub keeps the token out of an error, should a client ever quote a
// header. net/http does not; this is insurance for what is a credential.
func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "[token]"))
}

// ZoneOf names the zone, among those given, that hostname belongs to: the
// longest zone the hostname is or ends with. "" when none does. A wildcard's
// zone is the zone of the name under it.
func ZoneOf(hostname string, zones []string) string {
	host := strings.ToLower(strings.TrimPrefix(hostname, "*."))
	best := ""
	for _, z := range zones {
		z = strings.ToLower(z)
		if (host == z || strings.HasSuffix(host, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

// cloudflareError is how Cloudflare's DNS module in Caddy quotes an answer
// that refused it: `got error status: HTTP 403: [{Code:10000 Message:Authentication error}]`.
var cloudflareError = regexp.MustCompile(`HTTP (\d{3})(?::\s*\[\{Code:(\d+) Message:([^}\]]*)\}\])?`)

// Said reads Cloudflare's status and first error out of a message the
// proxy logged, in the words the check uses: "Cloudflare answered HTTP 403,
// code 10000: Authentication error". ok is false when the text holds no
// such answer.
func Said(text string) (said string, ok bool) {
	m := cloudflareError.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	said = "Cloudflare answered HTTP " + m[1]
	if m[2] != "" {
		said += fmt.Sprintf(", code %s: %s", m[2], strings.TrimSpace(m[3]))
	}
	return said, true
}
