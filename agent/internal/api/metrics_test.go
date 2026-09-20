package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestMetricsEndpoint(t *testing.T) {
	f := newFixture(t)
	f.rt.CPUBusy, f.rt.MemoryUsed = 0.25, 128<<20
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig) // 2 replicas, 512mb each
	f.engine.Wait()

	status, body := f.do("GET", "/api/v1/applications/my-api/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	m := decode[api.Metrics](t, body)
	if m.Application != "my-api" || len(m.Replicas) != 2 || m.MemoryBytes != 256<<20 || m.MemoryLimitBytes != 1<<30 {
		t.Errorf("unexpected metrics: %+v", m)
	}
	if m.CPULimitPercent != 200 || m.Replicas[0].CPULimitPercent != 100 {
		t.Errorf("cpu limits = %v / %v; `cpu: 1` per replica is 100%% of one core, 200%% for two replicas", m.CPULimitPercent, m.Replicas[0].CPULimitPercent)
	}
	if m.CPUPercent < 40 || m.CPUPercent > 60 {
		t.Errorf("cpu_percent = %v, want ~50 (2 replicas × a quarter core)", m.CPUPercent)
	}

	// The exact field names are a contract: the dashboard was written against them.
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	json.Unmarshal(body, &raw)
	for _, field := range []string{"application", "collected_at", "cpu_percent", "cpu_limit_percent", "memory_bytes", "memory_limit_bytes", "replicas"} {
		if _, ok := raw.Data[field]; !ok {
			t.Errorf("missing field %q in %s", field, body)
		}
	}
	var replicas []map[string]json.RawMessage
	json.Unmarshal(raw.Data["replicas"], &replicas)
	for _, field := range []string{"replica", "container", "cpu_percent", "memory_bytes", "memory_limit_bytes"} {
		if _, ok := replicas[0][field]; !ok {
			t.Errorf("missing replica field %q in %s", field, body)
		}
	}

	if status, _ := f.do("GET", "/api/v1/applications/ghost/metrics", ""); status != http.StatusNotFound {
		t.Errorf("unknown app: status = %d", status)
	}
}
