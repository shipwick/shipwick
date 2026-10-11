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
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// stubVerifier is the Cloudflare check with a fixed result.
type stubVerifier struct {
	status api.CloudflareStatus
	calls  int
}

func (s *stubVerifier) Verify(context.Context) api.CloudflareStatus {
	s.calls++
	return s.status
}

// newCloudflareFixture is newFixture for an agent that has a Cloudflare
// token, checked by the stub.
func newCloudflareFixture(t *testing.T, verifier deploy.CloudflareVerifier) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:", store.Options{EncryptionKey: []byte("an-encryption-key-of-32-bytes!!!")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet, UploadDir: t.TempDir(), Cloudflare: verifier})
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

func TestVerifyingTheCloudflareTokenWithoutOneIsAConflict(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/server/cloudflare/verify", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeCloudflareNotConfigured || !strings.Contains(e.Message, "SHIPWICK_CLOUDFLARE_API_TOKEN") {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	status, body = f.do("GET", "/api/v1/server", "")
	if status != http.StatusOK || strings.Contains(string(body), `"cloudflare"`) {
		t.Errorf("GET /server carries a cloudflare field without a token: %s", body)
	}
}

func TestVerifyingTheCloudflareTokenAnswersTheCheckOnceAMinute(t *testing.T) {
	stub := &stubVerifier{status: api.CloudflareStatus{OK: false, Address: "2a01:4f8:1c1b:5e07::1",
		Zones:    []api.CloudflareZone{{Name: "example.com", DNSEdit: true}, {Name: "shipwick.com"}},
		Problems: []string{"the token cannot edit DNS records of shipwick.com: it needs Zone → DNS → Edit (DNS Settings, DNS Firewall and DNS Views are other permissions)"}}}
	f := newCloudflareFixture(t, stub)

	status, body := f.do("POST", "/api/v1/server/cloudflare/verify", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	got := decode[api.CloudflareStatus](t, body)
	if got.OK || got.Address != "2a01:4f8:1c1b:5e07::1" || len(got.Zones) != 2 || len(got.Problems) != 1 || got.CheckedAt.IsZero() {
		t.Errorf("result = %+v", got)
	}
	var shape struct{ Data map[string]json.RawMessage }
	if err := json.Unmarshal(body, &shape); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"checked_at", "ok", "address", "zones", "problems"} {
		if _, ok := shape.Data[field]; !ok {
			t.Errorf("the result lacks %q: %s", field, body)
		}
	}
	if !strings.Contains(string(body), `{"name":"shipwick.com","dns_edit":false}`) {
		t.Errorf("zones are not {name, dns_edit}: %s", body)
	}

	status, body = f.do("POST", "/api/v1/server/cloudflare/verify", "")
	e := decodeError(t, body)
	if status != http.StatusTooManyRequests || e.Code != api.CodeCloudflareCheckedRecently {
		t.Fatalf("second check: status = %d, body = %s", status, body)
	}
	retry, _ := e.Details["retry_after_seconds"].(float64)
	if _, ok := e.Details["checked_at"].(string); !ok || retry < 55 || retry > 60 {
		t.Errorf("details = %v, want checked_at and retry_after_seconds near 60", e.Details)
	}
	if stub.calls != 1 {
		t.Errorf("Cloudflare was asked %d times, want once", stub.calls)
	}

	status, body = f.do("GET", "/api/v1/server", "")
	server := decode[api.Server](t, body)
	if status != http.StatusOK || server.Cloudflare == nil || !server.Cloudflare.CheckedAt.Equal(got.CheckedAt) || len(server.Cloudflare.Problems) != 1 {
		t.Errorf("GET /server cloudflare = %+v, want the check's result", server.Cloudflare)
	}

	status, body = f.do("GET", "/api/v1/audit?action=cloudflare.verify", "")
	if status != http.StatusOK || strings.Count(string(body), `"cloudflare.verify"`) != 2 {
		t.Errorf("the two checks asked for were not both recorded: %d %s", status, body)
	}
}
