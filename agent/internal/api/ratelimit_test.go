package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// from sends a request as a client at addr, which httptest's server cannot do:
// every request through it comes from loopback.
func (f *fixture) from(h http.Handler, addr, path, auth string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = addr + ":40000"
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTooManyFailedAuthenticationsAreRateLimited(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	h := f.api.Handler()
	const guesser, other = "198.51.100.7", "203.0.113.9"
	const good = "Bearer " + testToken

	for i := range failedAuthLimit {
		clock = clock.Add(time.Second)
		if rec := f.from(h, guesser, "/api/v1/applications", "Bearer wrong-"+string(rune('a'+i))); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want 401 while the limit is not reached", i+1, rec.Code)
		}
	}

	rec := f.from(h, guesser, "/api/v1/applications", good)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: status = %d, want 429 even with the right token", failedAuthLimit, rec.Code)
	}
	if e := decodeError(t, rec.Body.Bytes()); e.Code != api.CodeRateLimited {
		t.Errorf("code = %q, want %s", e.Code, api.CodeRateLimited)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60: the block lasts a minute from the failure that reached the limit", got)
	}
	if rec := f.from(h, guesser, "/api/v1/applications", "Bearer still-wrong"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a wrong token during the block: status = %d, want 429 without the token being checked", rec.Code)
	}

	// Other clients and the unauthenticated endpoint are not affected.
	if rec := f.from(h, other, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("another address: status = %d, want 200", rec.Code)
	}
	if rec := f.from(h, other, "/api/v1/applications", "Bearer wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("another address's own failure: status = %d, want 401", rec.Code)
	}
	if rec := f.from(h, guesser, "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /health from the blocked address: status = %d, want 200", rec.Code)
	}

	clock = clock.Add(30 * time.Second)
	if rec := f.from(h, guesser, "/api/v1/applications", good); rec.Code != http.StatusTooManyRequests {
		t.Errorf("half a minute in: status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After on the first refusal = %q", got)
	}
	clock = clock.Add(31 * time.Second)
	if rec := f.from(h, guesser, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("after the minute: status = %d, want 200; the right token works again", rec.Code)
	}
}

func TestFailuresOlderThanAMinuteDoNotCount(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	h := f.api.Handler()
	const addr = "198.51.100.7"

	for range failedAuthLimit - 1 {
		f.from(h, addr, "/api/v1/applications", "Bearer wrong")
	}
	clock = clock.Add(failedAuthWindow + time.Second)
	f.from(h, addr, "/api/v1/applications", "Bearer wrong")
	if rec := f.from(h, addr, "/api/v1/applications", "Bearer "+testToken); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %d failures a minute ago and one now are not %d within a minute", rec.Code, failedAuthLimit-1, failedAuthLimit)
	}
}

func TestSuccessfulAuthenticationsAreNeverLimited(t *testing.T) {
	f := newFixture(t)
	h := f.api.Handler()
	for i := range 3 * failedAuthLimit {
		if rec := f.from(h, "198.51.100.7", "/api/v1/applications", "Bearer "+testToken); rec.Code != http.StatusOK {
			t.Fatalf("request %d with the right token: status = %d; only failures count", i+1, rec.Code)
		}
	}
}
