package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// stalled is a runtime in which one kind of operation never finishes: it
// says that it was reached and then waits for the agent to shut down. That is
// how a test stops the agent in a phase of its choosing without guessing at
// timings.
type stalled struct {
	Runtime
	at      string // "pull", "create", "start" or "hook"
	once    sync.Once
	reached chan struct{}
}

func stallAt(h *harness, at string) *stalled {
	s := &stalled{Runtime: h.rt, at: at, reached: make(chan struct{})}
	h.engine.rt = s
	return s
}

func (s *stalled) stall(ctx context.Context) error {
	s.once.Do(func() { close(s.reached) })
	<-ctx.Done()
	return ctx.Err()
}

func (s *stalled) PullImage(ctx context.Context, image string, auth *docker.RegistryAuth) error {
	if s.at == "pull" {
		return s.stall(ctx)
	}
	return s.Runtime.PullImage(ctx, image, auth)
}

func (s *stalled) CreateContainer(ctx context.Context, c docker.ContainerSpec) (string, string, error) {
	if s.at == "create" && c.Job == nil {
		return "", "", s.stall(ctx)
	}
	return s.Runtime.CreateContainer(ctx, c)
}

func (s *stalled) StartContainer(ctx context.Context, id string) error {
	if s.at == "start" {
		return s.stall(ctx)
	}
	return s.Runtime.StartContainer(ctx, id)
}

func (s *stalled) WaitContainer(ctx context.Context, id string) (int, error) {
	if s.at == "hook" {
		s.once.Do(func() { close(s.reached) })
	}
	return s.Runtime.WaitContainer(ctx, id)
}

// stallProbes makes the health check of the replicas chosen by which never
// answer, and reports the first time one is asked.
func stallProbes(h *harness, which func(docker.Container) bool) <-chan struct{} {
	reached := make(chan struct{})
	var once sync.Once
	h.engine.opts.Probe = func(ctx context.Context, ip string, _ int, _ string, _ time.Duration) error {
		for _, c := range h.rt.Containers() {
			if c.IP == ip && which(c) {
				once.Do(func() { close(reached) })
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return nil
	}
	return reached
}

// restart is the agent stopping and starting again: the engine shuts down,
// and a new one with the same database, the same Docker and the same proxy
// recovers. It returns once every resumed deployment has ended.
func (h *harness) restart() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.engine.Shutdown(ctx); err != nil {
		h.t.Fatalf("Shutdown: %v", err)
	}
	opts := h.engine.opts
	opts.Probe = func(context.Context, string, int, string, time.Duration) error { return nil }
	h.engine = New(h.store, h.rt, opts)
	engine := h.engine
	h.t.Cleanup(func() { engine.Shutdown(context.Background()) })
	if err := h.engine.Recover(ctx); err != nil {
		h.t.Fatalf("Recover: %v", err)
	}
	h.engine.Wait()
}

func (h *harness) begin(a spec.App) store.Deployment {
	h.t.Helper()
	d, err := h.engine.Deploy(context.Background(), a)
	if err != nil {
		h.t.Fatalf("Deploy: %v", err)
	}
	return d
}

func (h *harness) deployment(id int64) store.Deployment {
	h.t.Helper()
	d, err := h.store.GetDeployment(context.Background(), id)
	if err != nil {
		h.t.Fatalf("GetDeployment: %v", err)
	}
	return d
}

// wantResumed checks that the deployment went on to ACTIVE after the restart,
// said that it resumed, and passed through every status exactly once.
func (h *harness) wantResumed(id int64) store.Deployment {
	h.t.Helper()
	d := h.deployment(id)
	if d.Status != api.StatusActive || d.Error != "" || d.CompletedAt == nil {
		h.t.Fatalf("deployment after the restart: status %s, error %q, completed %v\n%s", d.Status, d.Error, d.CompletedAt, h.steps(id))
	}
	var states []string
	resumed := 0
	for _, e := range h.events(id) {
		if e.Type == api.EventState {
			states = append(states, e.Message)
		}
		if e.Message == "Resumed after the agent restarted" {
			resumed++
		}
	}
	if got, want := strings.Join(states, " "), "BUILDING STARTING HEALTH_CHECKING HEALTHY ACTIVE"; got != want {
		h.t.Errorf("state events = %q, want %q: a resumed deployment repeats no transition", got, want)
	}
	if resumed != 1 {
		h.t.Errorf("the events say %d times that the deployment resumed, want once", resumed)
	}
	return d
}

func TestDeploymentInterruptedWhilePullingResumes(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "pull")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached
	h.restart()

	h.wantResumed(d.ID)
	if got := names(h.rt.Containers()); got != "shipwick_my-api_2_1" {
		t.Errorf("containers = %s, want only the new version's replica", got)
	}
	if !slices.Contains(h.rt.Pulled(), "my-api:1.1") {
		t.Errorf("pulled %v: the pull that was cut off must be done again", h.rt.Pulled())
	}
}

