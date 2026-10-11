package commands

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func healthyDoctor(t *testing.T) *fakeAgent {
	t.Helper()
	return doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
}

func TestDoctorSaysWhenTheProxyIsNotOnAllOfItsNetworks(t *testing.T) {
	f := healthyDoctor(t)
	f.server.Proxy.NetworksMissing = []string{"shipwick-control"}
	out, _, err := f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("a proxy that cannot reach the agent is a problem: err=%v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Proxy serving 2 routes",
		"✗ The proxy is not on the shipwick-control network: it serves some applications and not others, or cannot reach the agent. On the server: cd /opt/shipwick && docker compose up -d --force-recreate caddy",
	})

	f.server.Proxy.NetworksMissing = []string{"shipwick-services", "shipwick-control"}
	out, _, _ = f.run(t.TempDir(), "doctor")
	if !strings.Contains(out, "not on the shipwick-services and shipwick-control networks") {
		t.Errorf("two networks:\n%s", out)
	}

	f.server.Proxy.NetworksMissing = nil
	out, _, err = f.run(t.TempDir(), "doctor")
	if err != nil || strings.Contains(out, "is not on") {
		t.Errorf("an agent that reports nothing missing, or does not say: err=%v\n%s", err, out)
	}
}

func TestServerStatusNamesTheNetworksTheProxyIsNotOn(t *testing.T) {
	f := healthyDoctor(t)
	f.server.Proxy.NetworksMissing = []string{"shipwick-control"}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "serving 2 routes; not on the shipwick-control network (on the server: docker compose up -d --force-recreate caddy)") {
		t.Errorf("server status:\n%s", out)
	}
}

func TestDoctorWarnsWhenTheContainersApproachTheKernelsTableOfNeighbours(t *testing.T) {
	f := healthyDoctor(t)
	f.server.Containers = 300
	f.server.KernelLimits = &api.KernelLimits{NeighbourTable: 1024}
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("a table that is filling is worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"! The server runs 301 containers; the kernel's table of neighbours holds 1024 entries and each container takes several: raise net.ipv4.neigh.default.gc_thresh3 (and gc_thresh1/2) in /etc/sysctl.d before it fills",
	})

	// Raised on the server: 301 of 8192 is nothing to say.
	f.server.KernelLimits = &api.KernelLimits{NeighbourTable: 8192}
	out, _, _ = f.run(t.TempDir(), "doctor")
	if strings.Contains(out, "table of neighbours") {
		t.Errorf("8192 entries for 301 containers is not near full:\n%s", out)
	}

	// A quarter of the table is where the warning starts.
	f.server.Containers, f.server.KernelLimits = 255, &api.KernelLimits{NeighbourTable: 1024}
	out, _, _ = f.run(t.TempDir(), "doctor")
	if !strings.Contains(out, "runs 256 containers") {
		t.Errorf("256 of 1024 is a quarter:\n%s", out)
	}
	f.server.Containers = 254
	out, _, _ = f.run(t.TempDir(), "doctor")
	if strings.Contains(out, "table of neighbours") {
		t.Errorf("255 of 1024 is under a quarter:\n%s", out)
	}

	// An agent in a container cannot read the table; the stock size is
	// named for what it is.
	f.server.Containers, f.server.KernelLimits = 300, nil
	out, _, _ = f.run(t.TempDir(), "doctor")
	assertInOrder(t, out, []string{
		"! The server runs 301 containers; the kernel's table of neighbours holds 1024 entries in a stock kernel and each container takes several. The agent cannot read the setting from its container: check sysctl net.ipv4.neigh.default.gc_thresh3 on the server and raise it (and gc_thresh1/2) in /etc/sysctl.d before it fills",
	})
	f.server.Containers = 10
	out, _, _ = f.run(t.TempDir(), "doctor")
	if strings.Contains(out, "table of neighbours") {
		t.Errorf("11 containers are nothing to warn about:\n%s", out)
	}
}
