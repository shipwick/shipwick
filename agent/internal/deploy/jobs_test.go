package deploy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func withHook(a spec.App, command ...string) spec.App {
	a.PreDeploy = &spec.Hook{Command: command, Timeout: spec.Duration(spec.DefaultHookTimeout)}
	return a
}

func withJob(a spec.App, name, schedule string, timeout time.Duration, command ...string) spec.App {
	a.Jobs = append(a.Jobs, spec.Job{Name: name, Schedule: schedule, Command: command, Timeout: spec.Duration(timeout)})
	return a
}

func (h *harness) runs(name string) []api.Run {
	h.t.Helper()
	runs, err := h.engine.Runs(context.Background(), name, "", 0)
	if err != nil {
		h.t.Fatalf("Runs: %v", err)
	}
	return runs
}

// awaitCompleted polls a deployment until the engine is done with it, the way
// clients do. Unlike Wait it does not wait for jobs that happen to be running.
func (h *harness) awaitCompleted(id int64) store.Deployment {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		d, err := h.store.GetDeployment(context.Background(), id)
		if err != nil {
			h.t.Fatalf("GetDeployment: %v", err)
		}
		if d.CompletedAt != nil {
			return d
		}
		select {
		case <-poll.C:
		case <-deadline:
			h.t.Fatalf("deployment %d did not complete", id)
		}
	}
}

func (h *harness) appEventMessages(name string) []string {
	h.t.Helper()
	events, err := h.engine.Events(context.Background(), name, 500)
	if err != nil {
		h.t.Fatalf("Events: %v", err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Message)
	}
	return out
}

func TestPreDeployHookRunsBeforeTheReplicasAndIsRecorded(t *testing.T) {
	h := newHarness(t)
	a := withHook(app("my-api", "my-api:1.0", 1), "dotnet", "Migrate.dll")
	a.Entrypoint = []string{"/entry.sh"}
	a.Volumes = []spec.Volume{{Name: "data", Path: "/data"}}
	a.Deploy.Strategy = spec.StrategyRecreate

	var hookSpec docker.ContainerSpec
	h.rt.CreateHook = func(s docker.ContainerSpec) error {
		if s.Job != nil {
			hookSpec = s
		}
		return nil
	}
	d := h.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("deployment: %+v", d)
	}

	if hookSpec.Job == nil || hookSpec.Job.Name != "pre-deploy" || hookSpec.Command[1] != "Migrate.dll" || hookSpec.Entrypoint[0] != "/entry.sh" {
		t.Errorf("hook container spec: %+v", hookSpec)
	}
	if hookSpec.Env["SECRET"] != "hunter2" || hookSpec.Image != "my-api:1.0" || hookSpec.MemoryBytes != 256<<20 {
		t.Errorf("the hook must run from the deployment's image with its env and limits: %+v", hookSpec)
	}
	if len(hookSpec.Mounts) != 0 {
		t.Errorf("a job must never mount the application's volumes: %+v", hookSpec.Mounts)
	}

	var steps []string
	for _, e := range h.events(d.ID) {
		if e.Type == api.EventStep {
			steps = append(steps, e.Message)
		}
	}
	joined := strings.Join(steps, "\n")
	if !strings.Contains(joined, "Running pre-deploy command") || !strings.Contains(joined, "Pre-deploy command finished (") {
		t.Errorf("hook steps missing from %q", joined)
	}
	if i, j := strings.Index(joined, "Pre-deploy command finished"), strings.Index(joined, "Started 1 container"); i < 0 || j < 0 || i > j {
		t.Errorf("the hook must finish before any replica starts:\n%s", joined)
	}

	runs := h.runs("my-api")
	if len(runs) != 1 || runs[0].Kind != api.RunKindHook || runs[0].Job != "pre-deploy" || runs[0].Status != api.RunSucceeded ||
		runs[0].ExitCode == nil || *runs[0].ExitCode != 0 || runs[0].DeploymentID == nil || *runs[0].DeploymentID != d.ID {
		t.Errorf("hook run: %+v", runs)
	}
	if got := h.rt.JobContainers(); len(got) != 0 {
		t.Errorf("job container not removed: %+v", got)
	}
}

