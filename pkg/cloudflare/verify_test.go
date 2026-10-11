package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

const testToken = "0123456789abcdefghijklmnopqrstuvwxyz_-AB"

// fakeCloudflare answers the five requests a check makes the way Cloudflare
// does — the shapes are those of Cloudflare's API documentation — with the
// faults a test turns on.
type fakeCloudflare struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []string // "METHOD /path"
	records  []map[string]any
	deleted  []string

	address     string          // what /cdn-cgi/trace says; "" answers 404
	tokenStatus string          // "active" unless set
	refuseToken int             // 10000: refused with HTTP 403; another code: 401
	zones       []string        // names; IDs are zone-<i>
	zonesStatus int             // non-zero: GET /zones fails with it
	totalPages  int             // result_info.total_pages; 1 unless set
	noEdit      map[string]bool // zones whose POST is 403 code 10000
	createFails map[string]int  // zones whose POST fails with another status
	deleteFails bool            // DELETE answers 500
	down        bool            // the server closes every connection
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	f := &fakeCloudflare{t: t, address: "2a01:4f8:1c1b:5e07::1", tokenStatus: "active", zones: []string{"example.com", "shipwick.com"}, totalPages: 1,
		noEdit: map[string]bool{}, createFails: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloudflare) verifier() Verifier {
	return Verifier{Token: testToken, API: f.srv.URL + "/client/v4", Server: "srv1", Now: func() time.Time { return time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC) }}
}

func (f *fakeCloudflare) envelope(w http.ResponseWriter, status int, result any, errs ...[2]any) {
	type apiErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	body := map[string]any{"success": status/100 == 2, "errors": []apiErr{}, "messages": []any{}, "result": result}
	for _, e := range errs {
		body["errors"] = append(body["errors"].([]apiErr), apiErr{Code: e[0].(int), Message: e[1].(string)})
	}
	if status/100 == 2 && result != nil {
		body["result_info"] = map[string]any{"page": 1, "per_page": 50, "total_pages": f.totalPages}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func (f *fakeCloudflare) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	down := f.down
	f.mu.Unlock()
	if down {
		panic(http.ErrAbortHandler)
	}
	if r.URL.Path == "/cdn-cgi/trace" {
		if r.Header.Get("Authorization") != "" {
			f.t.Error("the trace request carried the token")
		}
		if f.address == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "fl=982f121\nh=api.cloudflare.com\nip=%s\nts=1791735341.000\nvisit_scheme=https\n", f.address)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.envelope(w, 400, nil, [2]any{6003, "Invalid request headers"})
		return
	}
	if f.refuseToken != 0 {
		status := 401
		if f.refuseToken == 10000 {
			status = 403
		}
		f.envelope(w, status, nil, [2]any{f.refuseToken, map[int]string{10000: "Authentication error", 1000: "Invalid API Token"}[f.refuseToken]})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	switch {
	case r.Method == "GET" && path == "/user/tokens/verify":
		f.envelope(w, 200, map[string]any{"id": "ed17574386854bf78a67040be0a770b0", "status": f.tokenStatus, "not_before": "2026-10-01T00:00:00Z", "expires_on": "2027-10-01T00:00:00Z"})
	case r.Method == "GET" && path == "/zones":
		if f.zonesStatus != 0 {
			f.envelope(w, f.zonesStatus, nil, [2]any{10000, "Authentication error"})
			return
		}
		if r.URL.Query().Get("per_page") != "50" {
			f.t.Errorf("GET /zones?%s: want per_page=50", r.URL.RawQuery)
		}
		zones := []map[string]any{}
		for i, name := range f.zones {
			zones = append(zones, map[string]any{"id": fmt.Sprintf("zone-%d", i), "name": name, "status": "active"})
		}
		f.envelope(w, 200, zones)
	case r.Method == "POST" && strings.HasSuffix(path, "/dns_records"):
		zone := f.zoneOf(path)
		var record map[string]any
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &record); err != nil {
			f.t.Errorf("POST dns_records: not JSON: %s", body)
		}
		f.mu.Lock()
		f.records = append(f.records, record)
		f.mu.Unlock()
		switch {
		case f.noEdit[zone]:
			f.envelope(w, 403, nil, [2]any{10000, "Authentication error"})
		case f.createFails[zone] != 0:
			f.envelope(w, f.createFails[zone], nil, [2]any{81057, "Record already exists."})
		default:
			record["id"] = "rec-" + zone
			f.envelope(w, 200, record)
		}
	case r.Method == "DELETE" && strings.Contains(path, "/dns_records/"):
		id := path[strings.LastIndex(path, "/")+1:]
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		if f.deleteFails {
			f.envelope(w, 500, nil, [2]any{10001, "Internal error"})
			return
		}
		f.envelope(w, 200, map[string]any{"id": id})
	default:
		f.envelope(w, 404, nil, [2]any{7000, "No route for that URI"})
	}
}

