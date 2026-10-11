package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestTheProxysNetworksAreCheckedEveryHalfMinuteAndReported(t *testing.T) {
	s, _ := newRouted(t)
	logs := &bytes.Buffer{}
	s.engine.log = slog.New(slog.NewTextHandler(logs, nil))
	s.deploy(web("web:1.0", 1))

	// On all three: nothing to report, and the log says so once.
	s.advance(time.Second)
	if got := s.engine.ProxyStatus().NetworksMissing; len(got) != 0 {
		t.Fatalf("networks missing = %v, want none", got)
	}

	// The proxy came back on the application network alone, as Compose
	// leaves it after a refused start. Not seen until the next look.
	s.rt.SetNetworks(dockertest.ProxyID, dockertest.Network)
	s.advance(time.Second)
	if got := s.engine.ProxyStatus().NetworksMissing; len(got) != 0 {
		t.Fatalf("networks missing = %v before the next look is due", got)
	}
	s.advance(proxyNetworksEvery)
	want := []string{docker.ServicesNetwork(dockertest.Network), docker.ControlNetwork(dockertest.Network)}
	if got := s.engine.ProxyStatus().NetworksMissing; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("networks missing = %v, want %v", got, want)
	}
	if n := strings.Count(logs.String(), "not on all of its networks"); n != 1 {
		t.Errorf("warned %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "force-recreate caddy") {
		t.Errorf("the warning does not say what to do:\n%s", logs.String())
	}
	// Still missing at the next look: nothing more in the log.
	s.advance(proxyNetworksEvery)
	if n := strings.Count(logs.String(), "not on all of its networks"); n != 1 {
		t.Errorf("warned %d times for one unchanged answer", n)
	}

	// Made again, on all three.
	s.rt.SetNetworks(dockertest.ProxyID, dockertest.InstallationNetworks()...)
	s.advance(proxyNetworksEvery)
	if got := s.engine.ProxyStatus().NetworksMissing; len(got) != 0 {
		t.Errorf("networks missing = %v after the proxy was made again", got)
	}
	if !strings.Contains(logs.String(), "on all of its networks\"") && !strings.Contains(logs.String(), "is on all of its networks") {
		t.Errorf("recovery not logged:\n%s", logs.String())
	}
}

func TestAProxyOutsideDockerHasNoNetworksToMiss(t *testing.T) {
	s, _ := newRouted(t)
	s.rt.ProxyErr = docker.ErrNoProxyContainer
	s.rt.SetNetworks(dockertest.ProxyID, dockertest.Network)
	s.deploy(web("web:1.0", 1))
	s.advance(time.Second)
	if got := s.engine.ProxyStatus().NetworksMissing; len(got) != 0 {
		t.Errorf("networks missing = %v for a proxy that is no container", got)
	}
}

func TestGetServerCarriesTheProxysNetworksAndTheKernelsLimitOnlyWhenThereIsSomethingToSay(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 1))
	s.advance(time.Second)

	marshal := func() string {
		info, err := s.engine.Server(context.Background())
		if err != nil {
			t.Fatalf("Server: %v", err)
		}
		data, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := marshal(); strings.Contains(got, "networks_missing") || strings.Contains(got, "kernel_limits") {
		t.Errorf("nothing to say, yet: %s", got)
	}

	s.rt.SetNetworks(dockertest.ProxyID, dockertest.Network, docker.ServicesNetwork(dockertest.Network))
	s.advance(proxyNetworksEvery)
	s.engine.opts.NeighbourTable = func() (int, bool) { return 1024, true }
	got := marshal()
	if !strings.Contains(got, `"networks_missing":["shipwick-control"]`) {
		t.Errorf("GET /server lacks the missing network: %s", got)
	}
	if !strings.Contains(got, `"kernel_limits":{"neighbour_table":1024}`) {
		t.Errorf("GET /server lacks the kernel's limit: %s", got)
	}
	var back api.Server
	if err := json.Unmarshal([]byte(got), &back); err != nil || back.KernelLimits == nil || back.KernelLimits.NeighbourTable != 1024 {
		t.Errorf("round trip: %+v, %v", back.KernelLimits, err)
	}

	// A kernel that cannot be asked — the agent in a container — says nothing.
	s.engine.opts.NeighbourTable = func() (int, bool) { return 0, false }
	if got := marshal(); strings.Contains(got, "kernel_limits") {
		t.Errorf("an unreadable limit is reported: %s", got)
	}
}
