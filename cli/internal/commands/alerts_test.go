package commands

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const (
	diskWarning    = "The server's disk is 87% full (5.2 GB of 40 GB free). See what takes the space with: docker system df"
	unhealthyAlert = "my-api has not been healthy for an hour: 1/2 replicas ready. See why with: shipwick status my-api"
)

func TestServerStatusShowsTheDiskAndActiveAlerts(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1",
		Disk: &api.DiskUsage{TotalBytes: 40 << 30, UsedBytes: 35 << 30},
		Alerts: []api.Alert{
			{Kind: api.AlertDisk, Severity: api.SeverityWarning, Message: diskWarning},
			{Kind: api.AlertUnhealthy, Severity: api.SeverityCritical, Application: "my-api", Message: unhealthyAlert},
		}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v", err)
	}
	assertInOrder(t, out, []string{"Notifications", "Disk", "35 GB of 40 GB used (87%)", "! " + diskWarning, "✗ " + unhealthyAlert})

	// An older agent knows neither field; a development agent off Linux has
	// no disk to report.
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1"}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	if strings.Contains(out, "Disk") || strings.Contains(out, "!") {
		t.Errorf("without disk and alerts:\n%s", out)
	}
}

func TestDoctorReportsActiveAlerts(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	f.server.Alerts = []api.Alert{{Kind: api.AlertDisk, Severity: api.SeverityWarning, Message: diskWarning}}
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("a warning is worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Proxy serving 2 routes", "! " + diskWarning, "No problems; 3 things worth a look."})

	f.server.Alerts = append(f.server.Alerts, api.Alert{Kind: api.AlertUnhealthy, Severity: api.SeverityCritical, Application: "my-api", Message: unhealthyAlert})
	out, _, err = f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("a critical alert is a problem, err = %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"! " + diskWarning, "✗ " + unhealthyAlert, "1 problem found."})
}
