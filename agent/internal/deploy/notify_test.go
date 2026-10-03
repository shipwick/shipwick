package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/notify/notifytest"
	"github.com/shipwick/shipwick/pkg/api"
)

// notified gives a harness a recorder in place of a webhook.
func notified(h *harness) *notifytest.Recorder {
	rec := &notifytest.Recorder{}
	h.engine.opts.Notifier = rec
	return rec
}

func lastEvent(t *testing.T, rec *notifytest.Recorder, kind string) notify.Event {
	t.Helper()
	events := rec.Events()
	if len(events) == 0 {
		t.Fatalf("no notification, want %s", kind)
	}
	last := events[len(events)-1]
	if last.Kind != kind {
		t.Fatalf("last notification = %s %q, want %s", last.Kind, last.Message, kind)
	}
	return last
}

func TestSuccessfulDeploymentIsNotified(t *testing.T) {
	h := newHarness(t)
	rec := notified(h)

	d := h.deploy(app("my-api", "my-api:1.0", 2))
	ev := lastEvent(t, rec, notify.DeploymentSucceeded)
	if ev.Application != "my-api" || ev.DeploymentID != d.ID || ev.Version != "1.0" || ev.Message != "my-api is running 1.0" {
		t.Errorf("unexpected event: %+v", ev)
	}

	h.deploy(app("my-api", "my-api:1.1", 2))
	if ev := lastEvent(t, rec, notify.DeploymentSucceeded); ev.Message != "my-api is running 1.1, replacing 1.0" {
		t.Errorf("message = %q; the replaced version is worth a word", ev.Message)
	}

	h.finish(h.engine.Redeploy(context.Background(), "my-api", ""))
	if ev := lastEvent(t, rec, notify.DeploymentSucceeded); ev.Message != "my-api was redeployed and is running 1.1" {
		t.Errorf("message = %q", ev.Message)
	}
	if n := len(rec.Events()); n != 3 {
		t.Errorf("%d notifications for 3 deployments; nothing else deserves one", n)
	}
	for _, ev := range rec.Events() {
		if strings.Contains(ev.Message, "hunter2") {
			t.Errorf("notification leaks an env value: %q", ev.Message)
		}
	}
}

func TestFailedDeploymentIsNotifiedWithWhatStillRuns(t *testing.T) {
	h := newHarness(t)
	rec := notified(h)
	h.deploy(app("my-api", "my-api:1.0", 1))

	h.rt.CrashImages["my-api:1.1"] = true
	d := h.deploy(app("my-api", "my-api:1.1", 1))
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s", d.Status)
	}
	ev := lastEvent(t, rec, notify.DeploymentFailed)
	want := "my-api deployment of 1.1 failed: replica 1 exited with code 1 shortly after start. my-api is still running 1.0; the failed deployment did not affect it"
	if ev.Message != want || ev.DeploymentID != d.ID || ev.Version != "1.1" {
		t.Errorf("event = %+v\nwant message %q", ev, want)
	}
}

func TestFailedFirstDeploymentIsNotified(t *testing.T) {
	h := newHarness(t)
	rec := notified(h)
	h.rt.PullErr = errors.New("manifest unknown")
	h.deploy(app("my-api", "my-api:1.0", 1))

	ev := lastEvent(t, rec, notify.DeploymentFailed)
	if !strings.HasPrefix(ev.Message, "my-api deployment of 1.0 failed: manifest unknown. Nothing of my-api is running") {
		t.Errorf("message = %q", ev.Message)
	}
	if !strings.Contains(ev.Message, "shipwick status my-api") {
		t.Errorf("message = %q; it should say where to look", ev.Message)
	}
}

