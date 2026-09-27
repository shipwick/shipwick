package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Proxy is what the engine needs from the reverse proxy. *proxy.Caddy is the
// production implementation; a nil Proxy means routing is disabled.
type Proxy interface {
	// Sync makes the proxy serve exactly routes. It must be cheap when
	// nothing changed: the supervisor calls it every tick.
	Sync(ctx context.Context, routes []proxy.Route) error
	Status() proxy.Status
}

// DomainConflictError is returned when a deployment claims a hostname that
// something else already serves — as its domain, an alias or a redirect.
type DomainConflictError struct {
	Domain string
	Field  string // where deploy.yaml claims it: "domain", "aliases[0]", "redirects[1]"
	Owner  string // an application, or Shipwick itself
}

func (e *DomainConflictError) Error() string {
	return fmt.Sprintf("%s: %s is already served by %s", e.Field, e.Domain, e.Owner)
}

// hostnames is everything one application answers to: its domain, the
// aliases served like it, and the redirects sent to it.
type hostnames struct {
	domain    string
	aliases   []string
	redirects []string
}

func hostnamesOf(a spec.App) hostnames {
	return hostnames{domain: a.Domain, aliases: a.Aliases, redirects: a.Redirects}
}

// all lists every hostname with the deploy.yaml field that names it.
func (h hostnames) all() []namedHost {
	var out []namedHost
	if h.domain != "" {
		out = append(out, namedHost{"domain", h.domain})
	}
	for i, a := range h.aliases {
		out = append(out, namedHost{fmt.Sprintf("aliases[%d]", i), a})
	}
	for i, r := range h.redirects {
		out = append(out, namedHost{fmt.Sprintf("redirects[%d]", i), r})
	}
	return out
}

type namedHost struct{ field, host string }

// list is every hostname, whatever its role.
func (h hostnames) list() []string {
	var out []string
	for _, n := range h.all() {
		out = append(out, n.host)
	}
	return out
}

func routeHostnames(r proxy.Route) []string {
	out := append([]string{r.Domain}, r.Aliases...)
	return append(out, r.Redirects...)
}

// checkDomain refuses a hostname that is already taken, whatever role it has
// on either side. Two routes for one hostname would silently send all traffic
// to whichever sorts first.
func (e *Engine) checkDomain(ctx context.Context, app spec.App) error {
	claims := hostnamesOf(app).all()
	if len(claims) == 0 {
		return nil
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return err
	}
	owners := map[string]string{}
	for _, other := range apps {
		if other.Name == app.Name || other.ActiveDeploymentID == nil {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *other.ActiveDeploymentID)
		if err != nil {
			return err
		}
		for _, h := range hostnamesOf(d.Spec).all() {
			owners[h.host] = fmt.Sprintf("application %q", other.Name)
		}
	}
	for _, r := range e.opts.ExtraRoutes {
		for _, h := range routeHostnames(r) {
			owners[h] = "Shipwick itself (the agent or the dashboard)"
		}
	}
	for _, c := range claims {
		if owner := owners[c.host]; owner != "" {
			return &DomainConflictError{Domain: c.host, Field: c.field, Owner: owner}
		}
	}
	return nil
}

// serviceNames are the names a ready replica answers to on the services
// network: the application's own, which is what other applications call, and
// one that includes the port, which is what the proxy resolves — two versions
// of an application may listen on different ports while a rollout lasts, and
// the proxy has to dial each on its own.
//
// An application without a port cannot be called, and gets no names.
func serviceNames(app string, port int) []string {
	if port == 0 {
		return nil
	}
	return []string{app, backendName(app, port)}
}

// backendName cannot be an application's name: those have no underscore.
func backendName(app string, port int) string {
	return app + "_" + strconv.Itoa(port)
}

// routeOverride names exactly who serves an application while a rollout is in
// progress: a mix of old and new replicas that no single deployment record
// describes.
type routeOverride struct {
	hosts   hostnames
	members []routeMember
	// desired is how many replicas should be serving while the rollout runs:
	// the smaller of the old and the new count, which a rollout never goes below.
	desired int
	// staticRoot is the folder in the proxy that serves instead of replicas:
	// a static application (see static.go).
	staticRoot string
}

