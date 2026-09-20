package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// parsed mirrors just enough of Caddy's config to assert on it.
type parsed struct {
	Admin struct{ Listen string }
	Apps  struct {
		HTTP struct {
			Servers map[string]struct {
				Listen []string
				Routes []struct {
					ID       string `json:"@id"`
					Terminal bool
					Match    []struct{ Host []string }
					Handle   []struct {
						Handler       string
						StatusCode    int `json:"status_code"`
						Upstreams     []struct{ Dial string }
						FlushInterval *int `json:"flush_interval"`
					}
				}
			}
		}
	}
}

func build(t *testing.T, routes []Route) (parsed, string, []byte) {
	t.Helper()
	raw, fp, err := Build("unix//run/caddy/admin.sock", routes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var p parsed
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, raw)
	}
	return p, fp, raw
}

func TestBuild(t *testing.T) {
	p, fp, _ := build(t, []Route{
		{Domain: "web.example.com", Upstreams: []string{"shipwick_web_3_2:80", "shipwick_web_3_1:80"}},
		{Domain: "api.example.com", Upstreams: []string{"shipwick_api_7_1:8080"}},
	})

	if p.Admin.Listen != "unix//run/caddy/admin.sock" {
		t.Errorf("admin.listen = %q; omitting it would reset Caddy's admin endpoint and lock the agent out", p.Admin.Listen)
	}
	srv := p.Apps.HTTP.Servers["shipwick"]
	if len(srv.Listen) != 1 || srv.Listen[0] != ":443" {
		t.Errorf("listen = %v, want [:443] (automatic HTTPS adds the :80 redirect itself)", srv.Listen)
	}
	if len(srv.Routes) != 3 {
		t.Fatalf("got %d routes, want 2 + the catch-all", len(srv.Routes))
	}

	api, web, catchAll := srv.Routes[0], srv.Routes[1], srv.Routes[2]
	if api.Match[0].Host[0] != "api.example.com" || web.Match[0].Host[0] != "web.example.com" {
		t.Errorf("routes should be ordered by domain, got %v then %v", api.Match[0].Host, web.Match[0].Host)
	}
	if !api.Terminal || api.Handle[0].Handler != "reverse_proxy" || api.Handle[0].Upstreams[0].Dial != "shipwick_api_7_1:8080" {
		t.Errorf("unexpected api route: %+v", api)
	}
	if got := web.Handle[0].Upstreams; len(got) != 2 || got[0].Dial != "shipwick_web_3_1:80" {
		t.Errorf("upstreams should be sorted: %+v", got)
	}
	if web.Handle[0].FlushInterval != nil {
		t.Error("ordinary routes must keep Caddy's default buffering")
	}

	if catchAll.ID != markerPrefix+fp || len(catchAll.Match) != 0 || catchAll.Handle[0].StatusCode != 404 {
		t.Errorf("last route should be the fingerprinted catch-all 404 without a host matcher: %+v", catchAll)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a := []Route{
		{Domain: "b.example.com", Upstreams: []string{"x:1", "y:1"}},
		{Domain: "a.example.com", Upstreams: []string{"z:1"}},
	}
	b := []Route{
		{Domain: "a.example.com", Upstreams: []string{"z:1"}},
		{Domain: "b.example.com", Upstreams: []string{"y:1", "x:1"}},
	}
	_, fpA, rawA := build(t, a)
	_, fpB, rawB := build(t, b)
	if fpA != fpB || string(rawA) != string(rawB) {
		t.Error("the same routing in a different order must produce the same config, or every tick would reload Caddy")
	}
	if a[0].Domain != "b.example.com" || a[0].Upstreams[0] != "x:1" {
		t.Error("Build must not reorder the caller's slices")
	}

	_, fpC, _ := build(t, []Route{{Domain: "a.example.com", Upstreams: []string{"z:2"}}})
	if fpC == fpA {
		t.Error("a different routing must change the fingerprint")
	}
}

func TestBuildWithoutUpstreamsServes503(t *testing.T) {
	p, _, _ := build(t, []Route{{Domain: "web.example.com"}})
	h := p.Apps.HTTP.Servers["shipwick"].Routes[0].Handle[0]
	if h.Handler != "static_response" || h.StatusCode != 503 {
		t.Errorf("handler = %+v; a known application without healthy replicas is a 503, and keeps its certificate", h)
	}
}

func TestBuildStreamingRoute(t *testing.T) {
	p, _, _ := build(t, []Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true}})
	h := p.Apps.HTTP.Servers["shipwick"].Routes[0].Handle[0]
	if h.FlushInterval == nil || *h.FlushInterval != -1 {
		t.Error("streaming routes need flush_interval -1, or log following would arrive in bursts")
	}
}

func TestBuildTreatsInputAsData(t *testing.T) {
	// pkg/spec rejects such a domain long before it gets here. This asserts
	// the second line of defense: even hostile input cannot change the
	// config's structure, because nothing is ever templated.
	evil := `a.com"]}],"handle":[{"handler":"file_server","root":"/"}]},{"match":[{"host":["b.com`
	p, _, raw := build(t, []Route{{Domain: evil, Upstreams: []string{`x:1"},{"dial":"evil:1`}}})

	routes := p.Apps.HTTP.Servers["shipwick"].Routes
	if len(routes) != 2 || routes[0].Match[0].Host[0] != evil || len(routes[0].Handle[0].Upstreams) != 1 {
		t.Errorf("input altered the config structure: %s", raw)
	}
	if strings.Contains(string(raw), `"handler":"file_server"`) {
		t.Errorf("injected handler appears as structure: %s", raw)
	}
}