func TestDeploymentInterruptedBeforeItsReplicaWasCreatedResumes(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "create")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached
	if got := h.deployment(d.ID).Status; got != api.StatusStarting {
		t.Fatalf("status when the agent stops = %s, want STARTING", got)
	}
	h.restart()

	h.wantResumed(d.ID)
	if got := names(h.rt.Containers()); got != "shipwick_my-api_2_1" {
		t.Errorf("containers = %s, want only the new version's replica", got)
	}
}

func TestDeploymentInterruptedWhileStartingAdoptsTheContainerItCreated(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "start")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached
	var created docker.Container
	for _, c := range h.rt.Containers() {
		if c.DeploymentID == d.ID {
			created = c
		}
	}
	if created.ID == "" || created.Running {
		t.Fatalf("expected a created, not yet started container of the new deployment: %+v", h.rt.Containers())
	}
	h.restart()

	h.wantResumed(d.ID)
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].ID != created.ID || !containers[0].Running {
		t.Errorf("containers = %+v, want the container the deployment had created, adopted and started", containers)
	}
	if n := h.rt.Starts(created.ID); n != 1 {
		t.Errorf("the adopted container was started %d times, want once", n)
	}
}

func TestDeploymentInterruptedDuringHealthChecksAdoptsItsReplica(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 1)))
	old := s.container(t, 1)

	reached := stallProbes(s.harness, func(c docker.Container) bool { return c.DeploymentID != old.DeploymentID })
	d := s.begin(withHealth(web("web:1.1", 1)))
	<-reached
	if got := s.deployment(d.ID).Status; got != api.StatusHealthChecking {
		t.Fatalf("status when the agent stops = %s, want HEALTH_CHECKING", got)
	}
	var fresh docker.Container
	for _, c := range s.rt.Containers() {
		if c.DeploymentID == d.ID {
			fresh = c
		}
	}
	s.restart()

	s.wantResumed(d.ID)
	containers := s.rt.Containers()
	if len(containers) != 1 || containers[0].ID != fresh.ID {
		t.Errorf("containers = %s, want the replica that was being checked, adopted", names(containers))
	}
	if n := s.rt.Starts(fresh.ID); n != 1 {
		t.Errorf("the adopted replica was started %d times, want once", n)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != fresh.Name+":8080" {
		t.Errorf("serving %v, want the new replica", got)
	}
	// The old version served until the new one was verified, across the
	// restart: the domain never had nobody behind it.
	if history := p.history("web.example.com"); slices.Contains(history, "") {
		t.Errorf("the domain was without a backend at some point: %q", history)
	}
}

