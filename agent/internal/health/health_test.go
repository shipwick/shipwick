package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func target(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestCheckStatusCodes(t *testing.T) {
	tests := []struct {
		status  int
		healthy bool
	}{
		{200, true}, {204, true}, {299, true},
		{301, false}, {404, false}, {500, false}, {503, false},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" || r.URL.RawQuery != "deep=1" {
				t.Errorf("unexpected request: %s", r.URL)
			}
			if tt.status == 301 {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(tt.status)
		}))
		ip, port := target(t, srv)
		err := New().Check(context.Background(), ip, port, "/health?deep=1", time.Second)
		if (err == nil) != tt.healthy {
			t.Errorf("status %d: err = %v, healthy should be %v", tt.status, err, tt.healthy)
		}
		if err != nil && !strings.Contains(err.Error(), strconv.Itoa(tt.status)) {
			t.Errorf("status %d: the error should name the status, got %q", tt.status, err)
		}
		srv.Close()
	}
}

func TestCheckDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	ip, port := target(t, srv)
	if err := New().Check(context.Background(), ip, port, "/health", time.Second); err == nil {
		t.Error("a redirect is not a 2xx")
	}
	if followed.Load() {
		t.Error("the probe must not follow redirects")
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ip, port := target(t, srv)
	start := time.Now()
	err := New().Check(context.Background(), ip, port, "/health", 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "no response within 100ms") {
		t.Errorf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the timeout was not enforced: took %s", elapsed)
	}
}

func TestCheckConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	ip, port := target(t, srv)
	srv.Close() // nothing listens there any more

	err := New().Check(context.Background(), ip, port, "/health", time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	// The message must be short enough for a status line: no URLs, no
	// "Get ...: dial tcp ...:" chains.
	if strings.Contains(err.Error(), "http://") || len(err.Error()) > 120 {
		t.Errorf("error is not user-friendly: %q", err)
	}
}

func TestCheckIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ip, port := target(t, srv)
	if err := New().Check(context.Background(), ip, port, "/health", time.Second); err != nil {
		t.Errorf("probes must go direct, never through a proxy: %v", err)
	}
}

func TestCheckOpensAFreshConnectionEveryTime(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	ip, port := target(t, srv)
	c := New()
	for range 3 {
		if err := c.Check(context.Background(), ip, port, "/health", time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if got := conns.Load(); got != 3 {
		t.Errorf("connections = %d, want 3 (keep-alive would hide a dead listener)", got)
	}
}

func TestCheckCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	ip, port := target(t, srv)
	if err := New().Check(ctx, ip, port, "/health", 10*time.Second); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled so callers can tell shutdown from failure", err)
	}
}