func TestFailingHookFailsTheDeploymentBeforeAnyReplicaExists(t *testing.T) {
	h := newHarness(t)
	h.rt.JobExits["pre-deploy"] = 1

	d := h.deploy(withHook(app("my-api", "my-api:1.0", 2), "migrate"))
	if d.Status != api.StatusFailed || d.Error != "pre-deploy command exited 1" {
		t.Fatalf("deployment: status=%s error=%q", d.Status, d.Error)
	}
	if n := len(h.rt.Containers()); n != 0 {
		t.Errorf("%d containers exist; a failed hook must leave none", n)
	}

	var output string
	for _, e := range h.events(d.ID) {
		if e.Type == api.EventLog {
			output = e.Message
		}
	}
	if !strings.HasPrefix(output, "Last output of the pre-deploy command:") || !strings.Contains(output, "log line from shipwick_my-api_job_pre-deploy_") {
		t.Errorf("the hook's output must be in the events: %q", output)
	}
	if runs := h.runs("my-api"); len(runs) != 1 || runs[0].Status != api.RunFailed || *runs[0].ExitCode != 1 {
		t.Errorf("hook run: %+v", runs)
	}
}

func TestFailingHookLeavesTheRunningVersionAloneUnderRecreate(t *testing.T) {
	h := newHarness(t)
	a := app("db", "db:1.0", 1)
	a.Deploy.Strategy = spec.StrategyRecreate
	v1 := h.deploy(a)

	h.rt.JobExits["pre-deploy"] = 2
	v2 := h.deploy(withHook(a, "migrate"))
	if v2.Status != api.StatusFailed {
		t.Fatalf("v2: %+v", v2)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].DeploymentID != v1.ID || !containers[0].Running {
		t.Errorf("the hook runs before the old version is stopped; v1 must still run: %+v", containers)
	}
}

func TestHookTimeoutFailsTheDeployment(t *testing.T) {
	h := newHarness(t)
	h.rt.HoldJobs = true
	a := withHook(app("my-api", "my-api:1.0", 1), "migrate")
	a.PreDeploy.Timeout = spec.Duration(time.Millisecond)

	d := h.deploy(a)
	if d.Status != api.StatusFailed || d.Error != "pre-deploy command timed out after 1ms" {
		t.Fatalf("deployment: status=%s error=%q", d.Status, d.Error)
	}
	if runs := h.runs("my-api"); len(runs) != 1 || runs[0].Status != api.RunTimedOut || runs[0].ExitCode != nil {
		t.Errorf("hook run: %+v", runs)
	}
	if got := h.rt.JobContainers(); len(got) != 0 {
		t.Errorf("timed-out job container not removed: %+v", got)
	}
}

func TestScheduledJobRunsWhenDueAndOncePerMinute(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(withJob(app("my-api", "my-api:1.0", 1), "nightly", "0 3 * * *", time.Hour, "node", "report.js"))

	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	h.engine.scheduleJobs(ctx, day.Add(2*time.Hour+59*time.Minute))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 0 {
		t.Fatalf("ran before it was due: %+v", runs)
	}

	h.engine.scheduleJobs(ctx, day.Add(3*time.Hour+5*time.Second))
	h.engine.scheduleJobs(ctx, day.Add(3*time.Hour+40*time.Second))
	h.engine.Wait()
	runs := h.runs("my-api")
	if len(runs) != 1 || runs[0].Kind != api.RunKindScheduled || runs[0].Job != "nightly" || runs[0].Status != api.RunSucceeded || runs[0].Command[1] != "report.js" {
		t.Fatalf("runs after 03:00: %+v", runs)
	}
	if got := h.rt.JobContainers(); len(got) != 0 {
		t.Errorf("job container not removed: %+v", got)
	}
	if n := len(h.rt.Containers()); n != 1 {
		t.Errorf("%d containers, want the one replica", n)
	}

	// A stall across the scheduled minute: the job runs once, not once per
	// missed minute, and a stall across nothing runs nothing.
	h.engine.scheduleJobs(ctx, day.Add(24*time.Hour+3*time.Hour+10*time.Minute))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 2 {
		t.Errorf("a missed 03:00 must be run once: %d runs", len(runs))
	}
	h.engine.scheduleJobs(ctx, day.Add(24*time.Hour+5*time.Hour))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 2 {
		t.Errorf("nothing was due between 03:10 and 05:00: %d runs", len(runs))
	}
	if events := h.appEventMessages("my-api"); len(events) != 0 {
		t.Errorf("successful runs record no events: %v", events)
	}
}

