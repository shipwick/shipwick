package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// What happens when the things under the agent break: the disk its database
// is on, the Docker daemon, the proxy, the agent's own process, the server.
// Each test produces one failure and pins down what the agent does about it;
// docs/handbook.md, "When things break", says the same to the operator. The
// failures an agent restart can produce half-way through a rolling deployment
// are in resume_test.go, and what a small disk does for real in
// faults_disk_test.go.

// outage puts a daemon that can stop answering between the engine and the
// fake's containers.
func outage(h *harness) *dockertest.Outage {
	out := dockertest.NewOutage(h.rt)
	h.engine.rt = out
	return out
}

// free reports whether the application's lock is free.
func (h *harness) free(name string) bool {
	wait, err := h.engine.tryLock(name, false)
	if wait != nil || err != nil {
		return false
	}
	h.engine.unlock(name)
	return true
}

// stopAgent is the agent's process ending: by a signal, or by the server
// going down under it. startAgent is the next one starting, with the same
// database and the same Docker; it returns once every deployment it resumed
// has ended.
func (h *harness) stopAgent() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.engine.Shutdown(ctx); err != nil {
		h.t.Fatalf("Shutdown: %v", err)
	}
}

func (h *harness) startAgent() {
	h.t.Helper()
	opts := h.engine.opts
	opts.Probe = func(context.Context, string, int, string, time.Duration) error { return nil }
	h.engine = New(h.store, h.rt, opts)
	engine := h.engine
	h.t.Cleanup(func() { engine.Shutdown(context.Background()) })
	if err := h.engine.Recover(context.Background()); err != nil {
		h.t.Fatalf("Recover: %v", err)
	}
	h.engine.Wait()
}

// powerOff ends every container the way a server that loses power does: none
// of them was asked, and Docker reports exit code 255 for each when it is
// back (measured, see docs/handbook.md).
func (h *harness) powerOff() {
	for _, c := range h.rt.Containers() {
		if c.Running {
			h.rt.Crash(c.ID, 255)
		}
	}
}

func running(containers []docker.Container) string {
	var up []docker.Container
	for _, c := range containers {
		if c.Running {
			up = append(up, c)
		}
	}
	return names(up)
}

func recreating(image string) spec.App {
	a := app("db", image, 1)
	a.Volumes = []spec.Volume{{Name: "data", Path: "/var/lib/data"}}
	a.Deploy.Strategy = spec.StrategyRecreate
	return a
}

// The disk is full.

func TestADeploymentTheDatabaseHasNoRoomForIsRefusedAndNothingChanges(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	if err := h.store.Fill(ctx, true); err != nil {
		t.Fatal(err)
	}
	// A record that needs a page of its own, whatever room the last one left.
	big := app("my-api", "my-api:1.1", 1)
	big.Env["LICENCE"] = strings.Repeat("x", 64<<10)
	_, err := h.engine.Deploy(ctx, big)
	if !IsDiskFull(err) {
		t.Fatalf("Deploy on a full disk: err = %v, want one that says the disk is full", err)
	}
	if msg := DiskFullMessage(err); !strings.Contains(msg, "database or disk is full") || strings.Contains(msg, "xxxx") {
		t.Errorf("message = %q; it must name the cause and nothing of the configuration", msg)
	}
	h.engine.Wait()

	if !h.free("my-api") {
		t.Error("the application is still locked after the refusal")
	}
	if got := running(h.rt.Containers()); got != "shipwick_my-api_1_1" {
		t.Errorf("running = %s, want the version that ran before and nothing else", got)
	}
	if app, _ := h.store.GetApplication(ctx, "my-api"); app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v1.ID {
		t.Errorf("the active deployment changed: %v", app.ActiveDeploymentID)
	}
	if all, _ := h.store.ListDeployments(ctx, storeFilterLatest); len(all) != 1 || all[0].ID != v1.ID {
		t.Errorf("a deployment was recorded on a full disk: %+v", all)
	}

	// Room again: the same request goes through, and nothing was reopened.
	if err := h.store.Fill(ctx, false); err != nil {
		t.Fatal(err)
	}
	if d := h.deploy(big); d.Status != api.StatusActive {
		t.Errorf("after space was freed: %s (%s)", d.Status, d.Error)
	}
}

