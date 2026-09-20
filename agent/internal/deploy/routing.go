package deploy

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// Proxy is what the engine needs from the reverse proxy. *proxy.Caddy is the
// production implementation; a nil Proxy means routing is disabled.
type Proxy interface {
	// Sync makes the proxy serve exactly routes. It must be cheap when
	// nothing changed: the supervisor calls it every tick.
	Sync(ctx context.Context, routes []proxy.Route) error
	Status() proxy.Status
}

// DomainConflictError is returned when a deployment claims a domain that
// something else already serves.
type DomainConflictError struct {
	Domain string
	Owner  string // an application, or Shipwick itself
}

func (e *DomainConflictError) Error() string {
	return fmt.Sprintf("domain %s is already served by %s", e.Domain, e.Owner)
}

// checkDomain refuses a domain that is already taken. Two routes for one
// hostname would silently send all traffic to whichever sorts first.
func (e *Engine) checkDomain(ctx context.Context, appName, domain string) error {
	if domain == "" {
		return nil
	}
	for _, r := range e.opts.ExtraRoutes {
		if r.Domain == domain {
			return &DomainConflictError{Domain: domain, Owner: "Shipwick itself (the agent or the dashboard)"}
		}
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return err
	}
	for _, other := range apps {
		if other.Name == appName || other.ActiveDeploymentID == nil {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *other.ActiveDeploymentID)
		if err != nil {
			return err
		}
		if d.Spec.Domain == domain {
			return &DomainConflictError{Domain: domain, Owner: fmt.Sprintf("application %q", other.Name)}
		}
	}
	return nil
}

// routeOverride names exactly who serves an application while a rollout is in
// progress: a mix of old and new replicas that no single deployment record
// describes.
type routeOverride struct {
	domain  string
	members []routeMember
	// desired is how many replicas should be serving while the rollout runs:
	// the smaller of the old and the new count, which a rollout never goes below.
	desired int
}

type routeMember struct {
	replica store.Replica
	port    int // old and new version need not listen on the same one
}

// routeVia puts a rollout in charge of the application's routing until it
// commits or aborts. Without it, the database would be the source of truth —
// and the database keeps naming the old deployment until the commit, so the
// supervisor's next tick would route traffic straight back to replicas the
// rollout has just retired.
func (e *Engine) routeVia(app string, o routeOverride) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.routeOverrides[app] = o
}

func (e *Engine) clearRouteOverride(app string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.routeOverrides, app)
}

// SyncProxy brings the reverse proxy in line with the current state: every
// application with a domain is routed to the replicas of its serving
// deployment that are ready for traffic. With no proxy configured it is a no-op.
func (e *Engine) SyncProxy(ctx context.Context) error {
	if e.opts.Proxy == nil {
		return nil
	}
	routes, err := e.desiredRoutes(ctx)
	if err != nil {
		return fmt.Errorf("compute routes: %w", err)
	}
	return e.opts.Proxy.Sync(ctx, routes)
}

func (e *Engine) desiredRoutes(ctx context.Context) ([]proxy.Route, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]docker.Container, len(containers))
	for _, c := range containers {
		byID[c.ID] = c
	}

	e.mu.Lock()
	overrides := make(map[string]routeOverride, len(e.routeOverrides))
	for k, v := range e.routeOverrides {
		overrides[k] = v
	}
	e.mu.Unlock()

	routes := append([]proxy.Route(nil), e.opts.ExtraRoutes...)
	taken := map[string]bool{}
	for _, r := range routes {
		taken[r.Domain] = true
	}

	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	for _, app := range apps {
		var domain string
		var members []routeMember

		if o, overridden := overrides[app.Name]; overridden {
			// A rollout is in charge and says exactly who serves.
			domain, members = o.domain, o.members
		} else {
			if app.ActiveDeploymentID == nil {
				continue
			}
			d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
			if err != nil {
				return nil, err
			}
			domain = d.Spec.Domain
			// A stopped application keeps its route — and with it its
			// certificate — but has no upstreams: the proxy answers 503.
			if domain != "" && app.DesiredState == api.DesiredRunning {
				replicas, err := e.store.ListReplicas(ctx, d.ID)
				if err != nil {
					return nil, err
				}
				for _, r := range replicas {
					members = append(members, routeMember{replica: r, port: d.Spec.Port})
				}
			}
		}

		// checkDomain prevents duplicates; this keeps a config sane even if
		// one slipped in (e.g. two first deployments racing for a domain).
		if domain == "" || taken[domain] {
			continue
		}
		taken[domain] = true

		route := proxy.Route{Domain: domain}
		for _, m := range members {
			c, exists := byID[m.replica.ContainerID]
			health, _ := e.sup.snapshot(m.replica.ContainerID)
			if exists && isReady(c.Running, health) {
				// By name, not IP: a restarted container may come back with
				// another address, and Docker's DNS always knows.
				route.Upstreams = append(route.Upstreams, net.JoinHostPort(m.replica.ContainerName, strconv.Itoa(m.port)))
			}
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// ProxyStatus reports the proxy's state for the API.
func (e *Engine) ProxyStatus() proxy.Status {
	if e.opts.Proxy == nil {
		return proxy.Status{}
	}
	return e.opts.Proxy.Status()
}

// serving reports who serves the application while a rollout is in charge of
// it. Views use it for the same reason routing does: the active deployment's
// own replicas are being retired one by one, and counting only those would
// show an application as degraded at the very moment it is being upgraded
// with full capacity.
func (e *Engine) serving(app string) (routeOverride, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o, ok := e.routeOverrides[app]
	return o, ok
}
