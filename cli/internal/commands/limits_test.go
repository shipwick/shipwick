package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func TestDoctorSaysWhenDockerEnforcesNoLimits(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	f.server.UnlimitedMemory = []string{"postgres"}
	f.server.Docker = &api.DockerStatus{Rootless: true, UnenforcedLimits: []string{api.LimitMemory, api.LimitCPU}}
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"! Docker on the server is rootless and does not enforce memory and CPU limits: no replica is held to resources.memory and resources.cpu of its deploy.yaml, and the usage shown for a replica is not its own.",
		"Delegate the cpu and memory cgroup controllers to the user who runs Docker, on a server with systemd (handbook: Rootless Docker)",
	})
	if strings.Contains(out, "without a memory limit") {
		t.Errorf("where no memory limit is enforced, setting one is no advice:\n%s", out)
	}

	// A daemon that runs as root and lacks one controller.
	f.server.Docker = &api.DockerStatus{UnenforcedLimits: []string{api.LimitMemory}}
	out, _, _ = f.run(t.TempDir(), "doctor")
	assertInOrder(t, out, []string{
		"! Docker on the server does not enforce memory limits: no replica is held to resources.memory of its deploy.yaml.",
		"docker info on the server says which",
	})

	// One that enforces everything, rootless or not, and an agent too old to say.
	for _, status := range []*api.DockerStatus{{Rootless: true, UnenforcedLimits: []string{}}, nil} {
		f.server.Docker = status
		out, _, _ = f.run(t.TempDir(), "doctor")
		if strings.Contains(out, "does not enforce") {
			t.Errorf("docker %+v: nothing to say about limits:\n%s", status, out)
		}
		assertInOrder(t, out, []string{"! 1 application runs without a memory limit: postgres."})
	}
}

func TestServerStatusSaysHowDockerRuns(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1", DockerVersion: "29.8.2"}
	for _, tc := range []struct {
		docker *api.DockerStatus
		want   string
	}{
		{nil, "29.8.2\n"},
		{&api.DockerStatus{UnenforcedLimits: []string{}}, "29.8.2\n"},
		{&api.DockerStatus{Rootless: true, UnenforcedLimits: []string{}}, "29.8.2, rootless\n"},
		{&api.DockerStatus{Rootless: true, UnenforcedLimits: []string{api.LimitMemory, api.LimitCPU}}, "29.8.2, rootless; memory and CPU limits are not enforced\n"},
		{&api.DockerStatus{UnenforcedLimits: []string{api.LimitCPU}}, "29.8.2; CPU limits are not enforced\n"},
	} {
		f.server.Docker = tc.docker
		out, _, err := f.run(t.TempDir(), "server", "status")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("docker %+v: want a Docker line ending %q in\n%s", tc.docker, tc.want, out)
		}
	}
}

func TestStatusSaysWhenAnApplicationsLimitsAreNotEnforced(t *testing.T) {
	both := spec.Resources{CPU: 0.5, MemoryBytes: 64 << 20}
	none := []string{api.LimitMemory, api.LimitCPU}
	for _, tc := range []struct {
		name       string
		resources  spec.Resources
		unenforced []string
		want       string
	}{
		{"a daemon that applies them", both, nil, ""},
		{"an application without limits", spec.Resources{}, none, ""},
		{"both", both, none, "  (Docker on this server does not enforce the memory and CPU limits; see shipwick doctor)"},
		{"the one it has", spec.Resources{MemoryBytes: 64 << 20}, none, "  (Docker on this server does not enforce the memory limit; see shipwick doctor)"},
		{"the one it has not", spec.Resources{MemoryBytes: 64 << 20}, []string{api.LimitCPU}, ""},
	} {
		if got := notEnforced(tc.resources, tc.unenforced); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