// refuseWritesAtCreate makes the database refuse every write from the moment
// the first container of a deployment other than `before` is created: the
// deployment is recorded and under way, and nothing of what follows can be.
func refuseWritesAtCreate(h *harness, before int64) {
	h.rt.CreateHook = func(c docker.ContainerSpec) error {
		if c.DeploymentID != before {
			if err := h.store.RefuseWrites(context.Background(), true); err != nil {
				h.t.Errorf("RefuseWrites: %v", err)
			}
		}
		return nil
	}
}

func TestADeploymentWhoseEndTheDatabaseRefusedIsSettledOnceItTakesWrites(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	refuseWritesAtCreate(h, v1.ID)
	d := h.begin(app("my-api", "my-api:1.1", 1))
	h.engine.Wait()
	h.rt.CreateHook = nil

	// The engine is done with it — the replica it could not record is gone,
	// the old version runs, the application is free — and the record cannot
	// say so.
	got := h.deployment(d.ID)
	if got.Status != api.StatusStarting || got.CompletedAt != nil {
		t.Fatalf("record while the database refuses writes: %s, completed %v; want it as the last write left it", got.Status, got.CompletedAt)
	}
	if up := running(h.rt.Containers()); up != "shipwick_my-api_1_1" || len(h.rt.Containers()) != 1 {
		t.Errorf("containers = %s, want the old version alone", names(h.rt.Containers()))
	}
	if !h.free("my-api") {
		t.Error("the application stays locked by a deployment that has ended")
	}

	// Still refused: asking again changes nothing and loses nothing.
	h.engine.retrySettling(ctx)
	if got := h.deployment(d.ID); got.CompletedAt != nil {
		t.Fatalf("completed while the database refuses writes: %+v", got)
	}

	if err := h.store.RefuseWrites(ctx, false); err != nil {
		t.Fatal(err)
	}
	h.engine.retrySettling(ctx)
	got = h.deployment(d.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("record once the database takes writes: %s, completed %v; want FAILED and completed", got.Status, got.CompletedAt)
	}
	if got.Error != "record replica: attempt to write a readonly database (8)" {
		t.Errorf("error = %q; it must be why the deployment failed when it did", got.Error)
	}
	events := h.events(d.ID)
	if last := events[len(events)-1]; last.Type != api.EventState || last.Message != "FAILED: "+got.Error {
		t.Errorf("the last event is %q, want the failure", last.Message)
	}

	h.engine.retrySettling(ctx) // nothing left to write
	if d := h.deploy(app("my-api", "my-api:1.2", 1)); d.Status != api.StatusActive {
		t.Errorf("the next deployment: %s (%s)", d.Status, d.Error)
	}
}

