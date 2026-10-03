package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// fakeTraffic answers the traffic endpoints in front of a fakeAgent.
type fakeTraffic struct {
	mu      sync.Mutex
	traffic api.Traffic
	// requestPolls are returned one per GET …/requests; after the last one
	// the agent answers with fail.
	requestPolls [][]api.Request
	fail         *api.Error // with status 409
	queries      []string
}

func newTrafficAgent(t *testing.T) (*fakeAgent, *fakeTraffic) {
	t.Helper()
	f := newFakeAgent(t)
	ft := &fakeTraffic{}
	rest := f.srv.Config.Handler
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/applications/{name}/traffic", func(w http.ResponseWriter, r *http.Request) {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		ft.queries = append(ft.queries, r.URL.RequestURI())
		if ft.fail != nil {
			respondError(w, 409, *ft.fail)
			return
		}
		out := ft.traffic
		out.Application = r.PathValue("name")
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/requests", func(w http.ResponseWriter, r *http.Request) {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		ft.queries = append(ft.queries, r.URL.RequestURI())
		if len(ft.requestPolls) == 0 {
			if ft.fail != nil {
				respondError(w, 409, *ft.fail)
				return
			}
			respond(w, 200, []api.Request{})
			return
		}
		next := ft.requestPolls[0]
		ft.requestPolls = ft.requestPolls[1:]
		respond(w, 200, next)
	})
	mux.Handle("/", rest)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.srv = srv
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppHealthy, Domain: "api.example.com"}}
	return f, ft
}

func request(second int, method, path string, status int, ms float64) api.Request {
	return api.Request{Time: fixedNow.Add(time.Duration(second) * time.Second), Method: method, Path: path, Status: status, DurationMs: ms, Bytes: 2048, Client: "203.0.113.7"}
}

var busyHour = api.TrafficCounts{Requests: 200, Status2xx: 170, Status3xx: 4, Status4xx: 6, Status5xx: 20, Bytes: 40960, P50Ms: 12.4, P95Ms: 48, P99Ms: 1210}

func TestTrafficListsEveryApplication(t *testing.T) {
	f, ft := newTrafficAgent(t)
	ft.traffic = api.Traffic{StepSeconds: 60, Totals: busyHour}

	out, _, err := f.run(t.TempDir(), "traffic")
	if err != nil {
		t.Fatalf("traffic: %v", err)
	}
	assertInOrder(t, out, []string{
		"over the last hour",
		"APP", "REQ/MIN", "5XX", "P95", "BYTES",
		"my-api", "3.3", "20 (10%)", "48ms", "40 KB",
	})
	if len(ft.queries) != 1 || ft.queries[0] != "/api/v1/applications/my-api/traffic?since=1h" {
		t.Errorf("asked %v, want the last hour of the one application", ft.queries)
	}

	// An application nobody asked anything of is a row of zeros, not an error.
	ft.traffic = api.Traffic{StepSeconds: 300}
	out, _, err = f.run(t.TempDir(), "traffic", "--since", "24h")
	if err != nil {
		t.Fatalf("traffic --since 24h: %v", err)
	}
	assertInOrder(t, out, []string{"over the last 24 hours", "my-api", "0", "0", "-", "0 B"})
}

