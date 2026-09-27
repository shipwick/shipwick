package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestMetricsHistoryEndpoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig) // 2 replicas, cpu 1, 512mb
	f.engine.Wait()

	// Samples as the sampler would have stored them: every 30s, two replicas.
	app, err := f.store.GetApplication(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var rows []store.MetricSample
	for i := 1; i <= 20; i++ {
		at := now.Add(-time.Duration(i) * 30 * time.Second)
		rows = append(rows,
			store.MetricSample{ApplicationID: app.ID, Replica: 1, At: at, CPUPercent: 10, MemoryBytes: 100 << 20},
			store.MetricSample{ApplicationID: app.ID, Replica: 2, At: at, CPUPercent: 30, MemoryBytes: 300 << 20})
	}
	// Older than any window a client can ask for except the week.
	rows = append(rows, store.MetricSample{ApplicationID: app.ID, Replica: 1, At: now.Add(-2 * 24 * time.Hour), CPUPercent: 70, MemoryBytes: 1})
	if err := f.store.AddSamples(ctx, rows); err != nil {
		t.Fatal(err)
	}

	status, body := f.do("GET", "/api/v1/applications/my-api/metrics/history", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	h := decode[api.MetricsHistory](t, body)
	if h.Application != "my-api" || h.Step != "30s" || h.Limits.CPU != 1 || h.Limits.MemoryBytes != 512<<20 {
		t.Errorf("unexpected history: %+v", h)
	}
	if since := now.Sub(h.Since); since < 59*time.Minute || since > 61*time.Minute {
		t.Errorf("since = %s ago; the default window is one hour", since)
	}
	if len(h.Series) != 2 || h.Series[0].Replica != 1 || h.Series[1].Replica != 2 {
		t.Fatalf("series = %+v", h.Series)
	}
	if n := len(h.Series[0].Points); n != 20 {
		t.Errorf("points = %d, want the 20 samples of the last hour, one per 30s bucket", n)
	}
	if p := h.Series[1].Points[0]; p.CPUPercent != 30 || p.MemoryBytes != 300<<20 || p.At.IsZero() {
		t.Errorf("point = %+v", p)
	}

	// The exact field names are a contract with the dashboard.
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	json.Unmarshal(body, &raw)
	for _, field := range []string{"application", "since", "step", "series", "limits"} {
		if _, ok := raw.Data[field]; !ok {
			t.Errorf("missing field %q in %s", field, body)
		}
	}
	if !strings.Contains(string(body), `"limits":{"cpu":1,"memory_bytes":536870912}`) {
		t.Errorf("limits are not the deploy.yaml values: %s", body)
	}
	if !strings.Contains(string(body), `"cpu_percent":30,"memory_bytes":314572800}`) {
		t.Errorf("points carry at, cpu_percent and memory_bytes: %s", body)
	}

	// A wider window is served in coarser buckets.
	_, body = f.do("GET", "/api/v1/applications/my-api/metrics/history?since=24h", "")
	if h := decode[api.MetricsHistory](t, body); h.Step != "5m" || len(h.Series[0].Points) < 2 || len(h.Series[0].Points) > 3 {
		t.Errorf("24h: step=%s points=%d; ten minutes of samples make two or three 5m buckets", h.Step, len(h.Series[0].Points))
	}
	_, body = f.do("GET", "/api/v1/applications/my-api/metrics/history?since=7d", "")
	if h := decode[api.MetricsHistory](t, body); h.Step != "1h" || len(h.Series[0].Points) < 2 {
		t.Errorf("7d: step=%s points=%d; the two-day-old sample is in the week", h.Step, len(h.Series[0].Points))
	}
}

func TestMetricsHistoryValidatesSince(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	for _, bad := range []string{"2h", "1d", "60", "1H"} {
		status, body := f.do("GET", "/api/v1/applications/my-api/metrics/history?since="+bad, "")
		if status != http.StatusBadRequest {
			t.Errorf("since=%q: status = %d, want 400", bad, status)
			continue
		}
		if e := decodeError(t, body); e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "1h, 24h or 7d") {
			t.Errorf("since=%q: error = %+v; it should list the accepted values", bad, e)
		}
	}
	if status, _ := f.do("GET", "/api/v1/applications/ghost/metrics/history", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: status = %d", status)
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/applications/my-api/metrics/history", "", ""); status != http.StatusUnauthorized {
		t.Errorf("without a token: status = %d", status)
	}
}

func TestMetricsHistoryOfAnApplicationNeverDeployed(t *testing.T) {
	f := newFixture(t)
	f.rt.PullErr = errors.New("nope")
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()
	status, body := f.do("GET", "/api/v1/applications/my-api/metrics/history", "")
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeNotDeployed {
		t.Errorf("status = %d, body = %s; want 409 NOT_DEPLOYED", status, body)
	}
}

func TestServerReportsNotifications(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("GET", "/api/v1/server", "")
	if !strings.Contains(string(body), `"notifications":{"webhook":false}`) {
		t.Errorf("server view should always carry the notification status: %s", body)
	}
}
