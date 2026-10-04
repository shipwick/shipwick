// Package upstreams is Shipwick's addition to its Caddy: the source the
// reverse proxy asks for an application's replicas.
//
// Caddy's own source for A records asks the name again whenever its answer
// is older than the refresh interval, holds one lock for every name while
// the question is out, and asks it on behalf of the request that happened
// to come first. One lost answer from Docker's DNS therefore holds every
// request to every application for as long as the resolver waits, five
// seconds, and a request that is abandoned takes the question with it.
//
// This source asks in the background, per name, and keeps the last answer
// while a question goes unanswered: a replica that was there a second ago
// is still there, and one that is not refuses the connection, which the
// proxy already retries elsewhere.
package upstreams

import (
	"net"
	"net/http"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(Replicas{})
}

// names is shared by every configuration the process loads: a reload must
// not forget who stands behind a name.
var names = newTable()

// Replicas provides the replicas of one application's port from the A
// records of a name on Docker's network.
type Replicas struct {
	// The name the replicas carry on the network.
	Name string `json:"name,omitempty"`

	// The port they listen on.
	Port string `json:"port,omitempty"`

	// How long an answer is used before the name is asked again. Default: 1s
	Refresh caddy.Duration `json:"refresh,omitempty"`

	// How long a request waits for a question that was just asked before it
	// uses the last answer. Default: 200ms
	Wait caddy.Duration `json:"wait,omitempty"`

	// How long the last answer is used while questions go unanswered.
	// Default: 2s
	Keep caddy.Duration `json:"keep,omitempty"`

	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Replicas) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.reverse_proxy.upstreams.shipwick",
		New: func() caddy.Module { return new(Replicas) },
	}
}

// Provision sets the defaults.
func (r *Replicas) Provision(ctx caddy.Context) error {
	r.logger = ctx.Logger()
	if r.Port == "" {
		r.Port = "80"
	}
	if r.Refresh <= 0 {
		r.Refresh = caddy.Duration(time.Second)
	}
	if r.Wait <= 0 {
		r.Wait = caddy.Duration(200 * time.Millisecond)
	}
	if r.Keep <= 0 {
		r.Keep = caddy.Duration(2 * time.Second)
	}
	logger := r.logger
	names.setChanged(func(name string, err error) {
		if err != nil {
			logger.Warn("the name is not being answered; its last answer is used meanwhile", zap.String("name", name), zap.Error(err))
			return
		}
		logger.Info("the name is answered again", zap.String("name", name))
	})
	return nil
}

// GetUpstreams returns the replicas behind the name.
func (r *Replicas) GetUpstreams(req *http.Request) ([]*reverseproxy.Upstream, error) {
	addrs, err := names.addresses(req.Context(), r.Name, settings{
		refresh: time.Duration(r.Refresh),
		wait:    time.Duration(r.Wait),
		keep:    time.Duration(r.Keep),
		timeout: lookupTimeout,
	})
	if err != nil {
		return nil, err
	}
	upstreams := make([]*reverseproxy.Upstream, len(addrs))
	for i, addr := range addrs {
		upstreams[i] = &reverseproxy.Upstream{Dial: net.JoinHostPort(addr, r.Port)}
	}
	return upstreams, nil
}

// lookupTimeout ends a question to Docker's DNS, which answers in
// milliseconds or not at all. The resolver's own patience is five seconds;
// this one ends before the last answer has been kept for too long, so that
// the next question is out by then.
const lookupTimeout = 800 * time.Millisecond

var (
	_ caddy.Provisioner           = (*Replicas)(nil)
	_ reverseproxy.UpstreamSource = (*Replicas)(nil)
)