func TestADeploymentWhoseEndWasNeverWrittenIsNotResumedOverANewerOne(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	refuseWritesAtCreate(h, v1.ID)
	lost := h.begin(app("my-api", "my-api:1.1", 1))
	h.engine.Wait()
	h.rt.CreateHook = nil
	if err := h.store.RefuseWrites(ctx, false); err != nil {
		t.Fatal(err)
	}

	// The operator freed space and deployed again, before the agent had
	// written the end of the deployment that failed; then the agent restarts.
	v3 := h.deploy(app("my-api", "my-api:1.2", 1))
	if v3.Status != api.StatusActive {
		t.Fatalf("v3: %s (%s)", v3.Status, v3.Error)
	}
	if got := h.deployment(lost.ID); got.CompletedAt != nil {
		t.Fatalf("the test wants a record left in flight; it is %s, completed", got.Status)
	}
	h.restart()

	got := h.deployment(lost.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("the stale deployment after the restart: %s, completed %v", got.Status, got.CompletedAt)
	}
	if !strings.Contains(got.Error, "deployed again since") {
		t.Errorf("error = %q; it must say why it was not resumed", got.Error)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v3.ID || containers[0].Image != "my-api:1.2" || !containers[0].Running {
		t.Errorf("containers = %+v, want the newest version untouched", containers)
	}
	if app, _ := h.store.GetApplication(ctx, "my-api"); app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v3.ID {
		t.Errorf("the active deployment is %v, want the newest", app.ActiveDeploymentID)
	}
}

func TestTheSupervisorRestartsReplicasWhileTheDatabaseRefusesWrites(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	victim := s.container(t, 1)

	if err := s.store.RefuseWrites(ctx, true); err != nil {
		t.Fatal(err)
	}
	s.rt.Crash(victim.ID, 137)
	s.advance(time.Second)
	s.advance(time.Second)
	if !s.container(t, 1).Running {
		t.Fatal("a crashed replica stays down because its restart cannot be written down")
	}
	if n := s.rt.Starts(s.container(t, 1).ID); n != 2 {
		t.Errorf("replica 1 was started %d times, want the deployment's start and one restart", n)
	}
}

func TestAMissingReplicaIsRecreatedOnceTheDatabaseTakesWritesAndNoContainerIsLeftOver(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))

	if err := s.store.RefuseWrites(ctx, true); err != nil {
		t.Fatal(err)
	}
	s.rt.RemoveContainer(ctx, s.container(t, 1).ID)
	for range 3 {
		s.advance(time.Second)
	}
	// A replica that cannot be recorded is not kept: nothing would know it.
	if got := names(s.rt.Containers()); got != "shipwick_my-api_1_2" {
		t.Fatalf("containers while the database refuses writes = %s, want only the replica that was left", got)
	}

	if err := s.store.RefuseWrites(ctx, false); err != nil {
		t.Fatal(err)
	}
	// The attempts backed off while they failed; the longest step is 30s.
	for range 4 {
		s.advance(10 * time.Second)
	}
	if got := running(s.rt.Containers()); got != "shipwick_my-api_1_1 shipwick_my-api_1_2" {
		t.Errorf("running = %s, want both replicas", got)
	}
	if events := strings.Join(s.appEvents(t, "my-api"), "\n"); !strings.Contains(events, "Recreated replica 1") {
		t.Errorf("events do not say that the replica was recreated:\n%s", events)
	}
}

// Docker does not answer.

func TestDockerGoingAwayHalfWayFailsTheDeploymentAndReconciliationRestoresTheOldVersion(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))
	out := outage(s.harness)

	// The first replica of the new version takes over; the daemon stops when
	// the second is to be created.
	created := 0
	s.rt.CreateHook = func(docker.ContainerSpec) error {
		if created++; created == 2 {
			out.Fail(dockertest.ErrDaemonDown)
			return dockertest.ErrDaemonDown
		}
		return nil
	}
	d := s.begin(web("web:1.1", 2))
	s.engine.Wait()
	s.rt.CreateHook = nil

	got := s.deployment(d.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("deployment: %s, completed %v\n%s", got.Status, got.CompletedAt, s.steps(d.ID))
	}
	for _, want := range []string{"replica 2", "Cannot connect to the Docker daemon", "the rollback to 1.0 then failed too"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("error = %q, want it to contain %q", got.Error, want)
		}
	}
	// Nothing that was serving has been taken away: Docker could not be asked to.
	if got := running(s.rt.Containers()); got != "shipwick_web_1_2 shipwick_web_2_1" {
		t.Errorf("running after the failure = %s, want one replica of each version", got)
	}
	if !s.free("web") {
		t.Error("the application stays locked after the deployment failed")
	}

	// While Docker is away, the supervisor does nothing and says nothing new.
	s.advance(time.Second)
	if events := s.appEvents(t, "web"); len(events) != 0 {
		t.Errorf("supervisor events during the outage: %q", events)
	}

	out.Restore()
	for range 3 {
		s.advance(time.Second)
		s.engine.awaitDrains(context.Background(), "web")
	}
	if got := running(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" || len(s.rt.Containers()) != 2 {
		t.Errorf("containers once Docker answers = %s, want the old version complete and nothing of the new one", names(s.rt.Containers()))
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v, want both replicas of the old version", got)
	}
	events := strings.Join(s.appEvents(t, "web"), "\n")
	for _, want := range []string{"Removed leftover container shipwick_web_2_1", "Recreated replica 1"} {
		if !strings.Contains(events, want) {
			t.Errorf("events lack %q:\n%s", want, events)
		}
	}
}

