package deploy

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func withInit(a spec.App) spec.App {
	a.Init = true
	return a
}

// createdSpecs records the configuration of every container the engine
// creates, by what it is: "replica", or the job's name.
func createdSpecs(h *harness) func() map[string]docker.ContainerSpec {
	var mu sync.Mutex
	seen := map[string]docker.ContainerSpec{}
	h.rt.CreateHook = func(s docker.ContainerSpec) error {
		mu.Lock()
		defer mu.Unlock()
		kind := "replica"
		if s.Job != nil {
			kind = s.Job.Name
		}
		seen[kind] = s
		return nil
	}
	return func() map[string]docker.ContainerSpec {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]docker.ContainerSpec, len(seen))
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

func TestInitReachesEveryContainerOfTheApplication(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	specs := createdSpecs(h)
	a := withInit(withHook(app("my-api", "my-api:1.0", 1), "node", "migrate.js"))
	a = withJob(a, "nightly", "0 3 * * *", time.Minute, "node", "report.js")
	if d := h.deploy(a); d.Status != api.StatusActive {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}
	if _, err := h.engine.RunJob(ctx, "my-api", "nightly"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if _, err := h.engine.RunCommand(ctx, "my-api", []string{"node", "console.js"}); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	h.engine.Wait()

	got := specs()
	for _, kind := range []string{"replica", "pre-deploy", "nightly", "run"} {
		s, created := got[kind]
		if !created {
			t.Errorf("no %s container was created: %v", kind, got)
		} else if !s.Init {
			t.Errorf("the %s container has no init process; `init: true` is the application's, whatever runs from its image", kind)
		}
	}
}

func TestWithoutInitNoContainerGetsAnInitProcess(t *testing.T) {
	h := newHarness(t)
	specs := createdSpecs(h)
	h.deploy(withHook(app("my-api", "my-api:1.0", 1), "node", "migrate.js"))
	for kind, s := range specs() {
		if s.Init {
			t.Errorf("the %s container was given an init process nobody asked for", kind)
		}
	}
}

func TestBackupVerificationRunsTheApplicationAsItsReplicasRun(t *testing.T) {
	h := newHarness(t)
	withBackups(h, "", nil)
	specs := createdSpecs(h)
	const name = "db"
	h.deployBackupApp(spec.Backups{Schedule: "0 3 * * *"}, map[string]string{"a.txt": "alpha"})
	h.backup(name)
	if got := h.verify(name, 1); got.VerifiedAt == nil {
		t.Fatalf("verification: %+v", got)
	}
	if specs()[docker.VerifyJob].Init {
		t.Error("the verification container has an init process the application does not ask for")
	}

	d, err := h.store.ListDeployments(context.Background(), storeFilterLatest)
	if err != nil || len(d) != 1 {
		t.Fatalf("ListDeployments: %v, %v", d, err)
	}
	h.deploy(withInit(d[0].Spec))
	h.backup(name)
	if got := h.verify(name, 2); got.VerifiedAt == nil {
		t.Fatalf("verification: %+v", got)
	}
	if !specs()[docker.VerifyJob].Init {
		t.Error("the verification container has no init process, though the replicas it stands in for have one")
	}
}

func TestAKilledReplicaIsToldAboutInitUnlessItHasOne(t *testing.T) {
	const initAdvice = "set init: true"
	lastEvent := func(t *testing.T, s *supervised) string {
		t.Helper()
		events := s.appEvents(t, "web")
		if len(events) == 0 {
			t.Fatal("no event says that the old replica was killed")
		}
		return events[len(events)-1]
	}

	t.Run("without init, the event names it next to stop_timeout", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(web("web:1.0", 1))
		s.rt.IgnoreSIGTERM("web:1.0")
		s.deploy(web("web:1.1", 1))
		got := lastEvent(t, s)
		if !strings.Contains(got, "was killed") || !strings.Contains(got, initAdvice) || !strings.Contains(got, "deploy.stop_timeout") {
			t.Errorf("event = %q, want both ways out", got)
		}
	})

	t.Run("with init, a process without a handler ends at once and nothing is reported", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withInit(web("web:1.0", 1)))
		s.rt.IgnoreSIGTERM("web:1.0")
		s.deploy(withInit(web("web:1.1", 1)))
		if events := s.appEvents(t, "web"); len(events) != 0 {
			t.Errorf("events = %q, want none: the signal ended the process", events)
		}
	})

	t.Run("with init, one that outlasts its grace period is told about stop_timeout alone", func(t *testing.T) {
		s, _ := newRouted(t)
		s.deploy(withInit(web("web:1.0", 1)))
		s.rt.OutlastGrace("web:1.0")
		// The advice is about the configuration the killed replica ran with,
		// not the one that replaced it.
		s.deploy(web("web:1.1", 1))
		got := lastEvent(t, s)
		if !strings.Contains(got, "was killed") || strings.Contains(got, initAdvice) || !strings.Contains(got, "deploy.stop_timeout") {
			t.Errorf("event = %q, want stop_timeout and no word of the init process it already has", got)
		}
	})
}

func TestAStoppedReplicaThatHadToBeKilledIsReported(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	s.deploy(withStopTimeout(web("web:1.0", 1), 20*time.Second))
	s.rt.IgnoreSIGTERM("web:1.0")
	if err := s.engine.Stop(ctx, "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	events := strings.Join(s.appEvents(t, "web"), "\n")
	if !strings.Contains(events, "The stopped container shipwick_web_1_1 did not exit within 20s of SIGTERM and was killed") ||
		!strings.Contains(events, "set init: true") {
		t.Errorf("events do not say that the stopped replica was killed, and what to do about it:\n%s", events)
	}

	// One that exits when asked is nothing to report.
	s2, _ := newRouted(t)
	s2.deploy(web("web:1.0", 1))
	if err := s2.engine.Stop(ctx, "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if events := strings.Join(s2.appEvents(t, "web"), "\n"); strings.Contains(events, "was killed") {
		t.Errorf("a replica that stopped by itself was reported:\n%s", events)
	}
}
