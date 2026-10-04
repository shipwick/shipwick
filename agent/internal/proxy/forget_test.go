package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// forgetfulCaddy is the fake Caddy with Shipwick's endpoint, as the proxy
// image of this version has it.
type forgetfulCaddy struct {
	*fakeCaddy
	mu        sync.Mutex
	forgotten [][]string
	status    int
}

func (f *forgetfulCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/shipwick/forget" {
		f.fakeCaddy.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct{ Names []string }
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Names == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if f.status != 0 {
		http.Error(w, `{"error":"no"}`, f.status)
		return
	}
	f.forgotten = append(f.forgotten, body.Names)
}

func newForgetfulCaddy(t *testing.T) (*Caddy, *forgetfulCaddy) {
	t.Helper()
	fake := &forgetfulCaddy{fakeCaddy: &fakeCaddy{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := NewCaddy(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func TestForgetIsConfirmedByTheProxy(t *testing.T) {
	c, fake := newForgetfulCaddy(t)
	ctx := context.Background()
	if err := c.Sync(ctx, appRoutes); err != nil {
		t.Fatal(err)
	}

	if err := c.Forget(ctx, []string{"api", "api_8080"}); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	// No names is still a question: can this proxy forget?
	if err := c.Forget(ctx, nil); err != nil {
		t.Fatalf("Forget without names: %v", err)
	}
	if len(fake.forgotten) != 2 || !slices.Equal(fake.forgotten[0], []string{"api", "api_8080"}) || len(fake.forgotten[1]) != 0 {
		t.Errorf("the proxy was told %v", fake.forgotten)
	}

	fake.mu.Lock()
	fake.status = http.StatusInternalServerError
	fake.mu.Unlock()
	if err := c.Forget(ctx, []string{"api"}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v, want the proxy's refusal", err)
	}
}

func TestForgetIsNotConfirmedByAProxyWithoutTheEndpoint(t *testing.T) {
	// The proxy image of 0.7: neither the source nor the endpoint.
	c, fake := newTestCaddy(t)
	fake.older = true
	if err := c.Sync(context.Background(), appRoutes); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(context.Background(), []string{"api"}); err == nil {
		t.Error("a proxy that looks names up the plain way confirmed that it forgot one")
	}

	// A proxy that has the source and not the endpoint answers 404.
	c, _ = newTestCaddy(t)
	if err := c.Sync(context.Background(), appRoutes); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(context.Background(), []string{"api"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v, want the 404 of a proxy without the endpoint", err)
	}
}

func TestForgetIsNotConfirmedWhileTheProxyRunsRoutesWithoutTheSource(t *testing.T) {
	c, fake := newForgetfulCaddy(t)
	// Nothing loaded yet: what the proxy runs was saved by whoever ran
	// before, and may look names up with Caddy's own source.
	if err := c.Forget(context.Background(), []string{"api"}); err == nil {
		t.Error("confirmed before this agent has loaded a configuration")
	}

	// The endpoint is there and the routes were loaded without the source,
	// as during the minute after a proxy was replaced by Shipwick's: Caddy's
	// own source keeps its answers where nobody can drop them.
	fake.older = true
	if err := c.Sync(context.Background(), appRoutes); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(context.Background(), []string{"api"}); err == nil || len(fake.forgotten) != 0 {
		t.Errorf("err = %v, the proxy was told %v; want neither a confirmation nor a question", err, fake.forgotten)
	}
}

func TestForgetIsNotConfirmedByAProxyThatIsGone(t *testing.T) {
	c, fake := newForgetfulCaddy(t)
	if err := c.Sync(context.Background(), appRoutes); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Forget(ctx, []string{"api"}); err == nil || len(fake.forgotten) != 0 {
		t.Errorf("err = %v", err)
	}
}
