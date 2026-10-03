package deploy

import (
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

// NetworkOptions say how the agent was set up to reach what is outside the
// server. The engine only reports them: the clients that leave the server
// are built where they are used, from pkg/outbound.
type NetworkOptions struct {
	// Proxy is the proxy of the agent's own requests, host:port.
	Proxy string
	// CAFile is true when authorities of the operator's own are trusted.
	CAFile bool
	// DNSResolvers are the name servers the routing gate asks, or "system";
	// empty means the public ones.
	DNSResolvers []string
	// ACMEDirectory is where the proxy obtains certificates, when that is
	// not Let's Encrypt.
	ACMEDirectory string
}

// networkStatus puts what the agent was told next to what the Docker daemon
// says about itself: a proxy on one side and none on the other is the usual
// reason an image cannot be pulled behind one.
func (e *Engine) networkStatus(info docker.Info) *api.NetworkStatus {
	resolvers := e.opts.Network.DNSResolvers
	if resolvers == nil {
		resolvers = []string{}
	}
	return &api.NetworkStatus{
		Proxy:         e.opts.Network.Proxy,
		DockerProxy:   info.Proxy,
		CAFile:        e.opts.Network.CAFile,
		DNSResolvers:  resolvers,
		ACMEDirectory: e.opts.Network.ACMEDirectory,
	}
}
