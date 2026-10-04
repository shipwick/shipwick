package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"strconv"

	"github.com/shipwick/shipwick/agent/internal/api"
	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/docker"
)

// apiReach is where the API listens and what follows from it.
type apiReach struct {
	listeners []net.Listener
	// upstream is where the proxy reaches the API when that is not what
	// ownAddress would say: the address on the control network.
	upstream string
	guard    api.Reach
}

// openAPI opens the API's listeners.
//
// In a container that is on the control network, the API listens on its
// address there and on loopback instead of on every address: an application's
// container shares only the application network with the agent, and nothing
// listens on that one. An agent that is a process of the host listens where
// it was told, and for the proxy on the address the server has on the control
// network, which the agent creates.
//
// Where none of that holds — a compose file without the control network, a
// published port that Docker forwards through the application network — the
// API listens as it always did, and says that applications can reach it.
func openAPI(ctx context.Context, rt *docker.Runtime, cfg config.Config, log *slog.Logger) (apiReach, error) {
	host, portText, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return apiReach{}, fmt.Errorf("%s: %w", config.EnvListenAddr, err)
	}
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host)
	everywhere := host == "" || (ip != nil && ip.IsUnspecified())
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())

	out := apiReach{guard: api.Reach{Origins: func(ctx context.Context) (api.Origins, error) {
		o, err := rt.Origins(ctx)
		return api.Origins{Subnets: o.Subnets, Containers: o.Containers}, err
	}}}
	listen := func(addr string) error {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", addr, err)
		}
		out.listeners = append(out.listeners, l)
		return nil
	}

	control := docker.ControlNetwork(cfg.Network)
	inContainer, err := rt.InContainer(ctx)
	if err != nil {
		log.Warn("could not look at the agent's own container", "error", err)
	}
	if !inContainer && runtime.GOOS == "linux" {
		if err := rt.EnsureControl(ctx); err != nil {
			log.Warn("could not create the control network; the proxy and the dashboard cannot join it", "network", control, "error", err)
		}
	}
	reach, err := rt.Reach(ctx, port)
	if err != nil {
		log.Warn("could not look up the control network", "network", control, "error", err)
	}

	switch {
	case inContainer && everywhere && reach.Control.IsValid() && !reach.PortElsewhere:
		at := netip.AddrPortFrom(reach.Control, uint16(port)).String()
		if err := listen(at); err != nil {
			return apiReach{}, err
		}
		if err := listen(net.JoinHostPort("127.0.0.1", portText)); err != nil {
			return apiReach{}, err
		}
		out.upstream = at
		out.guard.Control, out.guard.ControlSubnets, out.guard.Closed = reach.Control, reach.ControlSubnets, true
		log.Info("the API listens on the control network only; application containers have no way to it", "network", control, "addr", at)
		return out, nil

	case inContainer:
		if err := listen(cfg.ListenAddr); err != nil {
			return apiReach{}, err
		}
		out.guard.Open = !loopback
		switch {
		case !out.guard.Open:
		case reach.PortElsewhere:
			log.Warn("application containers can reach the API: its port is published, and this Docker forwards it through the application network. Docker 28 or later keeps it on the control network",
				"network", control)
		case reach.Control.IsValid():
			log.Warn("application containers can reach the API: "+config.EnvListenAddr+" names an address of its own. Leave it unset to listen on the control network only",
				"network", control, "addr", cfg.ListenAddr)
		default:
			log.Warn("application containers can reach the API: the agent's container is not on the control network. Run the installer again, or add the network to your compose file (handbook, Security)",
				"network", control)
		}
		return out, nil
	}

	if err := listen(cfg.ListenAddr); err != nil {
		return apiReach{}, err
	}
	out.guard.Open = !loopback
	if runtime.GOOS != "linux" || !reach.Control.IsValid() {
		return out, nil
	}
	// The dashboard is on the control network alone and the proxy is sent
	// there, so an address of an application network is an application's.
	out.guard.Closed, out.guard.Open = true, false
	out.guard.Control, out.guard.ControlSubnets = reach.Control, reach.ControlSubnets
	if cfg.AgentDomain == "" {
		return out, nil
	}
	// The proxy is a container: it reaches a process of the host at the
	// address the host has on a network they share. Sent anywhere else it
	// would call from whichever of its networks has its default route, an
	// application network.
	at := netip.AddrPortFrom(reach.Control, uint16(port)).String()
	if !everywhere && host != reach.Control.String() {
		if err := listen(at); err != nil {
			log.Warn("could not listen on the control network; the proxy reaches the API where "+config.EnvListenAddr+" says", "network", control, "error", err)
			out.guard.Closed, out.guard.Open = false, !loopback
			return out, nil
		}
	}
	out.upstream = at
	log.Info("the proxy reaches the API on the control network", "network", control, "addr", at)
	return out, nil
}