func (f *fakeCloudflare) zoneOf(path string) string {
	// /zones/zone-1/dns_records
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "zones" && i+1 < len(parts) {
			var n int
			fmt.Sscanf(parts[i+1], "zone-%d", &n)
			return f.zones[n]
		}
	}
	return ""
}

func (f *fakeCloudflare) saw() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func problems(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("problems = %q, want %d: %q", got, len(want), want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("problem %d = %q, want it to contain %q", i, got[i], want[i])
		}
	}
}

func TestATokenThatCanDoItsJobChecksOutByWritingAndRemovingARecordInEveryZone(t *testing.T) {
	f := newFakeCloudflare(t)
	got := f.verifier().Verify(context.Background())

	if !got.OK || len(got.Problems) != 0 {
		t.Fatalf("ok = %v, problems = %q", got.OK, got.Problems)
	}
	if got.Address != "2a01:4f8:1c1b:5e07::1" {
		t.Errorf("address = %q, want the one Cloudflare saw the check come from", got.Address)
	}
	if got.CheckedAt != time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC) {
		t.Errorf("checked_at = %s", got.CheckedAt)
	}
	if len(got.Zones) != 2 || got.Zones[0] != (api.CloudflareZone{Name: "example.com", DNSEdit: true}) || got.Zones[1] != (api.CloudflareZone{Name: "shipwick.com", DNSEdit: true}) {
		t.Errorf("zones = %+v, want both with dns_edit", got.Zones)
	}
	want := []string{"GET /cdn-cgi/trace", "GET /client/v4/user/tokens/verify", "GET /client/v4/zones",
		"POST /client/v4/zones/zone-0/dns_records", "DELETE /client/v4/zones/zone-0/dns_records/rec-example.com",
		"POST /client/v4/zones/zone-1/dns_records", "DELETE /client/v4/zones/zone-1/dns_records/rec-shipwick.com"}
	if saw := f.saw(); strings.Join(saw, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(saw, "\n"), strings.Join(want, "\n"))
	}
	rec := f.records[1]
	if rec["type"] != "TXT" || rec["name"] != "_shipwick-check.shipwick.com" || rec["ttl"] != float64(60) ||
		rec["content"] != "shipwick check from srv1 at 2026-10-11T09:00:00Z" {
		t.Errorf("record = %v, want a TXT _shipwick-check.<zone> with a TTL of 60 whose content names the server and the time", rec)
	}
}

func TestATokenRefusedFromThisAddressNamesTheAddressAndTheIPFilter(t *testing.T) {
	f := newFakeCloudflare(t)
	f.refuseToken = 10000
	got := f.verifier().Verify(context.Background())
	if got.OK {
		t.Fatal("ok for a refused token")
	}
	problems(t, got.Problems, "Cloudflare refuses the token from this server's address 2a01:4f8:1c1b:5e07::1: an IP filter on the token must name that address, or the token is not valid (Cloudflare answered HTTP 403, code 10000: Authentication error)")
	if n := len(f.saw()); n != 2 {
		t.Errorf("%d requests after the token was refused, want the trace and the verify only", n)
	}

	// Without the trace, the sentence still says what to look at.
	f.address = ""
	got = f.verifier().Verify(context.Background())
	problems(t, got.Problems, "Cloudflare refuses the token from this server: an IP filter on the token that does not name the server's address, or a token that is not valid")
	if got.Address != "" {
		t.Errorf("address = %q with no trace", got.Address)
	}
}

func TestATokenCloudflareDoesNotKnowIsNotValid(t *testing.T) {
	f := newFakeCloudflare(t)
	f.refuseToken = 1000
	got := f.verifier().Verify(context.Background())
	problems(t, got.Problems, "the token is not valid (Cloudflare answered HTTP 401, code 1000: Invalid API Token): create one at dash.cloudflare.com → My Profile → API Tokens with Zone → Zone → Read and Zone → DNS → Edit")

	f.refuseToken, f.tokenStatus = 0, "disabled"
	got = f.verifier().Verify(context.Background())
	problems(t, got.Problems, "the token is disabled: enable it, or create a new one")
	if len(got.Zones) != 0 {
		t.Errorf("zones = %+v listed with a disabled token", got.Zones)
	}
}

func TestATokenWithoutZoneReadOrWithoutZonesIsSaidSo(t *testing.T) {
	f := newFakeCloudflare(t)
	f.zonesStatus = 403
	got := f.verifier().Verify(context.Background())
	problems(t, got.Problems, "the token cannot list zones: it needs Zone → Zone → Read (Cloudflare answered HTTP 403, code 10000: Authentication error)")

	f.zonesStatus, f.zones = 0, nil
	got = f.verifier().Verify(context.Background())
	problems(t, got.Problems, "no zone is visible to the token: give it Zone → Zone → Read and Zone → DNS → Edit on the zones your hostnames are in")
}

