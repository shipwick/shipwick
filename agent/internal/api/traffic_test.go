package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// quietProxy accepts every configuration: these tests are about what the API
// says, not about routing.
type quietProxy struct{}

func (quietProxy) Sync(context.Context, []proxy.Route) error { return nil }
func (quietProxy) SetCertificates([]proxy.Certificate)       {}
func (quietProxy) Status() proxy.Status                      { return proxy.Status{Enabled: true, Reachable: true} }

// newProxiedFixture is a fixture whose engine has a proxy, and a resolver
// under which every hostname points at the server.
func newProxiedFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:", store.Options{EncryptionKey: []byte("an-encryption-key-of-32-bytes!!!")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet,
		Proxy:      quietProxy{},
		LookupHost: func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil },
	})
	apiServer := New(engine, st, sha256.Sum256([]byte(testToken)), quiet)
	srv := httptest.NewServer(apiServer.Handler())
	t.Cleanup(func() {
		apiServer.Close()
		srv.Close()
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &fixture{t: t, srv: srv, engine: engine, rt: rt, api: apiServer, store: st}
}

const routedConfig = "name: web\nimage: nginx:1.27\nport: 80\ndomain: web.example.com\nredirects: [www.example.com]\n"

func TestTrafficEndpoint(t *testing.T) {
	ctx := context.Background()
	f := newProxiedFixture(t)
	f.do("POST", "/api/v1/applications/web/deploy", routedConfig)
	f.engine.Wait()
	app, err := f.store.GetApplication(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}

	// Minutes as the agent would have stored them: ten requests in each of
	// the last twenty minutes, all between 25 and 50 ms, one of them a 502.
	minute := time.Now().UTC().Truncate(time.Minute)
	latency := make([]int64, 15)
	latency[5] = 10
	var rows []store.TrafficSample
	for i := 1; i <= 20; i++ {
		rows = append(rows, store.TrafficSample{ApplicationID: app.ID, At: minute.Add(-time.Duration(i) * time.Minute),
			Requests: 10, Status2xx: 7, Status3xx: 1, Status4xx: 1, Status5xx: 1, Bytes: 2048, Latency: latency})
	}
	rows = append(rows, store.TrafficSample{ApplicationID: app.ID, At: minute.Add(-2 * 24 * time.Hour), Requests: 5, Status2xx: 5, Bytes: 1, Latency: latency})
	if err := f.store.AddTrafficSamples(ctx, rows); err != nil {
		t.Fatal(err)
	}

	status, body := f.do("GET", "/api/v1/applications/web/traffic", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	got := decode[api.Traffic](t, body)
	if got.Application != "web" || got.StepSeconds != 60 || len(got.Points) != 20 {
		t.Fatalf("traffic = %+v; the default window is an hour in one-minute buckets", got)
	}
	if since := time.Since(got.Since); since < 59*time.Minute || since > 62*time.Minute {
		t.Errorf("since = %s ago, want an hour", since)
	}
	want := api.TrafficCounts{Requests: 200, Status2xx: 140, Status3xx: 20, Status4xx: 20, Status5xx: 20, Bytes: 40960}
	totals := got.Totals
	if totals.P50Ms < 25 || totals.P50Ms > 50 || totals.P99Ms < totals.P95Ms || totals.P99Ms > 50 {
		t.Errorf("percentiles = %v / %v / %v ms, want within the 25–50 ms bucket and in order", totals.P50Ms, totals.P95Ms, totals.P99Ms)
	}
	totals.P50Ms, totals.P95Ms, totals.P99Ms = 0, 0, 0
	if totals != want {
		t.Errorf("totals = %+v, want %+v", totals, want)
	}
	if p := got.Points[0]; !p.T.Equal(minute.Add(-20*time.Minute)) || p.Requests != 10 || p.Status5xx != 1 || p.P95Ms == 0 {
		t.Errorf("first point = %+v, want the oldest minute", p)
	}

	// The exact field names are a contract with the dashboard.
	var raw struct {
		Data struct {
			Totals map[string]json.RawMessage   `json:"totals"`
			Points []map[string]json.RawMessage `json:"points"`
		} `json:"data"`
	}
	json.Unmarshal(body, &raw)
	for _, field := range []string{"requests", "status_2xx", "status_3xx", "status_4xx", "status_5xx", "bytes", "p50_ms", "p95_ms", "p99_ms"} {
		if _, ok := raw.Data.Totals[field]; !ok {
			t.Errorf("totals miss %q in %s", field, body)
		}
		if _, ok := raw.Data.Points[0][field]; !ok {
			t.Errorf("points miss %q in %s", field, body)
		}
	}
	if _, ok := raw.Data.Points[0]["t"]; !ok || !strings.Contains(string(body), `"step_seconds":60`) {
		t.Errorf("points carry t, and the step is given in seconds: %s", body)
	}

	// Wider windows are served in wider buckets, and stay small.
	_, body = f.do("GET", "/api/v1/applications/web/traffic?since=24h", "")
	if got := decode[api.Traffic](t, body); got.StepSeconds != 300 || len(got.Points) < 4 || len(got.Points) > 5 || got.Totals.Requests != 200 {
		t.Errorf("24h: step = %d, points = %d, requests = %d; twenty minutes make four or five 5-minute buckets", got.StepSeconds, len(got.Points), got.Totals.Requests)
	}
	_, body = f.do("GET", "/api/v1/applications/web/traffic?since=7d", "")
	if got := decode[api.Traffic](t, body); got.StepSeconds != 3600 || got.Totals.Requests != 205 {
		t.Errorf("7d: step = %d, requests = %d; the two-day-old minute is in the week", got.StepSeconds, got.Totals.Requests)
	}
}

func TestTrafficOfAnApplicationWithoutRequests(t *testing.T) {
	f := newProxiedFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig) // no domain
	f.engine.Wait()

	status, body := f.do("GET", "/api/v1/applications/my-api/traffic", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"points":[]`) ||
		!strings.Contains(string(body), `"totals":{"requests":0,"status_2xx":0,"status_3xx":0,"status_4xx":0,"status_5xx":0,"bytes":0,"p50_ms":0,"p95_ms":0,"p99_ms":0}`) {
		t.Errorf("status = %d, body = %s; want zero totals and an empty list of points", status, body)
	}
	status, body = f.do("GET", "/api/v1/applications/my-api/requests", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Errorf("status = %d, body = %s; want an empty list", status, body)
	}
}

func TestTrafficEndpointsValidateAndAuthenticate(t *testing.T) {
	f := newProxiedFixture(t)
	f.do("POST", "/api/v1/applications/web/deploy", routedConfig)
	f.engine.Wait()

	for _, bad := range []string{"2h", "1d", "60"} {
		status, body := f.do("GET", "/api/v1/applications/web/traffic?since="+bad, "")
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "1h, 24h or 7d") {
			t.Errorf("since=%q: %d %+v; it should list the accepted values", bad, status, e)
		}
	}
	for _, bad := range []string{"0", "201", "many"} {
		if status, _ := f.do("GET", "/api/v1/applications/web/requests?tail="+bad, ""); status != http.StatusBadRequest {
			t.Errorf("tail=%q: status = %d, want 400", bad, status)
		}
	}
	for _, path := range []string{"/traffic", "/requests"} {
		if status, _ := f.do("GET", "/api/v1/applications/ghost"+path, ""); status != http.StatusNotFound {
			t.Errorf("%s of an unknown application: status = %d", path, status)
		}
		if status, _ := f.doWithAuth("GET", "/api/v1/applications/web"+path, "", ""); status != http.StatusUnauthorized {
			t.Errorf("%s without a token: status = %d", path, status)
		}
		// Reading traffic takes no more than the read role.
		reader := "Bearer " + f.createToken("viewer-"+path[1:], api.RoleRead).Token
		if status, body := f.doWithAuth("GET", "/api/v1/applications/web"+path, "", reader); status != http.StatusOK {
			t.Errorf("%s with a read token: %d %s", path, status, body)
		}
	}
}

func TestTrafficIsUnavailableWithoutAProxy(t *testing.T) {
	f := newFixture(t) // no proxy
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	for _, path := range []string{"/traffic", "/requests"} {
		status, body := f.do("GET", "/api/v1/applications/my-api"+path, "")
		e := decodeError(t, body)
		if status != http.StatusConflict || e.Code != api.CodeTrafficUnavailable || !strings.Contains(e.Message, "access log cannot be read") {
			t.Errorf("%s: %d %+v; want 409 TRAFFIC_UNAVAILABLE and a sentence", path, status, e)
		}
	}
}

func TestApplicationDetailCarriesCertificates(t *testing.T) {
	f := newProxiedFixture(t)
	f.do("POST", "/api/v1/applications/web/deploy", routedConfig)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	_, body := f.do("GET", "/api/v1/applications/web", "")
	want := `"certificates":[{"hostname":"web.example.com","status":"unknown","issuer":"","not_after":null,"message":"not checked yet"},` +
		`{"hostname":"www.example.com","status":"unknown","issuer":"","not_after":null,"message":"not checked yet"}]`
	if !strings.Contains(string(body), want) {
		t.Errorf("detail = %s\nwant it to carry %s", body, want)
	}
	_, body = f.do("GET", "/api/v1/applications/my-api", "")
	if !strings.Contains(string(body), `"certificates":[]`) {
		t.Errorf("an application without a hostname has an empty list: %s", body)
	}
}

func TestRequestShape(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 47, 37, 24695000, time.UTC)
	got, _ := json.Marshal(api.Request{Time: at, Method: "GET", Path: "/api/users", Status: 200, DurationMs: 12.5, Bytes: 2048, Client: "203.0.113.7"})
	want := `{"time":"2026-10-03T12:47:37.024695Z","method":"GET","path":"/api/users","status":200,"duration_ms":12.5,"bytes":2048,"client":"203.0.113.7"}`
	if string(got) != want {
		t.Errorf("request = %s\nwant      %s", got, want)
	}
}
