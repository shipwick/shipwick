package client

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestMetricsHistoryRequest(t *testing.T) {
	var got *http.Request
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		fmt.Fprint(w, `{"data": {"application": "my-api", "step": "5m", "series": [{"replica": 1, "points": [{"at": "2026-03-01T10:00:00Z", "cpu_percent": 12.5, "memory_bytes": 12345678}]}], "limits": {"cpu": 1, "memory_bytes": 536870912}}}`)
	})
	h, err := c.MetricsHistory(context.Background(), "my-api", "24h")
	if err != nil {
		t.Fatalf("MetricsHistory: %v", err)
	}
	if got.URL.Path != "/api/v1/applications/my-api/metrics/history" || got.URL.Query().Get("since") != "24h" {
		t.Errorf("unexpected URL: %s", got.URL)
	}
	if h.Step != "5m" || len(h.Series) != 1 || h.Series[0].Points[0].CPUPercent != 12.5 || h.Limits.MemoryBytes != 512<<20 {
		t.Errorf("unexpected history: %+v", h)
	}
}