func TestWhileDockerDoesNotAnswerTheSupervisorHoldsNoLockAndRequestsAreToldWhy(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	out := outage(s.harness)
	out.Fail(dockertest.ErrDaemonDown)

	s.advance(time.Second)
	if n := out.Asked(); n != 1 {
		t.Errorf("the tick asked Docker %d times, want the one question that went unanswered", n)
	}
	if !s.free("my-api") {
		t.Error("the supervisor holds the application while Docker does not answer")
	}

	_, err := s.engine.Application(ctx, "my-api")
	if !IsRuntimeUnavailable(err) {
		t.Fatalf("Application: err = %v, want one that says Docker does not answer", err)
	}
	msg := RuntimeUnavailableMessage(err)
	for _, want := range []string{"Docker does not answer on the server", "systemctl status docker", "Cannot connect to the Docker daemon"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, want it to contain %q", msg, want)
		}
	}
	if _, err := s.engine.Deploy(ctx, app("my-api", "my-api:1.1", 2)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	s.engine.Wait()
	latest, _ := s.store.ListDeployments(ctx, storeFilterLatest)
	if d := latest[0]; d.Status != api.StatusFailed || !strings.Contains(d.Error, "Cannot connect to the Docker daemon") || d.CompletedAt == nil {
		t.Errorf("a deployment during the outage: %s %q, completed %v; want FAILED with Docker's own words", d.Status, d.Error, d.CompletedAt)
	}
}

func TestDockerNotAnsweringIsAnAlertAfterThirtySecondsAndClearsWithTheFirstAnswer(t *testing.T) {
	s := newSupervised(t)
	rec := notified(s.harness)
	s.deploy(app("my-api", "my-api:1.0", 2))
	out := outage(s.harness)
	victim := s.container(t, 1)

	dockerAlerts := func() []api.Alert {
		var found []api.Alert
		for _, a := range s.engine.Alerts() {
			if a.Kind == api.AlertDocker {
				found = append(found, a)
			}
		}
		return found
	}
	told := func(kind string) int {
		n := 0
		for _, e := range rec.Events() {
			if e.Kind == kind && e.Alert != nil && e.Alert.Kind == api.AlertDocker {
				n++
			}
		}
		return n
	}

	out.Fail(dockertest.ErrDaemonDown)
	s.rt.Crash(victim.ID, 137) // nobody can see it yet
	s.advance(time.Second)
	s.advance(29 * time.Second)
	if alerts := dockerAlerts(); len(alerts) != 0 {
		t.Fatalf("alert after 29s: %+v; a daemon that restarts is not an outage", alerts)
	}
	s.advance(time.Second)
	alerts := dockerAlerts()
	if len(alerts) != 1 || alerts[0].Severity != api.SeverityCritical || alerts[0].Application != "" {
		t.Fatalf("alerts after 30s = %+v, want one critical alert about the server", alerts)
	}
	if !strings.Contains(alerts[0].Message, "Docker does not answer") || !strings.Contains(alerts[0].Message, "systemctl status docker") {
		t.Errorf("message = %q", alerts[0].Message)
	}
	for range 5 {
		s.advance(time.Minute)
	}
	if n := told(notify.AlertRaised); n != 1 {
		t.Errorf("the alert was sent %d times, want once", n)
	}

	out.Restore()
	s.advance(time.Second)
	if alerts := dockerAlerts(); len(alerts) != 0 {
		t.Errorf("alerts once Docker answers = %+v", alerts)
	}
	if n := told(notify.AlertCleared); n != 1 {
		t.Errorf("the clearing was sent %d times, want once", n)
	}
	// What happened during the outage is seen by the first tick after it.
	s.advance(time.Second)
	if !s.container(t, 1).Running {
		t.Error("the replica that crashed during the outage was not restarted after it")
	}
}

