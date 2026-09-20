package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// probeScript is a controllable stand-in for HTTP health checks.
type probeScript struct {
	mu    sync.Mutex
	fail  map[string]error // by container IP
	calls int
}

func (p *probeScript) Probe(_ context.Context, ip string, _ int, _ string, _ time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.fail[ip]
}

func (p *probeScript) setFailing(ip string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail == nil {
		p.fail = map[string]error{}
	}
	if err == nil {
		delete(p.fail, ip)
	} else {
		p.fail[ip] = err
	}
}

// supervised is a harness whose supervisor is ticked by hand on a synthetic clock.
type supervised struct {
	*harness
	probes *probeScript
	now    time.Time
}

func newSupervised(t *testing.T) *supervised {
	h := newHarness(t)
	probes := &probeScript{}
	h.engine.opts.Probe = probes.Probe
	h.engine.opts.StartupPollInterval = time.Millisecond
	h.engine.sup.inlineProbes = true
	return &supervised{harness: h, probes: probes, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// advance moves the clock forward and runs one supervision pass.
func (s *supervised) advance(d time.Duration) {
	s.now = s.now.Add(d)
	s.engine.sup.tick(context.Background(), s.now)
}

func (s *supervised) container(t *testing.T, replica int) docker.Container {
	t.Helper()
	for _, c := range s.rt.Containers() {
		if c.Replica == replica {
			return c
		}
	}
	t.Fatalf("no container for replica %d", replica)
	return docker.Container{}
}

func (s *supervised) appEvents(t *testing.T, name string) []string {
	t.Helper()
	events, err := s.engine.Events(context.Background(), name, 500)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	out := make([]string, len(events))
	for i, e := range events { // newest first → oldest first
		out[len(events)-1-i] = e.Message
	}
	return out
}

func withHealth(a spec.App) spec.App {
	a.Health = &spec.Health{Path: "/health", Interval: spec.Duration(10 * time.Second), Timeout: spec.Duration(time.Second), Retries: 3}
	return a
}

func TestSupervisorRestartsCrashedReplica(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	victim := s.container(t, 1)

	s.rt.Crash(victim.ID, 137)
	s.advance(time.Second) // notices the exit, schedules the restart 1s out
	if s.container(t, 1).Running {
		t.Fatal("the first backoff step (1s) must be honored, not skipped")
	}
	s.advance(time.Second)
	if !s.container(t, 1).Running {
		t.Fatal("replica was not restarted after its backoff elapsed")
	}
	if got := s.rt.Starts(s.container(t, 2).ID); got != 1 {
		t.Errorf("the healthy replica was touched: %d starts", got)
	}

	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppHealthy || detail.Containers[0].Restarts != 1 {
		t.Errorf("after recovery: status=%s restarts=%d", detail.Status, detail.Containers[0].Restarts)
	}
	events := strings.Join(s.appEvents(t, "my-api"), "\n")
	for _, want := range []string{"Replica 1 exited with code 137; restarting in 1s", "Replica 1 restarted (attempt 1)"} {
		if !strings.Contains(events, want) {
			t.Errorf("missing event %q in:\n%s", want, events)
		}
	}
}

func TestSupervisorBackoffAndCrashLoop(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID

	// From now on the container dies the moment it is started.
	s.rt.CrashImages["my-api:1.0"] = true
	s.rt.Crash(id, 1)

	// Tick every 100ms and record when each restart happens.
	var restartTimes []time.Duration
	start := s.now
	for elapsed := time.Duration(0); elapsed < 12*time.Minute; elapsed += 100 * time.Millisecond {
		before := s.rt.Starts(id)
		s.advance(100 * time.Millisecond)
		if s.rt.Starts(id) > before {
			restartTimes = append(restartTimes, s.now.Sub(start))
		}
	}

	// Gaps between consecutive restarts must follow 1s, 2s, 5s, 10s, 30s and
	// then settle at the crash-loop delay.
	wantGaps := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 5 * time.Minute, 5 * time.Minute}
	if len(restartTimes) != len(wantGaps) {
		t.Fatalf("got %d restarts in 12m (%v), want %d: a crash-looping replica must not be restarted hot",
			len(restartTimes), restartTimes, len(wantGaps))
	}
	prev := time.Duration(0)
	for i, at := range restartTimes {
		gap := at - prev
		// Each restart is detected on the following tick, hence the slack.
		if gap < wantGaps[i] || gap > wantGaps[i]+300*time.Millisecond {
			t.Errorf("restart %d came %s after the previous one, want ~%s", i+1, gap, wantGaps[i])
		}
		prev = at
	}

	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppCrashLoop || !detail.Containers[0].CrashLoop {
		t.Errorf("status = %s, crash_loop = %v; want CRASH_LOOP", detail.Status, detail.Containers[0].CrashLoop)
	}
	apps, _ := s.engine.Applications(context.Background())
	if apps[0].Status != api.AppCrashLoop {
		t.Errorf("list view status = %s, want CRASH_LOOP", apps[0].Status)
	}

	events := s.appEvents(t, "my-api")
	loops := 0
	for _, e := range events {
		if strings.Contains(e, "is crash-looping") {
			loops++
		}
	}
	if loops != 1 {
		t.Errorf("the crash loop should be announced exactly once, got %d in:\n%s", loops, strings.Join(events, "\n"))
	}
}

func TestSupervisorForgivesAfterStableRun(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID

	// Crash-loop it...
	s.rt.CrashImages["my-api:1.0"] = true
	s.rt.Crash(id, 1)
	for range 600 {
		s.advance(100 * time.Millisecond)
	}
	if _, looping := s.engine.sup.snapshot(id); !looping {
		t.Fatal("precondition: replica should be crash-looping")
	}

	// ...then fix whatever was wrong (say, the database came back).
	delete(s.rt.CrashImages, "my-api:1.0")
	for !s.container(t, 1).Running {
		s.advance(10 * time.Second)
	}
	s.advance(30 * time.Second)
	if _, looping := s.engine.sup.snapshot(id); !looping {
		t.Error("30s of uptime is not yet proof of recovery")
	}
	s.advance(time.Minute)
	if _, looping := s.engine.sup.snapshot(id); looping {
		t.Error("after StableAfter of uptime the crash loop should be cleared")
	}

	// A later, unrelated crash starts over at the first backoff step.
	s.rt.Crash(id, 1)
	s.advance(time.Second)
	s.advance(time.Second)
	if !s.container(t, 1).Running {
		t.Error("after forgiveness the next crash should be retried after 1s again")
	}
	if !strings.Contains(strings.Join(s.appEvents(t, "my-api"), "\n"), "no longer crash-looping") {
		t.Error("recovery should be recorded")
	}
}

func TestSupervisorRestartPolicies(t *testing.T) {
	tests := []struct {
		policy      string
		exitCode    int
		wantRestart bool
	}{
		{spec.RestartAlways, 0, true},
		{spec.RestartAlways, 1, true},
		{spec.RestartOnFailure, 0, false},
		{spec.RestartOnFailure, 1, true},
		{spec.RestartNever, 1, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/exit%d", tt.policy, tt.exitCode), func(t *testing.T) {
			s := newSupervised(t)
			a := app("my-api", "my-api:1.0", 1)
			a.Restart.Policy = tt.policy
			s.deploy(a)

			s.rt.Crash(s.container(t, 1).ID, tt.exitCode)
			for range 50 {
				s.advance(time.Second)
			}
			if got := s.container(t, 1).Running; got != tt.wantRestart {
				t.Errorf("running = %v, want %v", got, tt.wantRestart)
			}
			if !tt.wantRestart {
				events := s.appEvents(t, "my-api")
				if len(events) != 1 || !strings.Contains(events[0], "leaves it stopped") {
					t.Errorf("the decision should be explained once, got %v", events)
				}
			}
		})
	}
}