func TestDeploymentInterruptedBetweenReplicasGoesOnWithTheNextOne(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(withHealth(web("web:1.0", 2)))

	// Replica 1 of the new version is verified and takes over; the agent
	// stops while replica 2 is being checked.
	reached := stallProbes(s.harness, func(c docker.Container) bool { return c.DeploymentID != v1.ID && c.Replica == 2 })
	d := s.begin(withHealth(web("web:1.1", 2)))
	<-reached
	var first docker.Container
	for _, c := range s.rt.Containers() {
		if c.DeploymentID == d.ID && c.Replica == 1 {
			first = c
		}
	}
	if got := s.rt.Resolve("web"); !slices.Contains(got, first.Name) {
		t.Fatalf("replica 1 of the new version should be serving when the agent stops; serving %v", got)
	}
	s.restart()

	s.wantResumed(d.ID)
	if got := names(s.rt.Containers()); got != "shipwick_web_2_1 shipwick_web_2_2" {
		t.Errorf("containers = %s, want the two replicas of the new version", got)
	}
	for _, c := range s.rt.Containers() {
		if n := s.rt.Starts(c.ID); n != 1 {
			t.Errorf("%s was started %d times, want once: what was done is not done again", c.Name, n)
		}
	}
	if c := s.container(t, 1); c.ID != first.ID {
		t.Errorf("replica 1 is %s, want the one that was serving before the restart (%s)", c.ID, first.ID)
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v, want both replicas", got)
	}
	if history := p.history("web.example.com"); slices.Contains(history, "") {
		t.Errorf("the domain was without a backend at some point: %q", history)
	}
	if old := s.deployment(v1.ID); old.Status != api.StatusSuperseded {
		t.Errorf("the previous deployment is %s, want SUPERSEDED", old.Status)
	}
}

func TestDeploymentWhoseHookWasRunningFailsInsteadOfRunningItTwice(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	h.rt.HoldJobs = true
	stall := stallAt(h, "hook")
	d := h.begin(withHook(app("my-api", "my-api:1.1", 1), "migrate"))
	<-stall.reached
	h.restart()

	got := h.deployment(d.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("deployment after the restart: %s, completed %v", got.Status, got.CompletedAt)
	}
	if !strings.Contains(got.Error, "pre-deploy command was interrupted") || !strings.Contains(got.Error, "not run a second time") {
		t.Errorf("error = %q; it must say that the hook was interrupted and is not repeated", got.Error)
	}
	hooks := 0
	for _, run := range h.runs("my-api") {
		if run.Kind == api.RunKindHook {
			hooks++
			if run.Status != api.RunInterrupted {
				t.Errorf("the hook's run is %s, want interrupted", run.Status)
			}
		}
	}
	if hooks != 1 {
		t.Errorf("the hook ran %d times, want once", hooks)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("containers = %+v, want the old version untouched and nothing else", containers)
	}
}

func TestDeploymentWhoseHookHadFinishedDoesNotRunItAgain(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	// The hook runs to its end; the agent stops before the first replica.
	stall := stallAt(h, "create")
	d := h.begin(withHook(app("my-api", "my-api:1.1", 1), "migrate"))
	<-stall.reached
	h.restart()

	h.wantResumed(d.ID)
	hooks := 0
	for _, run := range h.runs("my-api") {
		if run.Kind == api.RunKindHook {
			hooks++
		}
	}
	if hooks != 1 {
		t.Errorf("the hook ran %d times, want once", hooks)
	}
}

func TestHookThatFinishedBeforeTheAgentStoppedInBuildingIsNotRepeated(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	// A crash right after the hook: BUILDING, with a run that succeeded.
	a := withHook(app("my-api", "my-api:1.1", 1), "migrate")
	d, _ := h.store.CreateDeployment(ctx, a, time.Now())
	h.store.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, "")
	run, err := h.store.CreateJobRun(ctx, store.JobRun{ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: hookJobName, Kind: api.RunKindHook, Command: a.PreDeploy.Command}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	h.store.FinishJobRun(ctx, run.ID, api.RunSucceeded, &code, "", time.Now())
	h.restart()

	final := h.deployment(d.ID)
	if final.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", final.Status, final.Error)
	}
	if runs := h.runs("my-api"); len(runs) != 1 {
		t.Errorf("%d hook runs, want the one that had finished and no second", len(runs))
	}
}