func TestTrafficOfOneApplication(t *testing.T) {
	f, ft := newTrafficAgent(t)
	ft.traffic = api.Traffic{StepSeconds: 60, Totals: busyHour}
	ft.requestPolls = [][]api.Request{{
		request(1, "GET", "/", 200, 3),
		request(2, "GET", "/reports", 200, 840),
		request(3, "POST", "/checkout", 502, 30),
		request(4, "GET", "/reports", 200, 1900),
		request(5, "POST", "/checkout", 503, 5020),
		request(6, "GET", "/", 200, 4),
	}}

	out, _, err := f.run(t.TempDir(), "traffic", "my-api")
	if err != nil {
		t.Fatalf("traffic my-api: %v", err)
	}
	assertInOrder(t, out, []string{
		"my-api", "over the last hour",
		"Requests", "200", "(3.3/min)",
		"Status", "170 2xx · 4 3xx · 6 4xx · 20 5xx (10%)",
		"Latency", "p50 12ms · p95 48ms · p99 1.2s",
		"Sent", "40 KB",
		"Slowest paths among the last 6 requests",
		"/checkout", "2", "5.0s",
		"/reports", "2", "1.9s",
		"/", "2", "4.0ms",
		"Failing paths among the last 6 requests",
		"PATH", "5XX", "LAST STATUS",
		"/checkout", "2", "503",
	})
	if strings.Count(out, "/reports") != 1 {
		t.Errorf("a path that never failed is listed among the failing ones:\n%s", out)
	}
	if want := []string{"/api/v1/applications/my-api/traffic?since=1h", "/api/v1/applications/my-api/requests?tail=200"}; strings.Join(ft.queries, " ") != strings.Join(want, " ") {
		t.Errorf("asked %v, want %v", ft.queries, want)
	}
}

func TestTrafficOfAnApplicationWithoutRequests(t *testing.T) {
	f, ft := newTrafficAgent(t)
	ft.traffic = api.Traffic{StepSeconds: 3600}
	out, _, err := f.run(t.TempDir(), "traffic", "my-api", "--since", "7d")
	if err != nil {
		t.Fatalf("traffic: %v", err)
	}
	if !strings.Contains(out, "No requests over the last 7 days.") || strings.Contains(out, "Latency") {
		t.Errorf("output = %q, want one sentence and no table of zeros", out)
	}
}

func TestTrafficRequests(t *testing.T) {
	f, ft := newTrafficAgent(t)
	ft.requestPolls = [][]api.Request{{request(1, "GET", "/", 200, 3.2), request(2, "POST", "/checkout", 502, 1210)}}

	out, _, err := f.run(t.TempDir(), "traffic", "my-api", "--requests", "-n", "20")
	if err != nil {
		t.Fatalf("traffic --requests: %v", err)
	}
	assertInOrder(t, out, []string{
		"200", "GET /", "3.2ms", "2 KB", "203.0.113.7",
		"502", "POST /checkout", "1.2s", "2 KB", "203.0.113.7",
	})
	if len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Errorf("want one line per request:\n%s", out)
	}
	if ft.queries[0] != "/api/v1/applications/my-api/requests?tail=20" {
		t.Errorf("asked %v, want the tail given", ft.queries)
	}

	out, _, err = f.run(t.TempDir(), "traffic", "my-api", "--requests")
	if err != nil || !strings.Contains(out, "No requests yet") {
		t.Errorf("without requests: %q, %v", out, err)
	}
}

func TestTrafficRequestsFollowedPrintsEachRequestOnce(t *testing.T) {
	f, ft := newTrafficAgent(t)
	first := []api.Request{request(1, "GET", "/a", 200, 1), request(2, "GET", "/b", 200, 1)}
	ft.requestPolls = [][]api.Request{
		first,
		first, // nothing new
		{first[1], request(3, "GET", "/c", 404, 1), request(4, "GET", "/d", 200, 1)},
	}
	// The agent going away is what ends the follow in this test.
	ft.fail = &api.Error{Code: api.CodeTrafficUnavailable, Message: "the proxy's access log cannot be read"}

	out, _, err := f.run(t.TempDir(), "traffic", "my-api", "--requests", "-f")
	if err == nil {
		t.Fatal("the follow should end with the agent's error")
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		paths = append(paths, fields[3])
	}
	if strings.Join(paths, " ") != "/a /b /c /d" {
		t.Errorf("printed %v, want every request once, in order:\n%s", paths, out)
	}
	if ft.queries[1] != "/api/v1/applications/my-api/requests?tail=200" {
		t.Errorf("follow asked %s, want everything the agent keeps", ft.queries[1])
	}
}