func TestSupervisorLeavesStoppedApplicationsAlone(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	if err := s.engine.Stop(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		s.advance(time.Second)
	}
	for _, c := range s.rt.Containers() {
		if c.Running {
			t.Errorf("the supervisor resurrected a replica of a stopped application: %+v", c)
		}
	}
}

func TestStartClearsRestartHistory(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID
	s.rt.CrashImages["my-api:1.0"] = true
	s.rt.Crash(id, 1)
	for range 600 {
		s.advance(100 * time.Millisecond)
	}

	delete(s.rt.CrashImages, "my-api:1.0")
	s.engine.Stop(context.Background(), "my-api")
	s.engine.Start(context.Background(), "my-api")

	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppHealthy {
		t.Errorf("status = %s; an explicit start should wipe the crash loop", detail.Status)
	}
}

func TestSupervisorSkipsApplicationsBeingDeployed(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	id := s.container(t, 1).ID

	s.rt.PullDelay = 300 * time.Millisecond
	if _, err := s.engine.Deploy(context.Background(), app("my-api", "my-api:1.1", 1)); err != nil {
		t.Fatal(err)
	}
	s.rt.Crash(id, 1)
	s.advance(time.Second)
	s.advance(2 * time.Second)
	if s.rt.Starts(id) != 1 {
		t.Error("the supervisor acted on an application locked by a deployment")
	}
	s.engine.Wait()
}