func TestRollbackInterruptedWhileRestoringIsFinished(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))

	// What a crash in the middle of a rollback leaves: the new version's
	// first replica took over from the old one, its second failed, and the
	// agent stopped after it had begun to restore the old version.
	a2 := web("web:1.1", 2)
	v2, _ := s.store.CreateDeployment(ctx, a2, time.Now())
	for _, step := range [][2]api.DeploymentStatus{
		{api.StatusPending, api.StatusBuilding}, {api.StatusBuilding, api.StatusStarting}, {api.StatusStarting, api.StatusHealthChecking},
	} {
		s.store.TransitionDeployment(ctx, v2.ID, step[0], step[1], "")
	}
	created, err := s.engine.ensureReplicas(ctx, v2, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	s.rt.SetServiceNames(ctx, created[0].ContainerID, serviceNames("web", 8080))
	old := s.container(t, 1)
	s.rt.RemoveContainer(ctx, old.ID)
	s.store.MarkReplicaRemoved(ctx, old.ID, time.Now())
	s.store.TransitionDeployment(ctx, v2.ID, api.StatusHealthChecking, api.StatusFailed, "replica 2 exited with code 1 shortly after start")
	s.store.TransitionDeployment(ctx, v2.ID, api.StatusFailed, api.StatusRollback, "")
	s.store.TransitionDeployment(ctx, v2.ID, api.StatusRollback, api.StatusRestoring, "")

	s.restart()

	got := s.deployment(v2.ID)
	if got.Status != api.StatusRolledBack || got.CompletedAt == nil {
		t.Fatalf("deployment after the restart: %s, completed %v\n%s", got.Status, got.CompletedAt, s.steps(v2.ID))
	}
	if !strings.Contains(got.Error, "replica 2 exited") {
		t.Errorf("error = %q; why the deployment failed must survive the restart", got.Error)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" {
		t.Errorf("containers = %s, want the old version complete again and nothing of the new one", got)
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v, want both replicas of the old version", got)
	}
	if history := p.history("web.example.com"); slices.Contains(history, "") {
		t.Errorf("the domain was without a backend at some point: %q", history)
	}
	steps := s.steps(v2.ID)
	for _, want := range []string{"Resumed after the agent restarted", "Rolled back: web is running 1.0 again"} {
		if !strings.Contains(steps, want) {
			t.Errorf("events lack %q:\n%s", want, steps)
		}
	}
	if app, _ := s.store.GetApplication(ctx, "web"); app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v1.ID {
		t.Errorf("the active deployment is %v, want the old one", app.ActiveDeploymentID)
	}
}

func TestResumedDeploymentHoldsTheApplicationsLock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "pull")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached
	if err := h.engine.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	// The next agent's pull stalls as well, so that the resumed deployment
	// is still under way when another operation asks for the application.
	opts := h.engine.opts
	h.engine = New(h.store, h.rt, opts)
	engine := h.engine
	t.Cleanup(func() { engine.Shutdown(context.Background()) })
	again := stallAt(h, "pull")
	if err := h.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	<-again.reached

	if err := h.engine.Stop(ctx, "my-api"); !errors.Is(err, ErrBusy) {
		t.Errorf("Stop while a deployment resumes: err = %v, want ErrBusy", err)
	}
	if got := h.deployment(d.ID); got.CompletedAt != nil {
		t.Errorf("the resumed deployment is marked completed while it runs: %+v", got)
	}
	view, err := h.engine.Application(ctx, "my-api")
	if err != nil || !view.Deploying {
		t.Errorf("the application does not show as deploying while its deployment resumes: %+v, %v", view.Application, err)
	}
}

func TestStaticDeploymentInterruptedResumes(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	a := site("site")
	up := s.upload("site", map[string]string{"index.html": "<h1>hello</h1>"})

	// A crash after the record was made and before anything was copied.
	files, err := s.engine.uploadInfo("site", up.Digest)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.store.CreateStaticDeployment(ctx, a, api.KindDeploy, nil, "", files, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.store.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, "")
	s.store.TransitionDeployment(ctx, d.ID, api.StatusBuilding, api.StatusStarting, "")
	s.restart()

	final := s.deployment(d.ID)
	if final.Status != api.StatusActive || final.CompletedAt == nil {
		t.Fatalf("deployment after the restart: %s (%s)\n%s", final.Status, final.Error, s.steps(d.ID))
	}
	if !strings.Contains(s.steps(d.ID), "Resumed after the agent restarted") {
		t.Errorf("the events do not say that the deployment resumed:\n%s", s.steps(d.ID))
	}
	if got := s.served("site"); len(got) != 1 || got[0] != hexOf(up)+"/index.html" {
		t.Errorf("files in the proxy = %v, want the uploaded folder", got)
	}
	if route := p.route(t, "site.example.com"); route.StaticRoot != staticDir("site", up.Digest) {
		t.Errorf("route = %+v, want the folder as its root", route)
	}
}