func TestAnAgentThatStartsWhileDockerDoesNotAnswerLeavesAnInterruptedDeploymentToTheNextStart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	stall := stallAt(h, "start")
	d := h.begin(app("my-api", "my-api:1.1", 1))
	<-stall.reached
	h.stopAgent()

	down := dockertest.NewOutage(h.rt)
	down.Fail(dockertest.ErrDaemonDown)
	failing := New(h.store, down, h.engine.opts)
	t.Cleanup(func() { failing.Shutdown(context.Background()) })
	err := failing.Recover(ctx)
	if !IsRuntimeUnavailable(err) {
		t.Fatalf("Recover without Docker: err = %v, want one that says Docker does not answer", err)
	}
	if got := h.deployment(d.ID); got.Status != api.StatusStarting || got.CompletedAt != nil {
		t.Fatalf("the deployment after a start without Docker: %s, completed %v; want it untouched", got.Status, got.CompletedAt)
	}
	if wait, err := failing.tryLock("my-api", false); wait != nil || err != nil {
		t.Error("the start that failed kept the application's lock")
	} else {
		failing.unlock("my-api")
	}

	h.startAgent()
	h.wantResumed(d.ID)
	if got := running(h.rt.Containers()); got != "shipwick_my-api_2_1" {
		t.Errorf("running = %s, want the new version", got)
	}
}

// The agent is killed.

func TestRecreateInterruptedAfterTheOldVersionStoppedGoesOnWithTheNewOne(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy(recreating("postgres:16"))
	old := h.rt.Containers()[0]

	stall := stallAt(h, "start")
	d := h.begin(recreating("postgres:17"))
	<-stall.reached
	if c, _ := h.rt.InspectContainer(context.Background(), old.ID); c.Running {
		t.Fatal("the old version should be stopped when the new one is about to start")
	}
	h.restart()

	h.wantResumed(d.ID)
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != d.ID || !containers[0].Running {
		t.Errorf("containers = %s, want the new version alone", names(containers))
	}
	if n := h.rt.Starts(old.ID); n != 1 {
		t.Errorf("the old version was started %d times: it must not run again next to the new one", n)
	}
	if !h.rt.VolumeExists("db", "data") {
		t.Error("the volume is gone")
	}
	if got := h.deployment(v1.ID).Status; got != api.StatusSuperseded {
		t.Errorf("the old deployment is %s, want SUPERSEDED", got)
	}
}

func TestRecreateInterruptedWhileRollingBackStartsTheOldVersionAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	v1 := h.deploy(recreating("postgres:16"))
	old := h.rt.Containers()[0]

	// What a kill in the middle of the rollback leaves: the old version
	// stopped, the new one's replica dead, the record on its way back.
	v2, _ := h.store.CreateDeployment(ctx, recreating("postgres:17"), time.Now())
	for _, step := range [][2]api.DeploymentStatus{
		{api.StatusPending, api.StatusBuilding}, {api.StatusBuilding, api.StatusStarting}, {api.StatusStarting, api.StatusHealthChecking},
	} {
		h.store.TransitionDeployment(ctx, v2.ID, step[0], step[1], "")
	}
	h.rt.StopContainer(ctx, old.ID, time.Second)
	created, err := h.engine.ensureReplicas(ctx, v2, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	h.rt.Crash(created[0].ContainerID, 1)
	h.store.TransitionDeployment(ctx, v2.ID, api.StatusHealthChecking, api.StatusFailed, "replica 1 exited with code 1 shortly after start")
	h.store.TransitionDeployment(ctx, v2.ID, api.StatusFailed, api.StatusRollback, "")

	h.restart()

	got := h.deployment(v2.ID)
	if got.Status != api.StatusRolledBack || got.CompletedAt == nil {
		t.Fatalf("deployment after the restart: %s, completed %v\n%s", got.Status, got.CompletedAt, h.steps(v2.ID))
	}
	if !strings.Contains(got.Error, "replica 1 exited") {
		t.Errorf("error = %q; why the deployment failed must survive the restart", got.Error)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].ID != old.ID || !containers[0].Running {
		t.Errorf("containers = %+v, want the old version's own container running again", containers)
	}
	if app, _ := h.store.GetApplication(ctx, "db"); app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v1.ID {
		t.Errorf("the active deployment is %v, want the old one", app.ActiveDeploymentID)
	}
}

