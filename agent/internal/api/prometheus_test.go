package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

func TestExpositionIsTheTextFormatSortedAndComplete(t *testing.T) {
	sc := deploy.Scrape{
		Applications: []deploy.ScrapeApplication{
			{
				Name: "blog", Status: api.AppStopped, Observed: true,
				Replicas:    api.ReplicaCount{Desired: 1},
				Deployments: deploy.DeploymentOutcomes{Succeeded: 3},
				ReplicaStats: []deploy.ScrapeReplica{
					{Replica: 1, Restarts: 2},
				},
				LastDuration: 2500 * time.Millisecond, Completed: true,
			},
			{
				Name: "my-api", Status: api.AppCrashLoop, Observed: true,
				Replicas:    api.ReplicaCount{Desired: 2, Running: 2, Healthy: 1},
				Deployments: deploy.DeploymentOutcomes{Succeeded: 12, Failed: 2, RolledBack: 1},
				ReplicaStats: []deploy.ScrapeReplica{
					{Replica: 1, Restarts: 7, Sampled: true, CPUCores: 0.25, MemoryBytes: 216006656, MemoryLimitBytes: 1 << 30},
					{Replica: 2, Sampled: true, CPUCores: 1.5, MemoryBytes: 1 << 20},
				},
				LastDuration: 14 * time.Second, Completed: true,
			},
			// Deployed a moment ago: the supervisor has not seen it yet.
			{Name: "new", Replicas: api.ReplicaCount{Desired: 1}},
		},
		Disk: &api.DiskUsage{TotalBytes: 40 << 30, UsedBytes: 35 << 30},
		Alerts: []api.Alert{
			{Kind: api.AlertMemory, Severity: api.SeverityWarning, Application: "my-api", Replica: 1},
			{Kind: api.AlertMemory, Severity: api.SeverityWarning, Application: "my-api", Replica: 2},
			{Kind: api.AlertDisk, Severity: api.SeverityCritical},
		},
	}
	want := strings.ReplaceAll(`# HELP shipwick_agent_info The running agent; the value is always 1.
# TYPE shipwick_agent_info gauge
shipwick_agent_info{version="VERSION"} 1
# HELP shipwick_application_status The current status of each application, in lower case; the value is always 1.
# TYPE shipwick_application_status gauge
shipwick_application_status{application="blog",status="stopped"} 1
shipwick_application_status{application="my-api",status="crash_loop"} 1
# HELP shipwick_application_replicas Replicas of each application: desired, running, and healthy (running and not failing the health check).
# TYPE shipwick_application_replicas gauge
shipwick_application_replicas{application="blog",state="desired"} 1
shipwick_application_replicas{application="blog",state="healthy"} 0
shipwick_application_replicas{application="blog",state="running"} 0
shipwick_application_replicas{application="my-api",state="desired"} 2
shipwick_application_replicas{application="my-api",state="healthy"} 1
shipwick_application_replicas{application="my-api",state="running"} 2
shipwick_application_replicas{application="new",state="desired"} 1
# HELP shipwick_replica_cpu_ratio CPU usage of a replica in cores, from the last sample: 1 is one core kept busy.
# TYPE shipwick_replica_cpu_ratio gauge
shipwick_replica_cpu_ratio{application="my-api",replica="1"} 0.25
shipwick_replica_cpu_ratio{application="my-api",replica="2"} 1.5
# HELP shipwick_replica_memory_bytes Working set of a replica, from the last sample.
# TYPE shipwick_replica_memory_bytes gauge
shipwick_replica_memory_bytes{application="my-api",replica="1"} 2.16006656e+08
shipwick_replica_memory_bytes{application="my-api",replica="2"} 1.048576e+06
# HELP shipwick_replica_memory_limit_bytes Memory limit of a replica; absent for a replica without one.
# TYPE shipwick_replica_memory_limit_bytes gauge
shipwick_replica_memory_limit_bytes{application="my-api",replica="1"} 1.073741824e+09
# HELP shipwick_replica_restarts_total Restarts of a replica by the supervisor since its deployment.
# TYPE shipwick_replica_restarts_total counter
shipwick_replica_restarts_total{application="blog",replica="1"} 2
shipwick_replica_restarts_total{application="my-api",replica="1"} 7
shipwick_replica_restarts_total{application="my-api",replica="2"} 0
# HELP shipwick_deployments_total Finished deployments of each application by outcome: succeeded, failed, rolled_back.
# TYPE shipwick_deployments_total counter
shipwick_deployments_total{application="blog",status="failed"} 0
shipwick_deployments_total{application="blog",status="rolled_back"} 0
shipwick_deployments_total{application="blog",status="succeeded"} 3
shipwick_deployments_total{application="my-api",status="failed"} 2
shipwick_deployments_total{application="my-api",status="rolled_back"} 1
shipwick_deployments_total{application="my-api",status="succeeded"} 12
shipwick_deployments_total{application="new",status="failed"} 0
shipwick_deployments_total{application="new",status="rolled_back"} 0
shipwick_deployments_total{application="new",status="succeeded"} 0
# HELP shipwick_deployment_last_duration_seconds How long the most recently completed deployment of each application took.
# TYPE shipwick_deployment_last_duration_seconds gauge
shipwick_deployment_last_duration_seconds{application="blog"} 2.5
shipwick_deployment_last_duration_seconds{application="my-api"} 14
# HELP shipwick_disk_bytes The disk that holds the agent's data, images and volumes: total and used, counted as df does.
# TYPE shipwick_disk_bytes gauge
shipwick_disk_bytes{state="total"} 4.294967296e+10
shipwick_disk_bytes{state="used"} 3.758096384e+10
# HELP shipwick_alerts Active alerts by kind and severity.
# TYPE shipwick_alerts gauge
shipwick_alerts{kind="disk",severity="critical"} 1
shipwick_alerts{kind="disk",severity="warning"} 0
shipwick_alerts{kind="docker",severity="critical"} 0
shipwick_alerts{kind="docker",severity="warning"} 0
shipwick_alerts{kind="memory",severity="critical"} 0
shipwick_alerts{kind="memory",severity="warning"} 2
shipwick_alerts{kind="restarts",severity="critical"} 0
shipwick_alerts{kind="restarts",severity="warning"} 0
shipwick_alerts{kind="unhealthy",severity="critical"} 0
shipwick_alerts{kind="unhealthy",severity="warning"} 0
`, "VERSION", version.Version)
	got := string(exposition(sc))
	if got != want {
		t.Errorf("exposition:\n%s\nwant:\n%s", got, want)
	}
	if again := string(exposition(sc)); again != got {
		t.Error("two renderings of the same state differ")
	}
}

