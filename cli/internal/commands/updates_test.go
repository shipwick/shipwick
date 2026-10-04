package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestServerStatusSaysWhenANewerReleaseIsAvailable(t *testing.T) {
	checked := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "v0.7.0", Update: &api.UpdateStatus{Enabled: true, LatestVersion: "v0.7.1", CheckedAt: &checked, Available: true}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "v0.7.0  v0.7.1 is available  (on the server, run the installer again: curl -fsSL https://get.shipwick.com | sh)") {
		t.Errorf("no notice of the newer release:\n%s", out)
	}
}

func TestServerStatusSaysNothingOfReleasesWithoutANewerOne(t *testing.T) {
	checked := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for name, update := range map[string]*api.UpdateStatus{
		"an agent from before 0.7":    nil,
		"an agent told not to ask":    {},
		"an agent that cannot ask":    {Enabled: true},
		"an agent that is up to date": {Enabled: true, LatestVersion: "v0.7.0", CheckedAt: &checked},
	} {
		f := newFakeAgent(t)
		f.server = api.Server{AgentVersion: "v0.7.0", Update: update}
		out, _, err := f.run(t.TempDir(), "server", "status")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "available") || !strings.Contains(out, "v0.7.0") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}