func TestSchedulerStartsFromItsFirstTickAndSkipsStoppedApplications(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(withJob(app("my-api", "my-api:1.0", 1), "hourly", "0 * * * *", time.Hour, "x"))

	// The first tick considers only its own minute: 03:00 passed while the
	// agent was down and is not caught up.
	h.engine.scheduleJobs(ctx, time.Date(2026, 3, 1, 3, 10, 0, 0, time.UTC))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 0 {
		t.Errorf("a first tick must not catch up: %+v", runs)
	}

	if err := h.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	h.engine.scheduleJobs(ctx, time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 0 {
		t.Errorf("a stopped application runs no jobs: %+v", runs)
	}
	jobs, _ := h.engine.Jobs(ctx, "my-api")
	if len(jobs) != 1 || jobs[0].NextRunAt != nil {
		t.Errorf("a stopped application has no next run: %+v", jobs)
	}

	if err := h.engine.Start(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	h.engine.scheduleJobs(ctx, time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC))
	h.engine.Wait()
	if runs := h.runs("my-api"); len(runs) != 1 {
		t.Errorf("runs after start: %+v", runs)
	}
}

func TestARunningJobIsNotStartedTwice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.HoldJobs = true
	h.deploy(withJob(app("my-api", "my-api:1.0", 1), "nightly", "0 3 * * *", time.Hour, "x"))

	first, err := h.engine.RunJob(ctx, "my-api", "nightly")
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); !errors.Is(err, ErrJobRunning) {
		t.Errorf("second RunJob = %v, want ErrJobRunning", err)
	}
	h.engine.scheduleJobs(ctx, time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC))
	if runs := h.runs("my-api"); len(runs) != 1 || runs[0].ID != first.ID || runs[0].Status != api.RunRunning {
		t.Errorf("the schedule must not start a second run: %+v", runs)
	}
	if _, err := h.engine.RunJob(ctx, "my-api", "no-such-job"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RunJob(unknown) = %v, want ErrNotFound", err)
	}

	if !h.rt.ReleaseJob("nightly", 1) {
		t.Fatal("no running job container to release")
	}
	h.engine.Wait()
	runs := h.runs("my-api")
	if len(runs) != 1 || runs[0].Status != api.RunFailed || *runs[0].ExitCode != 1 || runs[0].Kind != api.RunKindManual {
		t.Errorf("finished run: %+v", runs)
	}
	if events := h.appEventMessages("my-api"); len(events) != 1 || events[0] != "Job nightly failed (exit 1)" {
		t.Errorf("events: %v", events)
	}
	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Errorf("RunJob after the first run ended: %v", err)
	}
}