func TestAFailedDeploymentKilledBeforeItsRollbackBeganIsFinishedByReconciliation(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	v1 := s.deploy(web("web:1.0", 2))

	// The new version's first replica took over, its second failed, the
	// record says FAILED — and the agent was killed before the next line.
	v2, _ := s.store.CreateDeployment(ctx, web("web:1.1", 2), time.Now())
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

	s.restart()
	for range 3 {
		s.engine.awaitDrains(ctx, "web")
		s.advance(time.Second)
	}

	got := s.deployment(v2.ID)
	if got.Status != api.StatusFailed || got.CompletedAt == nil {
		t.Fatalf("deployment after the restart: %s, completed %v", got.Status, got.CompletedAt)
	}
	if got := running(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" || len(s.rt.Containers()) != 2 {
		t.Errorf("containers = %s, want the old version complete again and nothing of the new one", names(s.rt.Containers()))
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v, want both replicas of the old version", got)
	}
	if app, _ := s.store.GetApplication(ctx, "web"); app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v1.ID {
		t.Errorf("the active deployment is %v, want the old one", app.ActiveDeploymentID)
	}
}

func TestARollbackOnRequestInterruptedHalfWayResumesLikeAnyDeployment(t *testing.T) {
	ctx := context.Background()
	s, p := newRouted(t)
	s.deploy(withHealth(web("web:1.0", 2)))
	v2 := s.deploy(withHealth(web("web:1.1", 2)))

	reached := stallProbes(s.harness, func(c docker.Container) bool { return c.DeploymentID > v2.ID && c.Replica == 2 })
	d, err := s.engine.Rollback(ctx, "web", 0)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	<-reached
	s.restart()

	got := s.wantResumed(d.ID)
	if got.Kind != api.KindRollback || got.Image != "web:1.0" {
		t.Errorf("the resumed deployment is a %s of %s, want the rollback to web:1.0", got.Kind, got.Image)
	}
	containers := s.rt.Containers()
	if len(containers) != 2 {
		t.Fatalf("containers = %s, want two", names(containers))
	}
	for _, c := range containers {
		if c.DeploymentID != d.ID || c.Image != "web:1.0" || !c.Running {
			t.Errorf("unexpected container after the rollback: %+v", c)
		}
	}
	if history := p.history("web.example.com"); slices.Contains(history, "") {
		t.Errorf("the domain was without a backend at some point: %q", history)
	}
}

// The server reboots.