// fakeCaddy is a minimal admin API: POST /load, GET /id/<marker>.
type fakeCaddy struct {
	mu     sync.Mutex
	loads  [][]byte
	marker string
	reject string // non-empty: /load answers 400 with this body
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		if f.reject != "" {
			http.Error(w, f.reject, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.loads = append(f.loads, body)
		var p parsed
		json.Unmarshal(body, &p)
		routes := p.Apps.HTTP.Servers["shipwick"].Routes
		f.marker = routes[len(routes)-1].ID
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/id/"):
		if strings.TrimPrefix(r.URL.Path, "/id/") != f.marker {
			http.Error(w, `{"error":"unknown object ID"}`, http.StatusNotFound)
			return
		}
		w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCaddy) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.loads)
}

func newTestCaddy(t *testing.T) (*Caddy, *fakeCaddy) {
	t.Helper()
	fake := &fakeCaddy{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := NewCaddy(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

var someRoutes = []Route{{Domain: "web.example.com", Upstreams: []string{"shipwick_web_1_1:80"}}}

func TestSyncLoadsOnlyWhenRoutingChanges(t *testing.T) {
	c, fake := newTestCaddy(t)
	ctx := context.Background()

	for range 5 {
		if err := c.Sync(ctx, someRoutes); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if n := fake.loadCount(); n != 1 {
		t.Errorf("identical routing was loaded %d times, want 1: the supervisor syncs every second", n)
	}

	changed := []Route{{Domain: "web.example.com", Upstreams: []string{"shipwick_web_2_1:80"}}}
	if err := c.Sync(ctx, changed); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n := fake.loadCount(); n != 2 {
		t.Errorf("changed routing: %d loads, want 2", n)
	}
	if s := c.Status(); !s.Enabled || !s.Reachable || s.Routes != 1 || s.Error != "" {
		t.Errorf("Status = %+v", s)
	}
}

func TestSyncReloadsWhenCaddyLostTheConfig(t *testing.T) {
	c, fake := newTestCaddy(t)
	ctx := context.Background()
	c.Sync(ctx, someRoutes)

	// Caddy restarts and comes back with some older config.
	fake.mu.Lock()
	fake.marker = "something-else"
	fake.mu.Unlock()

	c.Sync(ctx, someRoutes)
	if n := fake.loadCount(); n != 1 {
		t.Errorf("verification should be rate-limited, got %d loads right away", n)
	}

	c.mu.Lock()
	c.verifiedAt = time.Now().Add(-verifyEvery - time.Second)
	c.mu.Unlock()
	if err := c.Sync(ctx, someRoutes); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n := fake.loadCount(); n != 2 {
		t.Errorf("loads = %d, want 2: a lost config must be restored without waiting for a deployment", n)
	}
}

func TestSyncReportsRejectedConfig(t *testing.T) {
	c, fake := newTestCaddy(t)
	fake.reject = `{"error":"loading config: unknown module"}`

	err := c.Sync(context.Background(), someRoutes)
	if err == nil || !strings.Contains(err.Error(), "unknown module") || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v, want Caddy's own explanation", err)
	}
	if s := c.Status(); s.Reachable || s.Error == "" {
		t.Errorf("Status = %+v, want the failure to be visible", s)
	}

	// A failed load must not be remembered as applied.
	fake.mu.Lock()
	fake.reject = ""
	fake.mu.Unlock()
	if err := c.Sync(context.Background(), someRoutes); err != nil || fake.loadCount() != 1 {
		t.Errorf("retry after a rejection: err=%v loads=%d", err, fake.loadCount())
	}
	if s := c.Status(); !s.Reachable || s.Error != "" {
		t.Errorf("Status after recovery = %+v", s)
	}
}

func TestSyncUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c, _ := NewCaddy(srv.URL)

	err := c.Sync(context.Background(), someRoutes)
	if err == nil || !strings.Contains(err.Error(), "cannot reach Caddy") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), "/load") {
		t.Errorf("the message should not leak request details: %v", err)
	}
}

func TestSyncOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets are not available here: %v", err)
	}
	fake := &fakeCaddy{}
	srv := &http.Server{Handler: fake}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	addr := "unix/" + filepath.ToSlash(sock)
	if !strings.HasPrefix(filepath.ToSlash(sock), "/") {
		t.Skip("socket path is not absolute in unix form on this platform")
	}
	c, err := NewCaddy(addr)
	if err != nil {
		t.Fatalf("NewCaddy(%q): %v", addr, err)
	}
	if err := c.Sync(context.Background(), someRoutes); err != nil {
		t.Fatalf("Sync over a unix socket: %v", err)
	}

	var p parsed
	json.Unmarshal(fake.loads[0], &p)
	if p.Admin.Listen != addr {
		t.Errorf("admin.listen = %q, want %q echoed back", p.Admin.Listen, addr)
	}
}

func TestNewCaddyRejectsBadAddresses(t *testing.T) {
	for _, addr := range []string{"", "caddy:2019", "https://caddy:2019", "unix/relative.sock", "unix/", "http://", "http://host:2019/config"} {
		if _, err := NewCaddy(addr); err == nil {
			t.Errorf("NewCaddy(%q): expected an error", addr)
		}
	}
}