func TestJobTimeoutStopsTheContainer(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.HoldJobs = true
	h.deploy(withJob(app("my-api", "my-api:1.0", 1), "slow", "* * * * *", time.Millisecond, "sleep"))

	if _, err := h.engine.RunJob(ctx, "my-api", "slow"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	runs := h.runs("my-api")
	if len(runs) != 1 || runs[0].Status != api.RunTimedOut || runs[0].ExitCode != nil || runs[0].FinishedAt == nil {
		t.Errorf("run: %+v", runs)
	}
	if got := h.rt.JobContainers(); len(got) != 0 {
		t.Errorf("job container not removed: %+v", got)
	}
	if events := h.appEventMessages("my-api"); len(events) != 1 || events[0] != "Job slow timed out after 1ms" {
		t.Errorf("events: %v", events)
	}
}

func TestRunCommandIsIndependentAndKeepsTheOutput(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.rt.HoldJobs = true

	if _, err := h.engine.RunCommand(ctx, "my-api", nil); !errors.As(err, new(*InvalidCommandError)) {
		t.Errorf("RunCommand(nil) = %v, want InvalidCommandError", err)
	}
	if _, err := h.engine.RunCommand(ctx, "ghost", []string{"x"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RunCommand(unknown app) = %v", err)
	}

	first, err := h.engine.RunCommand(ctx, "my-api", []string{"rails", "console"})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	second, err := h.engine.RunCommand(ctx, "my-api", []string{"rails", "db:seed"})
	if err != nil {
		t.Fatalf("a second command must run next to the first: %v", err)
	}
	if first.Job != "run" || first.Kind != api.RunKindManual || first.Status != api.RunRunning {
		t.Errorf("run: %+v", first)
	}
	got := h.rt.JobContainers()
	if len(got) != 2 {
		t.Fatalf("job containers: %+v", got)
	}
	for _, c := range got {
		s := h.rt.Spec(c.ID)
		if s.Command[0] != "rails" || s.Env["SECRET"] != "hunter2" || s.Job.RunID == 0 {
			t.Errorf("job container spec: %+v", s)
		}
	}

	// The listing is unordered; the run id in the spec says which is which.
	for _, c := range got {
		code := 0
		if h.rt.Spec(c.ID).Job.RunID == first.ID {
			code = 3
		}
		h.rt.Crash(c.ID, code)
	}
	h.engine.Wait()

	detail, err := h.engine.JobRun(ctx, "my-api", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != api.RunFailed || *detail.ExitCode != 3 || detail.Output != "log line from shipwick_my-api_job_run_"+strconv.FormatInt(first.ID, 10) {
		t.Errorf("first run: %+v", detail)
	}
	if d2, _ := h.engine.JobRun(ctx, "my-api", second.ID); d2.Status != api.RunSucceeded {
		t.Errorf("second run: %+v", d2)
	}
	if events := h.appEventMessages("my-api"); len(events) != 1 || events[0] != "Command rails failed (exit 3)" {
		t.Errorf("events: %v", events)
	}

	h.deploy(app("other", "other:1.0", 1))
	if _, err := h.engine.JobRun(ctx, "other", first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a run is only visible through its own application: %v", err)
	}
	h.rt.HoldJobs = false
	if _, err := h.engine.RunCommand(ctx, "other", []string{"x"}); err != nil {
		t.Errorf("RunCommand on another app: %v", err)
	}
	h.engine.Wait()
}

func TestRunCommandNeedsAnActiveDeployment(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.PullErr = errors.New("nope")
	h.deploy(app("my-api", "my-api:1.0", 1))
	if _, err := h.engine.RunCommand(ctx, "my-api", []string{"x"}); !errors.Is(err, ErrNotDeployed) {
		t.Errorf("RunCommand = %v, want ErrNotDeployed", err)
	}
	if _, err := h.engine.Jobs(ctx, "my-api"); !errors.Is(err, ErrNotDeployed) {
		t.Errorf("Jobs = %v, want ErrNotDeployed", err)
	}
}

func TestJobsViewShowsLastAndNextRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	a := withJob(app("my-api", "my-api:1.0", 1), "nightly", "0 3 * * *", 2*time.Hour, "node", "report.js")
	a = withJob(a, "cleanup", "*/15 * * * *", time.Hour, "cleanup")
	h.deploy(a)

	jobs, err := h.engine.Jobs(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].Name != "nightly" || jobs[0].Schedule != "0 3 * * *" || jobs[0].Timeout.Std() != 2*time.Hour || jobs[0].LastRun != nil {
		t.Fatalf("jobs: %+v", jobs)
	}
	for _, j := range jobs {
		if j.NextRunAt == nil || !j.NextRunAt.After(time.Now()) || j.NextRunAt.Second() != 0 || j.NextRunAt.Location() != time.UTC {
			t.Errorf("next run of %s: %v", j.Name, j.NextRunAt)
		}
	}

	h.rt.JobExits["nightly"] = 1
	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	jobs, _ = h.engine.Jobs(ctx, "my-api")
	if jobs[0].LastRun == nil || jobs[0].LastRun.Status != api.RunFailed || jobs[1].LastRun != nil {
		t.Errorf("last runs: %+v %+v", jobs[0].LastRun, jobs[1].LastRun)
	}
	if runs, _ := h.engine.Runs(ctx, "my-api", "cleanup", 10); len(runs) != 0 {
		t.Errorf("runs filtered by job: %+v", runs)
	}
}

func TestJobContainersAreNotReplicas(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	s.rt.HoldJobs = true
	v1 := s.deploy(withJob(app("my-api", "my-api:1.0", 2), "nightly", "0 3 * * *", time.Hour, "x"))
	if _, err := s.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Fatal(err)
	}

	// Not in the views.
	detail, err := s.engine.Application(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Containers) != 2 || detail.Replicas.Running != 2 || detail.Status != api.AppHealthy {
		t.Errorf("a job container must not count as a replica: %+v", detail.Application)
	}
	apps, _ := s.engine.Applications(ctx)
	if apps[0].Replicas.Running != 2 {
		t.Errorf("summary counts the job container: %+v", apps[0].Replicas)
	}
	if server, _ := s.engine.Server(ctx); server.Containers != 2 {
		t.Errorf("server counts the job container: %d", server.Containers)
	}

	// Not swept by the supervisor.
	s.advance(time.Second)
	if got := s.rt.JobContainers(); len(got) != 1 || !got[0].Running {
		t.Fatalf("the supervisor must leave a running job alone: %+v", got)
	}

	// Not retired by the next deployment: the job runs to its end. (Wait
	// would wait for the job too, so the deployment is followed by itself.)
	started, err := s.engine.Deploy(ctx, withJob(app("my-api", "my-api:1.1", 2), "nightly", "0 3 * * *", time.Hour, "x"))
	if err != nil {
		t.Fatal(err)
	}
	v2 := s.awaitCompleted(started.ID)
	if v2.Status != api.StatusActive {
		t.Fatalf("v2: %+v", v2)
	}
	got := s.rt.JobContainers()
	if len(got) != 1 || !got[0].Running || got[0].DeploymentID != v1.ID {
		t.Fatalf("a job of the old version must run to its end: %+v", got)
	}
	s.rt.ReleaseJob("nightly", 0)
	s.engine.Wait()
	if runs := s.runs("my-api"); len(runs) != 1 || runs[0].Status != api.RunSucceeded {
		t.Errorf("run: %+v", runs)
	}
}

func TestRecoverInterruptsRunsAndRemovesTheirContainers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	d := h.deploy(app("my-api", "my-api:1.0", 1))

	run, err := h.store.CreateJobRun(ctx, store.JobRun{ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: "nightly", Kind: api.RunKindScheduled, Command: []string{"x"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cspec := containerSpec("my-api", d.ID, d.Sequence, 0)
	cspec.Job = &docker.JobSpec{Name: "nightly", RunID: run.ID}
	orphan, _, _ := h.rt.CreateContainer(ctx, cspec)
	h.rt.StartContainer(ctx, orphan)
	foreign := containerSpec("unknown-app", 99, 1, 0)
	foreign.Job = &docker.JobSpec{Name: "theirs", RunID: 1}
	foreignID, _, _ := h.rt.CreateContainer(ctx, foreign)

	if err := h.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got, _ := h.store.GetJobRun(ctx, run.ID)
	if got.Status != api.RunInterrupted || got.FinishedAt == nil {
		t.Errorf("run after Recover: %+v", got)
	}
	for _, c := range h.rt.Containers() {
		switch {
		case c.ID == orphan:
			t.Error("the orphaned job container was not removed")
		case c.ID == foreignID:
		case c.DeploymentID == d.ID && c.Job == "":
		default:
			t.Errorf("unexpected container: %+v", c)
		}
	}
	if _, err := h.rt.InspectContainer(ctx, foreignID); err != nil {
		t.Error("Recover must never touch containers of applications it does not know")
	}
}

func TestDeleteRemovesJobContainers(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.HoldJobs = true
	h.deploy(app("my-api", "my-api:1.0", 1))
	if _, err := h.engine.RunCommand(ctx, "my-api", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got := h.rt.JobContainers(); len(got) != 1 {
		t.Fatalf("job containers: %+v", got)
	}
	if err := h.engine.Delete(ctx, "my-api"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := len(h.rt.Containers()); n != 0 {
		t.Errorf("%d containers left after delete", n)
	}
	h.engine.Wait() // the run's goroutine ends with its container
}

func TestShutdownInterruptsRunningJobs(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.HoldJobs = true
	h.deploy(app("my-api", "my-api:1.0", 1))
	run, err := h.engine.RunCommand(ctx, "my-api", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	got, _ := h.store.GetJobRun(ctx, run.ID)
	if got.Status != api.RunInterrupted {
		t.Errorf("run after shutdown: %+v", got)
	}
	if n := len(h.rt.JobContainers()); n != 0 {
		t.Errorf("%d job containers left after shutdown", n)
	}
	if _, err := h.engine.RunCommand(ctx, "my-api", []string{"x"}); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("RunCommand after shutdown = %v", err)
	}
}
