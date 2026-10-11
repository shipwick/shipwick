package dockertest

import (
	"context"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// Network is the name of the fake installation's application network; the
// services and control networks are derived from it as the runtime derives
// them.
const Network = "shipwick"

// InstallationNetworks lists the three networks of the fake installation.
func InstallationNetworks() []string {
	return []string{Network, docker.ServicesNetwork(Network), docker.ControlNetwork(Network)}
}

// SetNetworks says which networks a container is attached to. A container
// nobody said anything about is on every network of the installation.
func (f *Fake) SetNetworks(id string, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[id] = append([]string(nil), names...)
}

// MissingProxyNetworks is the runtime's: the installation's networks the
// proxy is not on.
func (f *Fake) MissingProxyNetworks(ctx context.Context) ([]string, error) {
	if f.ProxyErr != nil {
		return nil, f.ProxyErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	on, said := f.networks[ProxyID]
	if !said {
		return nil, nil
	}
	var missing []string
	for _, name := range InstallationNetworks() {
		found := false
		for _, n := range on {
			found = found || n == name
		}
		if !found {
			missing = append(missing, name)
		}
	}
	return missing, nil
}
