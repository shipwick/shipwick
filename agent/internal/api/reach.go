package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// The API answers the proxy, the dashboard and the server itself. An
// application's container is none of them, and whatever token it presents
// was either taken from somewhere it should not be or is a guess: it is
// refused before the token is looked at, so that its attempts are not counted
// against the address it borrowed.
//
// Where the agent listens on the control network (see docker.ControlNetwork)
// that is a statement about the network: no application is on it, and on the
// address the agent has there it answers the addresses of that network and no
// others. Elsewhere it is a statement about addresses, which is weaker: on a
// bridge it shares with applications the agent can refuse the containers it
// knows, and a container that claims the address of something else gets
// through to the token check. GET /server says so (Server.OpenToApplications).

// Origins is where the containers of applications call from.
type Origins struct {
	// Subnets are the addresses of the networks applications are on.
	Subnets []netip.Prefix
	// Containers are the addresses of the containers the agent manages.
	Containers []netip.Addr
}

// Reach is what the server knows about who can reach it.
type Reach struct {
	// Control is the address the API listens on in the control network;
	// ControlSubnets are the addresses of that network. Requests that arrive
	// at Control are answered for those and refused for every other.
	Control        netip.Addr
	ControlSubnets []netip.Prefix
	// Closed is true when everything that may call the API from a container
	// does so from the control network: any address of an application network
	// is then refused, whoever holds it.
	Closed bool
	// Open is true when application containers have a way to the API that
	// the agent cannot close: it is what GET /server reports.
	Open bool
	// Origins asks the runtime where applications call from. It is called at
	// most once every originsFor, and never for requests from loopback or
	// from the control network.
	Origins func(ctx context.Context) (Origins, error)
}

// originsFor is how long an answer of Reach.Origins is used. A container
// that starts is known within it; until then its requests are held to their
// token like anyone's.
const originsFor = 2 * time.Second

// originsTimeout bounds the question to the runtime: a request must not wait
// on a Docker daemon that does not answer.
const originsTimeout = 3 * time.Second

type reach struct {
	Reach

	mu         sync.Mutex
	asked      time.Time
	asking     bool
	subnets    []netip.Prefix
	containers map[netip.Addr]struct{}
}

// UseReach tells the server who can reach it. Call it before the server takes
// requests. Without it nobody is refused for where they call from.
func (s *Server) UseReach(r Reach) {
	s.reach = &reach{Reach: r}
}

// refuseApplications answers a request from an application's container with
// 403 APPLICATION_CALLER, the unauthenticated endpoints included.
func (s *Server) refuseApplications(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.reach != nil && s.reach.refuses(r, s.now(), s.warnOrigins) {
			writeError(w, http.StatusForbidden, api.CodeApplicationCaller,
				"the API answers the proxy, the dashboard and the server itself, not the containers of applications; from a container, call it at its hostname", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) warnOrigins(err error) {
	s.log.Warn("could not ask Docker which addresses belong to applications; the ones last known are refused", "error", err)
}

func (g *reach) refuses(r *http.Request, now time.Time, warn func(error)) bool {
	remote, err := netip.ParseAddr(clientAddress(r.RemoteAddr))
	if err != nil {
		return false
	}
	remote = remote.Unmap().WithZone("")
	if remote.IsLoopback() {
		return false
	}
	if g.Control.IsValid() {
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && local != nil {
			if at, ok := netip.AddrFromSlice(local.IP); ok && at.Unmap() == g.Control {
				return !within(g.ControlSubnets, remote)
			}
		}
	}
	if g.Origins == nil {
		return false
	}
	subnets, containers := g.origins(r.Context(), now, warn)
	if _, managed := containers[remote]; managed {
		return true
	}
	return g.Closed && within(subnets, remote)
}

// origins returns the last answer of Reach.Origins, asking again when it is
// older than originsFor. An answer that cannot be had leaves the last one in
// place: a daemon that is down starts no containers either.
func (g *reach) origins(ctx context.Context, now time.Time, warn func(error)) ([]netip.Prefix, map[netip.Addr]struct{}) {
	g.mu.Lock()
	if g.asking || !g.asked.IsZero() && now.Sub(g.asked) < originsFor {
		defer g.mu.Unlock()
		return g.subnets, g.containers
	}
	// One request asks; the others go on with the last answer meanwhile, so
	// that a daemon slow to answer holds up one request and not all of them.
	g.asking, g.asked = true, now
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), originsTimeout)
	defer cancel()
	found, err := g.Origins(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.asking = false
	if err != nil {
		warn(err)
		return g.subnets, g.containers
	}
	containers := make(map[netip.Addr]struct{}, len(found.Containers))
	for _, a := range found.Containers {
		containers[a.Unmap()] = struct{}{}
	}
	g.subnets, g.containers = slices.Clone(found.Subnets), containers
	return g.subnets, g.containers
}

func within(subnets []netip.Prefix, a netip.Addr) bool {
	for _, p := range subnets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
