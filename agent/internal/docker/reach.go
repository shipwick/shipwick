package docker

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ControlNetwork names the third network of an installation: the one the
// agent's API is reached on. The agent, the proxy and the dashboard are on
// it and no application is. An application's container shares the main
// network with the agent, which probes it there, and on a shared bridge a
// caller's address proves nothing: a container can answer for any address of
// the network it is on. So the API is kept off that network instead of
// guarded on it.
func ControlNetwork(network string) string { return network + "-control" }

// selfGwPriority is the gateway priority of the agent's own attachment to the
// application network. Docker forwards a published port to the address a
// container has on the network that provides its default gateway, and picks
// that network by priority and then by name, where the application network
// would win. The agent listens on the control network only, so its port has
// to arrive there. Daemons before 28 ignore the field.
const selfGwPriority = -1

// Reach is where the agent stands on the control network.
type Reach struct {
	// InContainer is true when the agent is a container of this daemon.
	InContainer bool
	// Control is the agent's address on the control network when it is a
	// container attached to it, and the server's address there, the bridge's
	// own, when the agent is a process of the host. Invalid when there is none.
	Control netip.Addr
	// ControlSubnets are the addresses of the control network.
	ControlSubnets []netip.Prefix
	// PortElsewhere is true when the API's port is published on the server
	// and Docker forwards it to an address of the agent outside the control
	// network: listening on the control network alone would cut it off.
	PortElsewhere bool
}

// Origins is where the containers of applications call from.
type Origins struct {
	// Subnets are the addresses of the two networks applications are on.
	Subnets []netip.Prefix
	// Containers are the addresses of the containers the agent manages, on
	// every network they are attached to.
	Containers []netip.Addr
}

// EnsureControl creates the control network if it does not exist yet. That is
// for an agent that runs as a process of the host, whose compose file joins
// the networks the agent made; in a container the network is the compose
// file's to create, and one made here would not be accepted as its own.
func (r *Runtime) EnsureControl(ctx context.Context) error {
	return r.ensureNetwork(ctx, ControlNetwork(r.network))
}

// Reach looks up where the API can be reached without being reachable from
// applications. port is the port the API listens on.
func (r *Runtime) Reach(ctx context.Context, port int) (Reach, error) {
	control := ControlNetwork(r.network)
	self, err := r.self(ctx)
	if err != nil {
		return Reach{}, err
	}
	if self == nil {
		res, err := r.cli.NetworkInspect(ctx, control, client.NetworkInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			return Reach{}, nil
		} else if err != nil {
			return Reach{}, fmt.Errorf("inspect network %s: %w", control, err)
		}
		out := Reach{ControlSubnets: subnetsOf(res.Network.IPAM)}
		for _, c := range res.Network.IPAM.Config {
			if c.Gateway.Is4() {
				out.Control = c.Gateway
				break
			}
		}
		return out, nil
	}

	out := Reach{InContainer: true}
	if self.NetworkSettings == nil {
		return out, nil
	}
	ep := self.NetworkSettings.Networks[control]
	if ep == nil || !ep.IPAddress.IsValid() {
		return out, nil
	}
	res, err := r.cli.NetworkInspect(ctx, control, client.NetworkInspectOptions{})
	if err != nil {
		return Reach{}, fmt.Errorf("inspect network %s: %w", control, err)
	}
	out.Control, out.ControlSubnets = ep.IPAddress.Unmap(), subnetsOf(res.Network.IPAM)
	if self.HostConfig != nil && published(self.HostConfig.PortBindings, port) {
		out.PortElsewhere = gatewayNetwork(self.NetworkSettings.Networks) != control
	}
	return out, nil
}

// Origins lists the addresses applications call from: the subnets of the
// application networks, and the addresses of every managed container.
func (r *Runtime) Origins(ctx context.Context) (Origins, error) {
	var out Origins
	for _, name := range []string{r.network, r.services} {
		res, err := r.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if err != nil {
			return Origins{}, fmt.Errorf("inspect network %s: %w", name, err)
		}
		out.Subnets = append(out.Subnets, subnetsOf(res.Network.IPAM)...)
	}
	filters := client.Filters{}
	filters.Add("label", LabelManaged+"=true")
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{Filters: filters})
	if err != nil {
		return Origins{}, fmt.Errorf("list containers: %w", err)
	}
	for _, item := range res.Items {
		if item.NetworkSettings == nil {
			continue
		}
		for _, ep := range item.NetworkSettings.Networks {
			if ep != nil && ep.IPAddress.IsValid() {
				out.Containers = append(out.Containers, ep.IPAddress.Unmap())
			}
		}
	}
	return out, nil
}

// self inspects the container the agent runs in; nil, without error, when it
// does not run in one this daemon knows.
func (r *Runtime) self(ctx context.Context) (*container.InspectResponse, error) {
	// Inside a container the hostname is, unless overridden, the short
	// container ID.
	hostname, err := os.Hostname()
	if err != nil || len(hostname) < 12 {
		return nil, nil
	}
	res, err := r.cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect own container: %w", err)
	}
	// A name lookup can match by coincidence; an ID prefix cannot.
	if !strings.HasPrefix(res.Container.ID, hostname) {
		return nil, nil
	}
	return &res.Container, nil
}

func subnetsOf(ipam network.IPAM) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range ipam.Config {
		if c.Subnet.IsValid() {
			out = append(out, c.Subnet.Masked())
		}
	}
	return out
}

// published reports whether the container's port is bound to a port of the
// server.
func published(bindings network.PortMap, port int) bool {
	for p, to := range bindings {
		if int(p.Num()) != port || p.Proto() != network.TCP {
			continue
		}
		for _, b := range to {
			if n, err := strconv.Atoi(b.HostPort); err == nil && n > 0 {
				return true
			}
		}
	}
	return false
}

// gatewayNetwork is the network Docker takes a container's default gateway
// from, and forwards its published ports to: the highest priority, and among
// equals the name that sorts first.
func gatewayNetwork(networks map[string]*network.EndpointSettings) string {
	names := make([]string, 0, len(networks))
	for name, ep := range networks {
		if ep != nil {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := networks[names[i]], networks[names[j]]
		if a.GwPriority != b.GwPriority {
			return a.GwPriority > b.GwPriority
		}
		return names[i] < names[j]
	})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// InContainer reports whether the agent is a container of this daemon.
func (r *Runtime) InContainer(ctx context.Context) (bool, error) {
	self, err := r.self(ctx)
	return self != nil, err
}
