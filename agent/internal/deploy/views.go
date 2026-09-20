package deploy

import (
	"context"
	"os"
	"sort"
	"sync"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// This file holds the read side of the engine: it joins what the database
// knows (desired state, history) with what Docker reports (actual state) into
// the API's view types.

// Applications returns a summary of every application.
func (e *Engine) Applications(ctx context.Context) ([]api.Application, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return nil, err
	}
	inFlight, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Statuses: InFlightStatuses()})
	if err != nil {
		return nil, err
	}
	deploying := map[string]*int64{}
	for _, d := range inFlight {
		deploying[d.Application] = &d.ID
	}

	out := make([]api.Application, 0, len(apps))
	for _, app := range apps {
		var active *store.Deployment
		if app.ActiveDeploymentID != nil {
			d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
			if err != nil {
				return nil, err
			}
			active = &d
		}
		out = append(out, e.summarize(app, active, containers, deploying[app.Name]))
	}
	return out, nil
}

// Application returns the detailed view of one application.
func (e *Engine) Application(ctx context.Context, name string) (api.ApplicationDetail, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.ApplicationDetail{}, err
	}
	listed, err := e.rt.ListContainers(ctx, name)
	if err != nil {
		return api.ApplicationDetail{}, err
	}
	inFlight, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: name, Statuses: InFlightStatuses(), Limit: 1})
	if err != nil {
		return api.ApplicationDetail{}, err
	}

	var active *store.Deployment
	if app.ActiveDeploymentID != nil {
		d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
		if err != nil {
			return api.ApplicationDetail{}, err
		}
		active = &d
	}

	detail := api.ApplicationDetail{
		Application: e.summarize(app, active, listed, inFlightID(inFlight)),
		Containers:  make([]api.Container, 0, len(listed)),
	}
	if active != nil {
		redacted := active.Spec.Redacted()
		view := DeploymentView(*active)
		detail.Spec, detail.ActiveDeployment = &redacted, &view
	}
	restarts := map[string]int{}
	if active != nil {
		replicas, err := e.store.ListReplicas(ctx, active.ID)
		if err != nil {
			return api.ApplicationDetail{}, err
		}
		for _, r := range replicas {
			restarts[r.ContainerID] = r.Restarts
		}
	}
	for _, c := range listed {
		// Inspect adds exit code, OOM flag and start time. A container may
		// vanish between list and inspect; the listed data is good enough then.
		if full, err := e.rt.InspectContainer(ctx, c.ID); err == nil {
			c = full
		}
		view := containerView(c)
		view.Restarts = restarts[c.ID]
		view.Health, view.CrashLoop = e.sup.snapshot(c.ID)
		detail.Containers = append(detail.Containers, view)
	}
	return detail, nil
}

// isReady reports whether a replica counts as healthy: it runs, and nothing
// is known against it. "Unknown" is deliberately included — right after an
// agent restart no replica has been probed yet, and an application must not
// flap to DOWN because its supervisor was restarted.
func isReady(running bool, health string) bool {
	return running && (health == api.HealthNone || health == api.HealthUnknown || health == api.HealthHealthy)
}

func inFlightID(inFlight []store.Deployment) *int64 {
	if len(inFlight) == 0 {
		return nil
	}
	return &inFlight[0].ID
}

func (e *Engine) summarize(app store.Application, active *store.Deployment, containers []docker.Container, inFlight *int64) api.Application {
	deploying := inFlight != nil
	out := api.Application{
		Name:         app.Name,
		DesiredState: app.DesiredState,
		Deploying:    deploying,

		InFlightDeploymentID: inFlight,
		CreatedAt:            app.CreatedAt,
		UpdatedAt:            app.UpdatedAt,
	}
	crashLoop := false
	if active != nil {
		out.Image, out.Version, out.Domain = active.Image, active.Version, active.Spec.Domain
		out.Replicas.Desired = active.Spec.Replicas

		// Normally the replicas that count are the active deployment's. While a
		// rollout is replacing them, they are whoever the rollout has serving.
		counts := func(c docker.Container) bool { return c.DeploymentID == active.ID }
		if o, rolling := e.serving(app.Name); rolling {
			members := make(map[string]bool, len(o.members))
			for _, m := range o.members {
				members[m.replica.ContainerID] = true
			}
			counts = func(c docker.Container) bool { return members[c.ID] }
			out.Replicas.Desired = o.desired
		}
		for _, c := range containers {
			if c.App != app.Name || !counts(c) {
				continue
			}
			health, looping := e.sup.snapshot(c.ID)
			crashLoop = crashLoop || looping
			if c.Running {
				out.Replicas.Running++
			}
			if isReady(c.Running, health) {
				out.Replicas.Healthy++
			}
		}
	}
	out.Status = applicationStatus(active != nil, app.DesiredState, out.Replicas, deploying, crashLoop)
	return out
}

func applicationStatus(hasActive bool, desiredState string, replicas api.ReplicaCount, deploying, crashLoop bool) api.ApplicationStatus {
	switch {
	case !hasActive && deploying:
		return api.AppDeploying
	case !hasActive:
		return api.AppFailed
	case desiredState == api.DesiredStopped:
		return api.AppStopped
	case crashLoop:
		// Says more than DEGRADED or DOWN would: not just "replicas are
		// missing" but "and restarting them is not working".
		return api.AppCrashLoop
	case replicas.Healthy >= replicas.Desired:
		return api.AppHealthy
	case replicas.Healthy == 0:
		return api.AppDown
	default:
		return api.AppDegraded
	}
}