func TestAfterARebootEveryReplicaIsStartedAgainAsItsRestartPolicyAllows(t *testing.T) {
	// A server that shuts down stops its containers with SIGTERM, and each
	// ends with whatever code its process chooses; one that loses power
	// leaves them with 255.
	for _, tc := range []struct {
		policy  string
		exit    int
		started bool
	}{
		{spec.RestartAlways, 0, true},
		{spec.RestartAlways, 255, true},
		{spec.RestartOnFailure, 0, false},
		{spec.RestartOnFailure, 255, true},
		{spec.RestartNever, 0, false},
		{spec.RestartNever, 255, false},
	} {
		t.Run(fmt.Sprintf("%s after exit %d", tc.policy, tc.exit), func(t *testing.T) {
			s, p := newRouted(t)
			rec := notified(s.harness)
			a := web("web:1.0", 2)
			a.Restart.Policy = tc.policy
			d := s.deploy(a)

			s.stopAgent()
			for _, c := range s.rt.Containers() {
				s.rt.Crash(c.ID, tc.exit)
			}
			s.startAgent()
			s.advance(time.Second)
			s.advance(time.Second)
			// Found down is reported down, whatever happens a second later.
			if !slices.Contains(rec.Kinds(), notify.ApplicationDown) {
				t.Errorf("notifications = %v, want application.down", rec.Kinds())
			}

			up := running(s.rt.Containers())
			events := strings.Join(s.appEvents(t, "web"), "\n")
			if tc.started {
				if up != "shipwick_web_1_1 shipwick_web_1_2" {
					t.Fatalf("running after the reboot = %q, want both replicas\n%s", up, events)
				}
				if got := p.upstreams("web.example.com"); len(got) != 2 {
					t.Errorf("serving %v, want both replicas", got)
				}
				if want := fmt.Sprintf("Replica 1 exited with code %d; restarting in 1s", tc.exit); !strings.Contains(events, want) {
					t.Errorf("events lack %q:\n%s", want, events)
				}
			} else {
				if up != "" {
					t.Fatalf("running after the reboot = %q, want none: the policy says so", up)
				}
				if want := fmt.Sprintf("Replica 1 exited with code %d; restart policy %q leaves it stopped", tc.exit, tc.policy); !strings.Contains(events, want) {
					t.Errorf("events lack %q:\n%s", want, events)
				}
				// Not forgotten: it is the operator's to start.
				if err := s.engine.Start(context.Background(), "web"); err != nil {
					t.Fatalf("Start: %v", err)
				}
				if up := running(s.rt.Containers()); up != "shipwick_web_1_1 shipwick_web_1_2" {
					t.Errorf("running after shipwick start = %q", up)
				}
			}
			// The same containers, not new ones: what they kept on their own
			// filesystem is still there.
			for _, c := range s.rt.Containers() {
				if c.DeploymentID != d.ID {
					t.Errorf("unexpected container %s", c.Name)
				}
			}
		})
	}
}

func TestAPowerLossInTheMiddleOfARolloutResumesItWithTheContainersThatExist(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(withHealth(web("web:1.0", 2)))

	reached := stallProbes(s.harness, func(c docker.Container) bool { return c.DeploymentID != v1.ID && c.Replica == 2 })
	d := s.begin(withHealth(web("web:1.1", 2)))
	<-reached
	before := names(s.rt.Containers())

	s.stopAgent()
	s.powerOff()
	s.startAgent()

	s.wantResumed(d.ID)
	if got := running(s.rt.Containers()); got != "shipwick_web_2_1 shipwick_web_2_2" || len(s.rt.Containers()) != 2 {
		t.Errorf("containers = %s, want the two replicas of the new version (before the power loss: %s)", names(s.rt.Containers()), before)
	}
	for _, c := range s.rt.Containers() {
		if n := s.rt.Starts(c.ID); n != 2 {
			t.Errorf("%s was started %d times, want once by the rollout and once after the power loss", c.Name, n)
		}
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v, want both replicas", got)
	}
}

// The proxy cannot be reached.

