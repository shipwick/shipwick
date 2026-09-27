package docker

import (
	"net/netip"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// configurePublish binds the published container ports on the host: exactly
// the ones deploy.yaml lists, on the address it names or on every address.
// Almost every application publishes none, and the proxy reaches its replicas
// over the network.
func configurePublish(spec ContainerSpec, cfg *container.Config, host *container.HostConfig) {
	if len(spec.Publish) == 0 {
		return
	}
	cfg.ExposedPorts = network.PortSet{}
	host.PortBindings = network.PortMap{}
	for _, p := range spec.Publish {
		port, ok := network.PortFrom(uint16(p.Port), network.IPProtocol(p.Protocol))
		if !ok {
			continue
		}
		binding := network.PortBinding{HostPort: strconv.Itoa(p.HostPort)}
		if p.Address != "" {
			addr, err := netip.ParseAddr(p.Address)
			if err != nil {
				// pkg/spec has validated it; whatever slipped through must
				// not end up bound on every address.
				continue
			}
			binding.HostIP = addr
		}
		cfg.ExposedPorts[port] = struct{}{}
		host.PortBindings[port] = append(host.PortBindings[port], binding)
	}
}
