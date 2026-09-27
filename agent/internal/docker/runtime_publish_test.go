package docker

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func TestConfigurePublish(t *testing.T) {
	cfg, host := &container.Config{}, &container.HostConfig{}
	configurePublish(ContainerSpec{}, cfg, host)
	if cfg.ExposedPorts != nil || host.PortBindings != nil {
		t.Errorf("an application without publish must bind nothing: %v %v", cfg.ExposedPorts, host.PortBindings)
	}

	configurePublish(ContainerSpec{Publish: []PortBinding{
		{Port: 5432, HostPort: 15432, Address: "10.0.0.5", Protocol: "tcp"},
		{Port: 5432, HostPort: 5432, Protocol: "tcp"},
		{Port: 5353, HostPort: 5353, Protocol: "udp"},
	}}, cfg, host)

	tcp, udp := network.MustParsePort("5432/tcp"), network.MustParsePort("5353/udp")
	if _, ok := cfg.ExposedPorts[tcp]; !ok || len(cfg.ExposedPorts) != 2 {
		t.Errorf("exposed ports = %v, want 5432/tcp and 5353/udp", cfg.ExposedPorts)
	}
	if _, ok := cfg.ExposedPorts[udp]; !ok {
		t.Errorf("exposed ports = %v, want 5353/udp", cfg.ExposedPorts)
	}

	// One container port may be published twice, on different addresses.
	got := host.PortBindings[tcp]
	if len(got) != 2 || got[0].HostPort != "15432" || got[0].HostIP != netip.MustParseAddr("10.0.0.5") {
		t.Errorf("5432/tcp bindings = %+v", got)
	}
	// No address means every address, which Docker spells as the zero value.
	if len(got) == 2 && (got[1].HostPort != "5432" || got[1].HostIP.IsValid()) {
		t.Errorf("5432/tcp second binding = %+v, want host port 5432 on every address", got[1])
	}
	if got := host.PortBindings[udp]; len(got) != 1 || got[0].HostPort != "5353" || got[0].HostIP.IsValid() {
		t.Errorf("5353/udp bindings = %+v", got)
	}
}
