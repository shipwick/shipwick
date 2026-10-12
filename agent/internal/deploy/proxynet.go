package deploy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// The proxy is on three networks: the two the replicas are on, and the one
// the agent's API is on. A proxy container whose start was refused once —
// rootless Docker refusing port 80 — is left by Compose on some of them, and
// `docker compose up -d` starts it that way: it reaches some applications
// and not others, or none of them and not the agent, and nothing in Caddy's
// own log says so. The agent looks at the container's networks every so
// often and says what is missing, in GET /server and in its log.

// proxyNetworksEvery is how often the proxy's networks are looked at. The
// answer is in the list of containers the daemon gives for one call, and
// changes only when the container is made again.
const proxyNetworksEvery = 30 * time.Second

// proxyNetworks is the optional Runtime interface that says which of the
// installation's networks the proxy's container is not on. A runtime
// without it has nothing to say.
type proxyNetworks interface {
	MissingProxyNetworks(ctx context.Context) ([]string, error)
}

// proxyNetworkWatch is the last look at the proxy's networks.
type proxyNetworkWatch struct {
	mu        sync.Mutex
	checkedAt time.Time
	missing   []string
	// said is what the log was last told, so that it is told again only
	// when the answer changes.
	said string
}

// checkProxyNetworks looks at the proxy's networks when a look is due. Time
// is a parameter so that tests can drive it.
func (e *Engine) checkProxyNetworks(ctx context.Context, now time.Time) {
	rt, ok := e.rt.(proxyNetworks)
	if !ok || e.opts.Proxy == nil {
		return
	}
	w := &e.proxyNets
	w.mu.Lock()
	due := w.checkedAt.IsZero() || now.Sub(w.checkedAt) >= proxyNetworksEvery
	if due {
		w.checkedAt = now
	}
	w.mu.Unlock()
	if !due {
		return
	}

	missing, err := rt.MissingProxyNetworks(ctx)
	switch {
	case errors.Is(err, docker.ErrNoProxyContainer):
		// A proxy outside Docker has no networks to be missing from.
		missing = nil
	case err != nil:
		// The daemon could not be asked, or the proxy is not running: the
		// proxy's own status says so, and the last answer stands.
		return
	}

	w.mu.Lock()
	w.missing = missing
	say := strings.Join(missing, ", ")
	changed := say != w.said
	w.said = say
	w.mu.Unlock()
	if !changed {
		return
	}
	if len(missing) == 0 {
		e.log.Info("the proxy is on all of its networks")
		return
	}
	e.log.Warn("the proxy's container is not on all of its networks and serves some applications and not others, or cannot reach the agent; make it again: cd /opt/shipwick && docker compose up -d --force-recreate caddy",
		"missing", say)
}

// missingProxyNetworks is what the last look found, for the API.
func (e *Engine) missingProxyNetworks() []string {
	e.proxyNets.mu.Lock()
	defer e.proxyNets.mu.Unlock()
	return append([]string{}, e.proxyNets.missing...)
}