func TestTrafficRefusesFlagsThatDoNotGoTogether(t *testing.T) {
	f, ft := newTrafficAgent(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"traffic", "--since", "2h"}, "--since must be 1h, 24h or 7d"},
		{[]string{"traffic", "my-api", "-f"}, "--follow goes with --requests"},
		{[]string{"traffic", "--requests"}, "--requests needs an application"},
		{[]string{"traffic", "my-api", "--requests", "-n", "500"}, "--tail must be between 1 and 200"},
		{[]string{"traffic", "My_Api"}, "name"},
	} {
		_, _, err := f.run(t.TempDir(), tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if len(ft.queries) != 0 {
		t.Errorf("the agent was asked %v for commands that are refused", ft.queries)
	}
}

func TestTrafficSaysWhyThereIsNone(t *testing.T) {
	f, ft := newTrafficAgent(t)
	ft.fail = &api.Error{Code: api.CodeTrafficUnavailable, Message: "the proxy's access log cannot be read"}
	_, _, err := f.run(t.TempDir(), "traffic", "my-api")
	if got := Render(err); !strings.Contains(got, "This server records no traffic") || !strings.Contains(got, "shipwick server status") {
		t.Errorf("rendered %q, want the reason and a next step", got)
	}

	// An agent from before traffic existed.
	ft.fail = &api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: GET /api/v1/applications/my-api/traffic"}
	_, _, err = f.run(t.TempDir(), "traffic")
	if got := Render(err); !strings.Contains(got, "older than this shipwick") {
		t.Errorf("rendered %q, want the hint that the agent is older", got)
	}
}

func TestStatusNamesCertificatesThatAreNotInOrder(t *testing.T) {
	f := newFakeAgent(t)
	notAfter := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	later := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	f.app = api.ApplicationDetail{
		Application: api.Application{Name: "my-api", Status: api.AppHealthy, Domain: "api.example.com", Replicas: api.ReplicaCount{Desired: 1, Running: 1, Healthy: 1}},
		Certificates: []api.HostnameCertificate{
			{Hostname: "api.example.com", Status: api.CertOK, Issuer: "Let's Encrypt E7", NotAfter: &later},
			{Hostname: "old.example.com", Status: api.CertExpiring, Issuer: "Let's Encrypt E7", NotAfter: &notAfter, Message: "expires in 9 days, on 2026-03-10"},
			{Hostname: "new.example.com", Status: api.CertObtaining, Message: "the proxy has no certificate for it yet; HTTPS connections to it fail until it does"},
			{Hostname: "www.example.com", Status: api.CertWaitingForDNS, Message: "does not resolve yet; add an A record: www.example.com → 203.0.113.10 (DNS only, not proxied)"},
			{Hostname: "alt.example.com", Status: api.CertUnknown, Message: "the proxy could not be asked: connection refused"},
		},
	}

	out, _, err := f.run(t.TempDir(), "status", "my-api")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	assertInOrder(t, out, []string{
		"Replicas", "1/1 healthy",
		"HOSTNAME", "CERTIFICATE",
		"old.example.com", "expires in 9 days, on 2026-03-10 (Let's Encrypt E7)",
		"new.example.com", "being obtained: the proxy has no certificate for it yet",
		"www.example.com", "waiting for DNS: does not resolve yet; add an A record",
		"alt.example.com", "unknown: the proxy could not be asked",
	})
	if strings.Contains(out, "valid until") {
		t.Errorf("a certificate in order is not worth a line unless asked for:\n%s", out)
	}

	out, _, err = f.run(t.TempDir(), "status", "my-api", "--verbose")
	if err != nil {
		t.Fatalf("status --verbose: %v", err)
	}
	assertInOrder(t, out, []string{"api.example.com", "valid until 2026-05-20 (Let's Encrypt E7)", "old.example.com"})

	// Every certificate in order, or an agent that reports none: no section.
	f.app.Certificates = f.app.Certificates[:1]
	if out, _, _ := f.run(t.TempDir(), "status", "my-api"); strings.Contains(out, "CERTIFICATE") {
		t.Errorf("nothing to report, but:\n%s", out)
	}
}