func TestUserOperationWaitsForSupervisorLock(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))

	// The supervisor holds the application, as it does while restarting a replica.
	if wait, err := s.engine.tryLock("my-api", true); wait != nil || err != nil {
		t.Fatalf("tryLock: %v %v", wait, err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(released)
		s.engine.unlock("my-api")
	}()

	// A deploy arriving meanwhile must wait it out rather than fail with 409.
	_, err := s.engine.Deploy(context.Background(), app("my-api", "my-api:1.1", 1))
	if err != nil {
		t.Fatalf("Deploy while the supervisor held the lock: %v", err)
	}
	select {
	case <-released:
	default:
		t.Error("Deploy returned before the supervisor released the lock")
	}
	s.engine.Wait()

	// Whereas a user operation in the way is reported immediately.
	s.rt.PullDelay = 300 * time.Millisecond
	s.engine.Deploy(context.Background(), app("my-api", "my-api:1.2", 1))
	start := time.Now()
	if err := s.engine.Stop(context.Background(), "my-api"); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("a conflict between two user operations should be reported at once")
	}
	s.engine.Wait()
}

func TestLockWaitGivesUp(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.LockWait = 50 * time.Millisecond
	s.deploy(app("my-api", "my-api:1.0", 1))
	s.engine.tryLock("my-api", true)
	defer s.engine.unlock("my-api")

	if err := s.engine.Stop(context.Background(), "my-api"); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy after LockWait", err)
	}
}

// ---- health ----

func TestDeployWaitsForHealthChecks(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.StartupPollInterval = 5 * time.Millisecond

	// The app needs a moment before it answers, like any real one.
	var mu sync.Mutex
	calls := 0
	s.engine.opts.Probe = func(context.Context, string, int, string, time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		if calls++; calls < 4 {
			return errors.New("connection refused")
		}
		return nil
	}

	d := s.deploy(withHealth(app("my-api", "my-api:1.0", 1)))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if calls < 4 {
		t.Errorf("deployment went active after %d probes; it must wait for a passing one", calls)
	}
	var sawStep bool
	for _, e := range s.events(d.ID) {
		sawStep = sawStep || strings.Contains(e.Message, "passed health checks")
	}
	if !sawStep {
		t.Error("expected a 'passed health checks' step")
	}
}