type routeMember struct {
	replica store.Replica
	port    int // old and new version need not listen on the same one
}

// routeVia puts a rollout in charge of the application's routing until it
// commits or aborts. Without it, the database would be the source of truth —
// and the database keeps naming the old deployment until the commit, so the
// supervisor's next tick would hand the application's name straight back to
// replicas the rollout has just retired.
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

// SyncProxy brings routing in line with the current state.
//
// Routing is two things. Who serves an application is decided by names on the
// services network: a replica carries its application's names exactly while it
// is ready for traffic, and Docker's DNS is what the proxy and other
// applications ask. That part works without a proxy, and for applications
// without a domain. What the proxy is told is only which name stands behind
// which domain — which does not change when replicas come and go, and that is
// the point: loading a proxy config resets connections that are being
// established at that instant.
func (e *Engine) SyncProxy(ctx context.Context) error {
	if e.opts.Proxy != nil {
		// DNS first, and before the lock: a lookup may take its whole timeout,
		// and nothing that renames replicas should wait for a resolver. The
		// plans are computed again under the lock; only the cache is read there.
		plans, err := e.routingPlans(ctx)
		if err != nil {
			return fmt.Errorf("compute routes: %w", err)
		}
		e.refreshHostnames(ctx, plannedHostnames(plans))
	}

	// One at a time. The supervisor syncs every tick and a rollout at every
	// step, and giving a replica its names is two calls to Docker — leave the
	// network, join it again. Two callers renaming the same replica would
	// collide in the middle; the second one here finds the work done.
	e.routing.Lock()
	defer e.routing.Unlock()

	plans, err := e.routingPlans(ctx)
	if err != nil {
		return fmt.Errorf("compute routes: %w", err)
	}
	containers, err := e.listContainers(ctx)
	if err != nil {
		return fmt.Errorf("compute routes: %w", err)
	}
	e.forgetNames(containers)

	var failures []error
	for _, p := range plans {
		for _, m := range p.members {
			c, exists := containers[m.replica.ContainerID]
			if !exists || !c.Running {
				// Docker's DNS does not answer for a stopped container, and
				// whoever starts it again takes its names first.
				continue
			}
			want := serviceNames(p.app, m.port)
			if health, _ := e.sup.snapshot(c.ID); !isReady(true, health) {
				want = nil
			}
			if err := e.setNames(ctx, c.ID, want); err != nil {
				failures = append(failures, fmt.Errorf("replica %s: %w", c.Name, err))
			}
		}
	}

	if e.opts.Proxy != nil {
		routes := e.routesFor(plans, containers)
		e.noteHeldBack(ctx, plans)
		// Every change here is a reload of the proxy, which is what routing by
		// name exists to avoid: worth a line in the log, with what changed.
		if now := describeRoutes(routes); now != e.lastRoutes {
			e.log.Info("proxy routes changed", "routes", now)
			e.lastRoutes = now
			e.explainUnserved(plans, containers, routes)
		}
		if err := e.opts.Proxy.Sync(ctx, routes); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// noteHeldBack keeps track of the hostnames the proxy is not told about
// because their DNS does not point here yet: a line in the log when the set
// changes, and an application event the moment one of them starts pointing
// here — the operator who fixed the record is waiting for exactly that. The
// caller holds e.routing.
func (e *Engine) noteHeldBack(ctx context.Context, plans []routingPlan) {
	held := map[string]string{}
	for _, p := range plans {
		for _, h := range p.hosts.all() {
			ready, why := e.hostnameReady(h.host)
			if !ready {
				held[h.host] = why
			} else if _, was := e.heldBack[h.host]; was {
				e.appEvent(ctx, store.Application{ID: p.appID, Name: p.app}, fmt.Sprintf("%s now points at this server and is being served", h.host))
			}
		}
	}
	e.heldBack = held
	if now := describeHeld(held); now != e.lastHeld {
		if now == "" {
			e.log.Info("every hostname points at this server")
		} else {
			e.log.Warn("hostnames waiting for DNS are not served yet", "hostnames", now)
		}
		e.lastHeld = now
	}
}

// describeHeld is a one-line summary for the log:
// "api.example.com: does not resolve yet; www.example.com: resolves to …".
func describeHeld(held map[string]string) string {
	parts := make([]string, 0, len(held))
	for host, why := range held {
		parts = append(parts, host+": "+why)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// explainUnserved says, for every application that should be served and is
// not, why each of its replicas does not count. It is for whoever reads the
// log after the 503s. An application whose domain is waiting for DNS is not
// served for a reason its replicas have nothing to do with; noteHeldBack has
// said so.
func (e *Engine) explainUnserved(plans []routingPlan, containers map[string]docker.Container, routes []proxy.Route) {
	served := map[string]bool{}
	for _, r := range routes {
		served[r.Domain] = len(r.Backends) > 0 || len(r.Upstreams) > 0 || r.StaticRoot != ""
	}
	for _, p := range plans {
		if p.hosts.domain == "" || served[p.hosts.domain] {
			continue
		}
		if ready, _ := e.hostnameReady(p.hosts.domain); !ready {
			continue
		}
		for _, m := range p.members {
			c, exists := containers[m.replica.ContainerID]
			health, _ := e.sup.snapshot(m.replica.ContainerID)
			e.log.Warn("replica is not in service", "app", p.app, "replica", m.replica.ContainerName,
				"exists", exists, "running", c.Running, "health", health, "names", e.namesOf(m.replica.ContainerID))
		}
	}
}

// describeRoutes is a one-line summary for the log:
// "web.example.com,api.example.com→web_8080 www.example.com→308".
func describeRoutes(routes []proxy.Route) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		var behind []string
		for _, b := range r.Backends {
			behind = append(behind, b.Name)
		}
		behind = append(behind, r.Upstreams...)
		if r.StaticRoot != "" {
			behind = append(behind, "files")
		}
		if len(behind) == 0 {
			behind = []string{"503"}
		}
		sort.Strings(behind)
		served := append([]string{r.Domain}, r.Aliases...)
		parts = append(parts, strings.Join(served, ",")+"→"+strings.Join(behind, "+"))
		for _, h := range r.Redirects {
			parts = append(parts, h+"→308")
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// routingPlan is who should be serving one application.
type routingPlan struct {
	app     string
	appID   int64
	hosts   hostnames
	members []routeMember
	// staticRoot serves in place of members: see routeOverride.
	staticRoot string
}

// plannedHostnames is every hostname the plans may ask the proxy to serve.
func plannedHostnames(plans []routingPlan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.hosts.list()...)
	}
	return out
}

func (e *Engine) listContainers(ctx context.Context) (map[string]docker.Container, error) {
	list, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return nil, err
	}
	containers := make(map[string]docker.Container, len(list))
	for _, c := range list {
		containers[c.ID] = c
	}
	return containers, nil
}

func (e *Engine) routingPlans(ctx context.Context) ([]routingPlan, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	overrides := make(map[string]routeOverride, len(e.routeOverrides))
	for k, v := range e.routeOverrides {
		overrides[k] = v
	}
	e.mu.Unlock()

	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	plans := make([]routingPlan, 0, len(apps))
	for _, app := range apps {
		plan := routingPlan{app: app.Name, appID: app.ID}
		if o, overridden := overrides[app.Name]; overridden {
			// A rollout is in charge and says exactly who serves.
			plan.hosts, plan.members, plan.staticRoot = o.hosts, o.members, o.staticRoot
		} else {
			if app.ActiveDeploymentID == nil {
				continue
			}
			d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
			if err != nil {
				return nil, err
			}
			plan.hosts = hostnamesOf(d.Spec)
			// A stopped application keeps its route — and with it its
			// certificate — but nobody serves it: the proxy answers 503.
			switch {
			case app.DesiredState != api.DesiredRunning:
			case d.StaticDigest != "":
				plan.staticRoot = staticDir(app.Name, d.StaticDigest)
			default:
				replicas, err := e.store.ListReplicas(ctx, d.ID)
				if err != nil {
					return nil, err
				}
				for _, r := range replicas {
					plan.members = append(plan.members, routeMember{replica: r, port: d.Spec.Port})
				}
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// routesFor is what the proxy is told: for every domain, the names that are
// behind it right now. A name is listed only while a running replica carries
// it — Docker's DNS forwards a name nobody carries to the outside resolvers,
// and every request would wait seconds for that to fail.
//
// A hostname whose DNS does not point here yet is left out (see dns.go). The
// ExtraRoutes are not: the agent's and the dashboard's hostnames come from the
// operator's configuration, not from a deploy.yaml, and holding them back
// could lock the operator out of the very agent that reports the problem.
func (e *Engine) routesFor(plans []routingPlan, containers map[string]docker.Container) []proxy.Route {
	routes := append([]proxy.Route(nil), e.opts.ExtraRoutes...)
	taken := map[string]bool{}
	for _, r := range routes {
		claim(taken, routeHostnames(r))
	}

	for _, p := range plans {
		// checkDomain prevents duplicates; this keeps a config sane even if
		// one slipped in (e.g. two first deployments racing for a domain).
		if p.hosts.domain == "" || taken[p.hosts.domain] {
			continue
		}
		taken[p.hosts.domain] = true
		// The whole route waits for the domain, aliases and redirects
		// included: the redirects' target is the domain, and an alias is
		// served "exactly like" a domain that is not served yet.
		if ready, _ := e.hostnameReady(p.hosts.domain); !ready {
			continue
		}

		route := proxy.Route{Domain: p.hosts.domain, Aliases: claim(taken, e.readyHosts(p.hosts.aliases)), Redirects: claim(taken, e.readyHosts(p.hosts.redirects)), StaticRoot: p.staticRoot}
		for _, m := range p.members {
			c, exists := containers[m.replica.ContainerID]
			if !exists || !c.Running {
				continue
			}
			backend := proxy.Backend{Name: backendName(p.app, m.port), Port: m.port}
			if slices.Contains(e.namesOf(c.ID), backend.Name) && !slices.Contains(route.Backends, backend) {
				route.Backends = append(route.Backends, backend)
			}
		}
		routes = append(routes, route)
	}
	return routes
}

// readyHosts is the subset of hosts whose DNS points here.
func (e *Engine) readyHosts(hosts []string) []string {
	var ready []string
	for _, h := range hosts {
		if ok, _ := e.hostnameReady(h); ok {
			ready = append(ready, h)
		}
	}
	return ready
}

// claim marks hosts as taken and returns the ones that were not already.
func claim(taken map[string]bool, hosts []string) []string {
	var free []string
	for _, h := range hosts {
		if !taken[h] {
			taken[h] = true
			free = append(free, h)
		}
	}
	return free
}

// setNames makes names what the container answers to, if it is not already.
// Changing them cuts what the container has open over the services network,
// so it happens only when it must. The caller holds e.routing.
func (e *Engine) setNames(ctx context.Context, id string, names []string) error {
	if slices.Equal(e.namesOf(id), names) {
		return nil
	}
	if err := e.rt.SetServiceNames(ctx, id, names); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(names) == 0 {
		delete(e.names, id)
	} else {
		e.names[id] = slices.Clone(names)
	}
	return nil
}

func (e *Engine) namesOf(id string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.names[id]
}

func (e *Engine) forgetNames(existing map[string]docker.Container) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.names {
		if _, ok := existing[id]; !ok {
			delete(e.names, id)
		}
	}
}

// startNameless starts a container that exists already. It may still carry
// the names it had when it stopped, and would be findable again the moment it
// starts, long before it is ready; it earns them back through syncRouting.
func (e *Engine) startNameless(ctx context.Context, id string) error {
	e.routing.Lock()
	err := e.setNames(ctx, id, nil)
	e.routing.Unlock()
	if err != nil {
		return err
	}
	return e.rt.StartContainer(ctx, id)
}

// restartNameless is a restart in three steps instead of Docker's one, for the
// same reason.
func (e *Engine) restartNameless(ctx context.Context, id string) error {
	if err := e.rt.StopContainer(ctx, id, e.opts.StopTimeout); err != nil {
		return err
	}
	return e.startNameless(ctx, id)
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