func TestAutomaticRollbackIsNotified(t *testing.T) {
	s, _ := newRouted(t)
	rec := notified(s.harness)
	s.deploy(web("web:1.0", 3))

	s.rt.CrashNames["shipwick_web_2_2"] = true
	d := s.deploy(web("web:1.1", 3))
	if d.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	ev := lastEvent(t, rec, notify.DeploymentRolledBack)
	want := "web deployment of 1.1 failed: replica 2 exited with code 1 shortly after start. Rolled back: web is running 1.0 again"
	if ev.Message != want || ev.DeploymentID != d.ID {
		t.Errorf("event = %+v\nwant message %q", ev, want)
	}
	if kinds := rec.Kinds(); len(kinds) != 2 {
		t.Errorf("kinds = %v; a rolled-back deployment is one notification, not a failure and then a rollback", kinds)
	}
}

func TestRollbackOnRequestIsNotifiedAsARollback(t *testing.T) {
	s, _ := newRouted(t)
	rec := notified(s.harness)
	s.deploy(web("web:1.0", 2))
	s.deploy(web("web:1.1", 2))

	rb := s.finish(s.engine.Rollback(context.Background(), "web", 0))
	ev := lastEvent(t, rec, notify.DeploymentRolledBack)
	if ev.Message != "web rolled back to 1.0 from 1.1" || ev.DeploymentID != rb.ID || ev.Version != "1.0" {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestApplicationDownAndRecoveredAreNotifiedOnce(t *testing.T) {
	s := newSupervised(t)
	d := s.deploy(app("my-api", "my-api:1.0", 2))
	rec := notified(s.harness)

	// One replica down is degraded, not down: nothing to tell.
	s.rt.Crash(s.container(t, 1).ID, 137)
	s.advance(time.Second)
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Fatalf("notifications = %v; one of two replicas down is not an outage", kinds)
	}

	s.rt.Crash(s.container(t, 2).ID, 137)
	s.advance(time.Second)
	ev := lastEvent(t, rec, notify.ApplicationDown)
	want := "my-api is down: none of its 2 replicas is running. Shipwick restarts it as restart.policy allows; see why with: shipwick logs my-api"
	if ev.Message != want || ev.DeploymentID != d.ID || ev.Version != "1.0" {
		t.Errorf("event = %+v\nwant message %q", ev, want)
	}

	// The supervisor restarts both within seconds. Running again is not yet
	// recovered: a crash-looping replica also runs for a moment each time.
	s.advance(2 * time.Second)
	s.advance(2 * time.Second)
	if !s.container(t, 1).Running || !s.container(t, 2).Running {
		t.Fatal("precondition: replicas restarted")
	}
	if kinds := rec.Kinds(); len(kinds) != 1 {
		t.Errorf("notifications = %v; a restart is not a recovery until the replica has stayed up", kinds)
	}

	s.advance(s.engine.opts.StableAfter + time.Second)
	ev = lastEvent(t, rec, notify.ApplicationRecovered)
	if ev.Message != "my-api is healthy again: 2/2 replicas running 1.0" {
		t.Errorf("message = %q", ev.Message)
	}
	if kinds := rec.Kinds(); len(kinds) != 2 {
		t.Errorf("notifications = %v; exactly one down and one recovered", kinds)
	}
}

func TestCrashLoopIsOneOutageNotification(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	rec := notified(s.harness)
	id := s.container(t, 1).ID

	s.rt.CrashImages["my-api:1.0"] = true
	s.rt.Crash(id, 1)
	for range 600 { // ten minutes: the whole backoff, into the crash loop
		s.advance(time.Second)
	}
	if _, looping := s.engine.sup.snapshot(id); !looping {
		t.Fatal("precondition: replica should be crash-looping")
	}
	// One more after five minutes: the alert that it has stayed down.
	if kinds := rec.Kinds(); len(kinds) != 2 || kinds[0] != notify.ApplicationDown || kinds[1] != notify.AlertRaised {
		t.Errorf("notifications = %v; every restart of a crash loop must not be a message", kinds)
	}

	// Fixed: the replica comes back at the next crash-loop retry and stays.
	delete(s.rt.CrashImages, "my-api:1.0")
	for range 8 {
		s.advance(time.Minute)
	}
	if kinds := rec.Kinds(); len(kinds) != 3 || kinds[2] != notify.ApplicationRecovered {
		t.Errorf("notifications = %v; a replica that stayed up is a recovery", kinds)
	}
}

func TestApplicationFailingItsHealthCheckIsDown(t *testing.T) {
	s := newSupervised(t)
	a := withHealth(app("my-api", "my-api:1.0", 1))
	a.Restart.Policy = "never" // running but unhealthy, and left that way
	s.deploy(a)
	rec := notified(s.harness)

	s.probes.setFailing(s.container(t, 1).IP, errors.New("HTTP 500"))
	for range 40 {
		s.advance(time.Second)
	}
	ev := lastEvent(t, rec, notify.ApplicationDown)
	if !strings.HasPrefix(ev.Message, "my-api is down: 1 replica running but failing its health check") {
		t.Errorf("message = %q", ev.Message)
	}
	if n := len(rec.Kinds()); n != 1 {
		t.Errorf("%d notifications; a replica that stays unhealthy is one outage", n)
	}
}

func TestStartingAStoppedApplicationIsNotAnOutage(t *testing.T) {
	s := newSupervised(t)
	s.deploy(withHealth(app("my-api", "my-api:1.0", 1)))
	rec := notified(s.harness)
	ctx := context.Background()

	if err := s.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if err := s.engine.Start(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	// Running, and not answering its health check yet: an application takes a
	// moment to listen.
	ip := s.container(t, 1).IP
	s.probes.setFailing(ip, errors.New("connection refused"))
	for range 3 {
		s.advance(time.Second)
	}
	s.probes.setFailing(ip, nil)
	for range 30 {
		s.advance(time.Second)
	}
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Errorf("notifications = %v; an application that was started and came up was never down", kinds)
	}

	// One that is started and does not come up is.
	if err := s.engine.Stop(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if err := s.engine.Start(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	s.probes.setFailing(s.container(t, 1).IP, errors.New("HTTP 500"))
	for range 300 {
		s.advance(time.Second)
	}
	if kinds := rec.Kinds(); len(kinds) == 0 || kinds[0] != notify.ApplicationDown {
		t.Errorf("notifications = %v; a started application that never passes its health check is down", kinds)
	}
}

func TestDeletedApplicationIsForgottenByTheSupervisor(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	rec := notified(s.harness)

	s.rt.Crash(s.container(t, 1).ID, 1)
	s.advance(time.Second)
	lastEvent(t, rec, notify.ApplicationDown)
	if err := s.engine.Delete(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)

	// The same name, deployed anew, starts with a clean slate.
	s.deploy(app("my-api", "my-api:1.0", 1))
	s.advance(time.Second)
	if kinds := rec.Kinds(); len(kinds) != 2 || kinds[1] != notify.DeploymentSucceeded {
		t.Errorf("notifications = %v; a new application must not be reported recovered", kinds)
	}
}

func TestServerViewReportsNotifications(t *testing.T) {
	h := newHarness(t)
	if s, _ := h.engine.Server(context.Background()); s.Notifications.Webhook {
		t.Error("no notifier configured, but the server view says webhook")
	}
	notified(h)
	if s, _ := h.engine.Server(context.Background()); !s.Notifications.Webhook {
		t.Error("a configured notifier must show in the server view")
	}
}

func TestAFailedJobIsNotified(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rec := notified(h)
	h.rt.HoldJobs = true
	h.deploy(withJob(app("my-api", "my-api:1.0", 1), "nightly", "0 3 * * *", time.Hour, "x"))

	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if !h.rt.ReleaseJob("nightly", 1) {
		t.Fatal("no running job container to release")
	}
	h.engine.Wait()

	ev := lastEvent(t, rec, notify.JobFailed)
	if ev.Application != "my-api" || !strings.Contains(ev.Message, "Job nightly failed (exit 1)") || !strings.Contains(ev.Message, "shipwick jobs logs my-api nightly") {
		t.Errorf("notification: %+v", ev)
	}
	h.rt.HoldJobs = false
	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	failed := 0
	for _, e := range rec.Events() {
		if e.Kind == notify.JobFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("a successful run must not notify: %d job.failed notifications", failed)
	}
}
