package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestStatusShowsAReplacedContainerAsStoppingAndNotAsAReplica(t *testing.T) {
	f := newFakeAgent(t)
	started := fixedNow.Add(-time.Minute)
	f.app = api.ApplicationDetail{
		Application: api.Application{Name: "my-api", Status: api.AppHealthy, Replicas: api.ReplicaCount{Desired: 1, Running: 1, Healthy: 1}},
		Containers: []api.Container{
			// As the agent lists them: the older container first.
			{Replica: 1, Name: "shipwick_my-api_3_1", State: "running", StartedAt: &started, Health: api.HealthHealthy, Stopping: true},
			{Replica: 1, Name: "shipwick_my-api_4_1", State: "running", StartedAt: &started, Health: api.HealthHealthy},
		},
	}
	out, _, err := f.run(t.TempDir(), "status", "my-api")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	assertInOrder(t, out, []string{
		"1/1 healthy",
		"REPLICA", "STATE",
		"1", "shipwick_my-api_4_1", "running", "healthy", "1m ago",
		"-", "shipwick_my-api_3_1", "stopping",
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "shipwick_my-api_3_1") {
			if strings.HasPrefix(line, "1") || strings.Contains(line, "healthy") || strings.Contains(line, "ago") {
				t.Errorf("the stopping container is shown like a replica: %q", line)
			}
		}
	}
}

func TestPsMarksCertificateProblemsAndAlerts(t *testing.T) {
	f := newFakeAgent(t)
	base := api.Application{Name: "my-api", Status: api.AppHealthy, Version: "1.4.2", Domain: "api.example.com",
		Replicas: api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}, UpdatedAt: fixedNow.Add(-5 * time.Minute)}
	line := func(app api.Application) string {
		t.Helper()
		f.app.Application = app
		out, _, err := f.run(t.TempDir(), "ps")
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || strings.TrimRight(lines[0], " ") != lines[0] || !strings.HasSuffix(lines[0], "UPDATED") {
			t.Fatalf("unexpected table:\n%s", out)
		}
		return lines[1]
	}

	if got := line(base); !strings.HasSuffix(got, "5m ago") {
		t.Errorf("an application with nothing to say has a marker: %q", got)
	}

	app := base
	app.CertificateProblem = &api.CertificateProblem{Hostname: "www.example.com", Status: api.CertWaitingForDNS, Message: "does not resolve yet"}
	if got := line(app); !strings.HasSuffix(got, "5m ago    certificate waiting for DNS") {
		t.Errorf("line = %q", got)
	}

	app.CertificateProblem.Status = api.CertExpiring
	app.AlertCount, app.AlertSeverity = 2, api.SeverityCritical
	if got := line(app); !strings.HasSuffix(got, "5m ago    certificate expiring, 2 alerts") {
		t.Errorf("line = %q", got)
	}

	app = base
	app.AlertCount, app.AlertSeverity = 1, api.SeverityWarning
	if got := line(app); !strings.HasSuffix(got, "5m ago    1 alert") {
		t.Errorf("line = %q", got)
	}
}