// Events returns the application's own event feed, newest first: what the
// supervisor saw and did, stops and starts. Deployment events live with their
// deployment.
func (e *Engine) Events(ctx context.Context, name string, limit int) ([]api.Event, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	return e.store.ListApplicationEvents(ctx, app.ID, limit)
}

// DeploymentView converts a stored deployment to its API representation.
func DeploymentView(d store.Deployment) api.Deployment {
	return api.Deployment{
		ID:          d.ID,
		Application: d.Application,
		Sequence:    d.Sequence,
		Version:     d.Version,
		Image:       d.Image,
		Status:      d.Status,
		Error:       d.Error,
		StartedAt:   d.StartedAt,
		CompletedAt: d.CompletedAt,

		Kind:               d.Kind,
		SourceDeploymentID: d.SourceID,
	}
}

func containerView(c docker.Container) api.Container {
	return api.Container{
		ID:           c.ID,
		Name:         c.Name,
		DeploymentID: c.DeploymentID,
		Replica:      c.Replica,
		Image:        c.Image,
		State:        c.State,
		ExitCode:     c.ExitCode,
		OOMKilled:    c.OOMKilled,
		IP:           c.IP,
		StartedAt:    c.StartedAt,
	}
}

// Deployments lists deployments, newest first, optionally for one application.
func (e *Engine) Deployments(ctx context.Context, application string, limit int) ([]api.Deployment, error) {
	if application != "" {
		if _, err := e.store.GetApplication(ctx, application); err != nil {
			return nil, err
		}
	}
	deployments, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: application, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]api.Deployment, 0, len(deployments))
	for _, d := range deployments {
		out = append(out, DeploymentView(d))
	}
	return out, nil
}

// Deployment returns one deployment with its spec (env masked) and events.
func (e *Engine) Deployment(ctx context.Context, id int64) (api.DeploymentDetail, error) {
	d, err := e.store.GetDeployment(ctx, id)
	if err != nil {
		return api.DeploymentDetail{}, err
	}
	events, err := e.store.ListDeploymentEvents(ctx, id)
	if err != nil {
		return api.DeploymentDetail{}, err
	}
	return api.DeploymentDetail{Deployment: DeploymentView(d), Spec: d.Spec.Redacted(), Events: events}, nil
}

// Logs returns the last `tail` log lines across the replicas of the
// application's active deployment, merged in chronological order.
func (e *Engine) Logs(ctx context.Context, name string, tail int) ([]api.LogLine, error) {
	replicas, err := e.activeReplicas(ctx, name)
	if err != nil {
		return nil, err
	}

	lines := []api.LogLine{}
	for _, r := range replicas {
		entries, err := e.rt.Logs(ctx, r.ContainerID, tail)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			lines = append(lines, logLineView(r, entry))
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// LogStream follows the logs of every replica of the application's active
// deployment, each starting with its last `tail` lines. Lines of one replica
// arrive in order; replicas interleave as their output arrives.
//
// The channel is closed when ctx is cancelled or when every replica's log has
// ended — which is what happens when a new deployment replaces the containers.
func (e *Engine) LogStream(ctx context.Context, name string, tail int) (<-chan api.LogLine, error) {
	replicas, err := e.activeReplicas(ctx, name)
	if err != nil {
		return nil, err
	}

	lines := make(chan api.LogLine, 64)
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := e.rt.FollowLogs(ctx, r.ContainerID, tail, func(entry docker.LogEntry) {
				select {
				case lines <- logLineView(r, entry):
				case <-ctx.Done():
				}
			})
			if err != nil {
				e.log.Warn("log stream ended with an error", "container", r.ContainerName, "error", err)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(lines)
	}()
	return lines, nil
}

func (e *Engine) activeReplicas(ctx context.Context, name string) ([]store.Replica, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	if app.ActiveDeploymentID == nil {
		return nil, ErrNotDeployed
	}
	return e.store.ListReplicas(ctx, *app.ActiveDeploymentID)
}

func logLineView(r store.Replica, entry docker.LogEntry) api.LogLine {
	return api.LogLine{
		Replica:   r.Index,
		Container: r.ContainerName,
		Stream:    entry.Stream,
		Time:      entry.Time,
		Message:   entry.Message,
	}
}

// Server describes the host the agent runs on.
func (e *Engine) Server(ctx context.Context) (api.Server, error) {
	info, err := e.rt.Info(ctx)
	if err != nil {
		return api.Server{}, err
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return api.Server{}, err
	}
	containers, err := e.rt.ListContainers(ctx, "")
	if err != nil {
		return api.Server{}, err
	}
	running := 0
	for _, c := range containers {
		if c.Running {
			running++
		}
	}

	hostname := info.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	return api.Server{
		AgentVersion:  version.Version,
		Hostname:      hostname,
		OS:            info.OS,
		Kernel:        info.Kernel,
		Architecture:  info.Architecture,
		DockerVersion: info.DockerVersion,
		CPUs:          info.CPUs,
		MemoryBytes:   info.MemoryBytes,
		Applications:  len(apps),
		Containers:    running,
		Proxy:         api.ProxyStatus(e.ProxyStatus()),
	}, nil
}