func TestExpositionWithoutADiskMeasurementHasNoDiskSeries(t *testing.T) {
	got := string(exposition(deploy.Scrape{}))
	if strings.Contains(got, "shipwick_disk_bytes{") || !strings.Contains(got, "# TYPE shipwick_disk_bytes gauge\n") {
		t.Errorf("exposition = %s", got)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	var p promWriter
	p.family("x", "gauge", "a \\ and a\nline feed")
	p.series(1, "version", "1.0 \"dev\" C:\\build\nx")
	want := "# HELP x a \\\\ and a\\nline feed\n# TYPE x gauge\nx{version=\"1.0 \\\"dev\\\" C:\\\\build\\nx\"} 1\n"
	if got := p.buf.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestMetricsEndpointIsForAnyTokenAndNobodyElse(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	if status, _ := f.doWithAuth("GET", "/metrics", "", ""); status != http.StatusUnauthorized {
		t.Errorf("without a token: status = %d", status)
	}
	req, _ := http.NewRequest("GET", f.srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+f.createToken("prometheus", api.RoleRead).Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("read token: status = %d, content type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	status, body := f.do("GET", "/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	for _, line := range []string{
		`shipwick_agent_info{version="` + version.Version + `"} 1`,
		`shipwick_application_replicas{application="my-api",state="desired"} 2`,
		`shipwick_deployments_total{application="my-api",status="succeeded"} 1`,
		`shipwick_replica_restarts_total{application="my-api",replica="2"} 0`,
		`shipwick_alerts{kind="disk",severity="warning"} 0`,
	} {
		if !strings.Contains(string(body), line+"\n") {
			t.Errorf("missing %s in:\n%s", line, body)
		}
	}
	if strings.Contains(string(body), "hunter2") {
		t.Error("the metrics leak an env value")
	}
}

func TestServerViewCarriesAlertsAndDisk(t *testing.T) {
	f := newFixture(t)
	_, body := f.do("GET", "/api/v1/server", "")
	if !strings.Contains(string(body), `"alerts":[]`) || !strings.Contains(string(body), `"disk":null`) {
		t.Errorf("server view without alerts, where the disk cannot be measured: %s", body)
	}
}