func TestDeployFailsWhenNeverHealthy(t *testing.T) {
	s := newSupervised(t)
	v1 := s.deploy(app("my-api", "my-api:1.0", 1))

	s.engine.opts.StartupPollInterval = 5 * time.Millisecond
	s.engine.opts.Probe = func(context.Context, string, int, string, time.Duration) error { return errors.New("HTTP 503") }
	bad := withHealth(app("my-api", "my-api:1.1", 2))
	bad.Health.Interval, bad.Health.Retries = spec.Duration(20*time.Millisecond), 3 // budget: 60ms

	d := s.deploy(bad)
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	for _, want := range []string{"did not become healthy within 60ms", "GET /health", "port 8080", "HTTP 503"} {
		if !strings.Contains(d.Error, want) {
			t.Errorf("error %q should mention %q", d.Error, want)
		}
	}
	containers := s.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("the previous version must survive untouched: %+v", containers)
	}
}

func TestDeployFailsFastWhenReplicaDiesDuringHealthChecks(t *testing.T) {
	s := newSupervised(t)
	s.rt.CrashImages["my-api:broken"] = true
	a := withHealth(app("my-api", "my-api:broken", 1))
	a.Health.Interval = spec.Duration(time.Hour) // a budget nobody should sit through

	start := time.Now()
	d := s.deploy(a)
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "exited with code 1 before it became healthy") {
		t.Errorf("unexpected deployment: %s %q", d.Status, d.Error)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a dead container must fail the deployment at once, not after the health budget")
	}
}

func TestSupervisorMarksAndRestartsUnhealthyReplica(t *testing.T) {
	s := newSupervised(t)
	s.deploy(withHealth(app("my-api", "my-api:1.0", 2)))
	sick := s.container(t, 1)

	s.advance(time.Second)
	if h, _ := s.engine.sup.snapshot(sick.ID); h != api.HealthHealthy {
		t.Fatalf("health = %q after a passing probe, want healthy", h)
	}

	// The process hangs: still running, no longer answering.
	s.probes.setFailing(sick.IP, errors.New("no response within 1s"))
	s.advance(10 * time.Second)
	s.advance(10 * time.Second)
	if h, _ := s.engine.sup.snapshot(sick.ID); h != api.HealthHealthy {
		t.Errorf("two failures are below retries=3; health = %q", h)
	}
	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppHealthy {
		t.Errorf("status = %s; a blip must not degrade the application", detail.Status)
	}

	s.advance(10 * time.Second) // third failure in a row
	detail, _ = s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppDegraded || detail.Replicas != (api.ReplicaCount{Desired: 2, Running: 2, Healthy: 1}) {
		t.Errorf("after 3 failures: status=%s replicas=%+v", detail.Status, detail.Replicas)
	}

	s.probes.setFailing(sick.IP, nil) // a restart cures it
	s.advance(time.Second)            // the supervisor acts on the verdict
	if got := s.rt.Starts(sick.ID); got != 2 {
		t.Fatalf("starts = %d, want 2: an unhealthy replica should be restarted", got)
	}
	s.advance(time.Second)
	detail, _ = s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppHealthy || detail.Containers[0].Health != api.HealthHealthy || detail.Containers[0].Restarts != 1 {
		t.Errorf("after the restart: status=%s container=%+v", detail.Status, detail.Containers[0])
	}

	events := strings.Join(s.appEvents(t, "my-api"), "\n")
	for _, want := range []string{"Replica 1 failed 3 health checks in a row: no response within 1s", "Replica 1 restarted", "Replica 1 is healthy"} {
		if !strings.Contains(events, want) {
			t.Errorf("missing event %q in:\n%s", want, events)
		}
	}
}

func TestSupervisorGivesRestartedReplicaItsStartupBudget(t *testing.T) {
	s := newSupervised(t)
	s.deploy(withHealth(app("my-api", "my-api:1.0", 1))) // budget: 10s × 3 = 30s
	c := s.container(t, 1)
	s.advance(time.Second)

	s.rt.Crash(c.ID, 1)
	s.advance(time.Second)
	s.advance(time.Second) // restarted; from here on it boots slowly
	s.probes.setFailing(s.container(t, 1).IP, errors.New("connection refused"))

	for range 25 {
		s.advance(time.Second)
	}
	if h, _ := s.engine.sup.snapshot(c.ID); h != api.HealthStarting {
		t.Errorf("health = %q; failing probes within the startup budget are 'starting', not 'unhealthy'", h)
	}
	if s.rt.Starts(c.ID) != 2 {
		t.Errorf("starts = %d; a booting replica must not be restarted for failing probes", s.rt.Starts(c.ID))
	}

	for range 10 {
		s.advance(time.Second)
	}
	if s.rt.Starts(c.ID) < 3 {
		t.Error("once the startup budget is spent, a replica that never came up should be restarted")
	}
}