func TestWhileTheProxyIsUnreachableReplicasAreStillRestartedAndRoutesCatchUpWhenItIsBack(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(web("web:1.0", 2))
	synced := p.configs()

	p.failWith(errors.New("cannot reach Caddy's admin endpoint: dial unix /run/caddy/admin.sock: connect: connection refused"))
	s.rt.Crash(s.container(t, 1).ID, 137)
	s.advance(time.Second)
	s.advance(time.Second)
	if got := running(s.rt.Containers()); got != "shipwick_web_1_1 shipwick_web_1_2" {
		t.Fatalf("running = %q: supervision must not wait for the proxy", got)
	}
	// Who serves is a matter of names on the network, which the proxy looks
	// up by itself: the restarted replica is found without a word from the agent.
	if got := s.rt.Resolve("web"); len(got) != 2 {
		t.Errorf("the name resolves to %v, want both replicas", got)
	}

	// A second application arrives only once the proxy can be told.
	other := app("blog", "blog:1.0", 1)
	other.Domain = "blog.example.com"
	failed := s.deploy(other)
	if failed.Status != api.StatusFailed || !strings.Contains(failed.Error, "could not route blog.example.com") || !strings.Contains(failed.Error, "connection refused") {
		t.Fatalf("a deployment while the proxy is unreachable: %s %q", failed.Status, failed.Error)
	}
	if p.configs() != synced {
		t.Error("the proxy was given a configuration while it was unreachable")
	}

	p.failWith(nil)
	if d := s.deploy(other); d.Status != api.StatusActive {
		t.Fatalf("after the proxy is back: %s (%s)", d.Status, d.Error)
	}
	if got := p.upstreams("blog.example.com"); len(got) != 1 {
		t.Errorf("blog is served by %v", got)
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("web is served by %v", got)
	}
}

func TestAnApplicationWithoutADomainIsNotDeployedEitherWhileTheProxyIsUnreachable(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(app("worker", "worker:1.0", 1))

	p.failWith(errors.New("cannot reach Caddy's admin endpoint: connection refused"))
	v2 := s.deploy(app("worker", "worker:1.1", 1))
	if v2.Status != api.StatusFailed || !strings.Contains(v2.Error, "could not route worker to the new version") {
		t.Fatalf("v2: %s %q; one configuration holds every application's routes, and it could not be written", v2.Status, v2.Error)
	}
	containers := s.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("containers = %+v, want the old version untouched", containers)
	}
}

// A pull that does not end.

func TestAPullThatStallsEndsWithTheDeploymentAndLeavesTheRunningVersionAlone(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy(app("my-api", "my-api:1.0", 1))

	h.engine.opts.DeployTimeout = 50 * time.Millisecond
	h.rt.PullDelay = time.Hour
	d := h.deploy(app("my-api", "my-api:1.1", 1))
	if d.Status != api.StatusFailed || d.Error != "deployment timed out after 50ms" || d.CompletedAt == nil {
		t.Fatalf("deployment: %s %q, completed %v", d.Status, d.Error, d.CompletedAt)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("containers = %+v, want the old version untouched", containers)
	}
	if !h.free("my-api") {
		t.Error("the application stays locked after the timeout")
	}
}

func TestAPullThatIsCutFailsTheDeploymentWithTheDaemonsWordsUnlessTheImageIsOnTheServer(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))

	h.rt.PullErr = errors.New(`pull my-api:1.1: failed to do request: Get "https://registry-1.docker.io/v2/": read tcp 172.17.0.2:51234->54.156.94.132:443: read: connection reset by peer`)
	d := h.deploy(app("my-api", "my-api:1.1", 1))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "connection reset by peer") {
		t.Fatalf("deployment: %s %q", d.Status, d.Error)
	}
	if got := running(h.rt.Containers()); got != "shipwick_my-api_1_1" {
		t.Errorf("running = %s, want the old version", got)
	}

	// The same image, already on the server from an earlier pull: the
	// registry is not needed to run it.
	h.rt.AddLocalImage("my-api:1.1")
	d = h.deploy(app("my-api", "my-api:1.1", 1))
	if d.Status != api.StatusActive {
		t.Fatalf("with a local copy: %s %q", d.Status, d.Error)
	}
	if steps := h.steps(d.ID); !strings.Contains(steps, "Could not pull my-api:1.1, using the local copy") {
		t.Errorf("the events do not say that the local copy was used:\n%s", steps)
	}
}