func TestATokenWithoutDNSEditListsTheZoneAndCannotWriteInIt(t *testing.T) {
	f := newFakeCloudflare(t)
	f.noEdit["shipwick.com"] = true
	got := f.verifier().Verify(context.Background())
	if got.OK {
		t.Fatal("ok although one zone cannot be edited")
	}
	if len(got.Zones) != 2 || !got.Zones[0].DNSEdit || got.Zones[1].DNSEdit {
		t.Errorf("zones = %+v, want example.com editable and shipwick.com not", got.Zones)
	}
	problems(t, got.Problems, "the token cannot edit DNS records of shipwick.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)")
	for _, r := range f.saw() {
		if strings.HasPrefix(r, "DELETE") && strings.Contains(r, "zone-1") {
			t.Error("a record that was not created was deleted")
		}
	}
}

func TestOtherFailuresQuoteCloudflare(t *testing.T) {
	f := newFakeCloudflare(t)
	f.createFails["example.com"] = 400
	f.deleteFails = true
	got := f.verifier().Verify(context.Background())
	problems(t, got.Problems,
		"a DNS record could not be created in example.com (Cloudflare answered HTTP 400, code 81057: Record already exists.)",
		"the check record _shipwick-check.shipwick.com was created and could not be removed: delete that TXT record by hand (Cloudflare answered HTTP 500, code 10001: Internal error)")
	if !got.Zones[1].DNSEdit {
		t.Error("a record was created in shipwick.com: the token can edit it, whatever the removal did")
	}
}

func TestATokenWithTooManyZonesIsToldSo(t *testing.T) {
	f := newFakeCloudflare(t)
	f.totalPages = 3
	got := f.verifier().Verify(context.Background())
	problems(t, got.Problems, "the token sees more than 50 zones, and only those were checked: limit it to the zones your hostnames are in")
	if len(got.Zones) != 2 {
		t.Errorf("zones = %+v, want the page that was checked", got.Zones)
	}
}

func TestCloudflareOutOfReachIsOneSentenceWithoutTheToken(t *testing.T) {
	f := newFakeCloudflare(t)
	f.down = true
	got := f.verifier().Verify(context.Background())
	if got.OK || len(got.Problems) != 1 || !strings.HasPrefix(got.Problems[0], "Cloudflare's API could not be reached from this server: ") {
		t.Fatalf("problems = %q", got.Problems)
	}
	if got.Address != "" {
		t.Errorf("address = %q while Cloudflare was out of reach", got.Address)
	}
}

func TestNothingOfTheTokenIsInAnyResult(t *testing.T) {
	for name, set := range map[string]func(*fakeCloudflare){
		"ok":      func(*fakeCloudflare) {},
		"refused": func(f *fakeCloudflare) { f.refuseToken = 10000 },
		"no edit": func(f *fakeCloudflare) { f.noEdit["example.com"] = true },
		"down":    func(f *fakeCloudflare) { f.down = true },
	} {
		f := newFakeCloudflare(t)
		set(f)
		got := f.verifier().Verify(context.Background())
		text, _ := json.Marshal(got)
		for _, part := range []string{testToken, testToken[:12], "Bearer"} {
			if strings.Contains(string(text), part) {
				t.Errorf("%s: the result carries %q: %s", name, part, text)
			}
		}
	}
}

func TestZoneOfPicksTheLongestZoneAHostnameIsUnder(t *testing.T) {
	zones := []string{"example.com", "apps.example.com", "shipwick.com"}
	for host, want := range map[string]string{
		"api.example.com":        "example.com",
		"a.apps.example.com":     "apps.example.com",
		"example.com":            "example.com",
		"*.example.com":          "example.com",
		"Shipwick.com":           "shipwick.com",
		"notexample.com":         "",
		"example.org":            "",
		"api.apps.example.co.uk": "",
	} {
		if got := ZoneOf(host, zones); got != want {
			t.Errorf("ZoneOf(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestSaidReadsCloudflaresAnswerOutOfTheProxysWords(t *testing.T) {
	text := `[scrumpokerplay.com] solving challenges: presenting for challenge: adding temporary record for zone "scrumpokerplay.com.": got error status: HTTP 403: [{Code:10000 Message:Authentication error}] (order=https://acme-v02.api.letsencrypt.org/acme/order/1/2) (ca=https://acme-v02.api.letsencrypt.org/directory)`
	got, ok := Said(text)
	if !ok || got != "Cloudflare answered HTTP 403, code 10000: Authentication error" {
		t.Errorf("Said = %q, %v", got, ok)
	}
	if got, ok := Said("got error status: HTTP 502"); !ok || got != "Cloudflare answered HTTP 502" {
		t.Errorf("Said = %q, %v", got, ok)
	}
	if _, ok := Said("timed out waiting for record to fully propagate"); ok {
		t.Error("Said found an answer where there is none")
	}
}