func TestNeverHealthyReplicaEndsInCrashLoop(t *testing.T) {
	s := newSupervised(t)
	a := withHealth(app("my-api", "my-api:1.0", 1))
	a.Health.Interval = spec.Duration(time.Second) // budget: 3s
	s.deploy(a)
	s.advance(time.Second)

	// It runs, but never answers again — restarting does not help.
	s.probes.setFailing(s.container(t, 1).IP, errors.New("HTTP 500"))
	for range 600 {
		s.advance(time.Second)
	}

	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppCrashLoop {
		t.Errorf("status = %s, want CRASH_LOOP: running-but-never-healthy is a crash loop too", detail.Status)
	}
	if starts := s.rt.Starts(s.container(t, 1).ID); starts > 10 {
		t.Errorf("%d starts in 10 minutes: unhealthy restarts must be rate-limited like crashes", starts)
	}
}

func TestRestartPolicyNeverLeavesUnhealthyReplicaRunning(t *testing.T) {
	s := newSupervised(t)
	a := withHealth(app("my-api", "my-api:1.0", 1))
	a.Restart.Policy = spec.RestartNever
	s.deploy(a)
	c := s.container(t, 1)
	s.probes.setFailing(c.IP, errors.New("HTTP 500"))
	for range 10 {
		s.advance(10 * time.Second)
	}
	if s.rt.Starts(c.ID) != 1 {
		t.Error("policy 'never' means never, for unhealthy replicas too")
	}
	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppDown || detail.Containers[0].Health != api.HealthUnhealthy {
		t.Errorf("status = %s, health = %q; it should still be reported", detail.Status, detail.Containers[0].Health)
	}
}

func TestUnknownHealthDoesNotFlapAfterAgentRestart(t *testing.T) {
	s := newSupervised(t)
	s.deploy(withHealth(app("my-api", "my-api:1.0", 2)))

	// No tick has run yet: exactly the situation right after an agent restart.
	detail, _ := s.engine.Application(context.Background(), "my-api")
	if detail.Status != api.AppHealthy {
		t.Errorf("status = %s; not having probed yet is no reason to report DOWN", detail.Status)
	}
}

func TestSupervisorForgetsRemovedContainers(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	s.advance(time.Second)
	s.deploy(app("my-api", "my-api:1.1", 1)) // replaces the container
	s.advance(time.Second)

	s.engine.sup.mu.Lock()
	defer s.engine.sup.mu.Unlock()
	if len(s.engine.sup.states) != 1 {
		t.Errorf("state kept for %d containers, want 1: memory must not grow with every deployment", len(s.engine.sup.states))
	}
}

func TestSupervisorLoopStopsOnShutdown(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.SuperviseInterval = 5 * time.Millisecond
	s.engine.sup.inlineProbes = false
	s.deploy(withHealth(app("my-api", "my-api:1.0", 1)))
	s.engine.StartSupervisor()

	s.rt.Crash(s.container(t, 1).ID, 1)
	deadline := time.Now().Add(5 * time.Second)
	for !s.container(t, 1).Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.container(t, 1).Running {
		t.Fatal("the running supervisor loop did not restart the replica")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.engine.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with the supervisor running: %v", err)
	}
}

func TestShortDuration(t *testing.T) {
	tests := map[time.Duration]string{
		time.Second:               "1s",
		30 * time.Second:          "30s",
		5 * time.Minute:           "5m",
		90 * time.Second:          "1m30s",
		time.Hour:                 "1h",
		60 * time.Millisecond:     "60ms",
		2*time.Hour + time.Minute: "2h1m",
	}
	for d, want := range tests {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
