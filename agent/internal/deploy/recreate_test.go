package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func stateful(image string) spec.App {
	a := web(image, 1)
	a.Volumes = []spec.Volume{{Name: "data", Path: "/var/lib/data"}}
	a.Deploy.Strategy = spec.StrategyRecreate
	return a
}

func TestRecreateStopsTheOldVersionBeforeTheNewOneStarts(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(stateful("db:1.0"))
	old := s.container(t, 1)

	// The moment the new container is created, nothing of the old version
	// may be running: two versions on one volume is how data gets lost.
	var runningAtCreate int
	var servedAtCreate []string
	var statusAtCreate api.ApplicationStatus
	s.rt.CreateHook = func(docker.ContainerSpec) error {
		for _, c := range s.rt.Containers() {
			if c.Running {
				runningAtCreate++
			}
		}
		servedAtCreate = p.upstreams("web.example.com")
		view, _ := s.engine.Application(context.Background(), "web")
		statusAtCreate = view.Status
		return nil
	}
	d := s.deploy(stateful("db:1.1"))
	s.rt.CreateHook = nil

	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if runningAtCreate != 0 {
		t.Errorf("%d containers were running when the new version was created; under recreate the old one is stopped first", runningAtCreate)
	}
	if len(servedAtCreate) != 0 {
		t.Errorf("the domain was routed to %v while nothing should serve", servedAtCreate)
	}
	if statusAtCreate != api.AppDown {
		t.Errorf("status while the old version is stopped and the new one not yet up = %s, want DOWN: it is", statusAtCreate)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_2_1" {
		t.Errorf("containers = %s; the old one is removed once the new one serves", got)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_2_1:8080" {
		t.Errorf("serving %v", got)
	}
	if _, err := s.store.GetDeployment(context.Background(), v1.ID); err != nil {
		t.Error(err)
	}
	if c, err := s.rt.InspectContainer(context.Background(), old.ID); err == nil && c.Running {
		t.Error("the old container is still running after the switch")
	}
	var steps []string
	for _, e := range s.events(d.ID) {
		steps = append(steps, e.Message)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "Stopped 1.0: 1.1 cannot run next to it") {
		t.Errorf("events do not say why the application was down: %v", steps)
	}
}

func TestRecreateRollsBackByStartingTheOldContainerAgain(t *testing.T) {
	s, p := newRouted(t)
	v1 := s.deploy(stateful("db:1.0"))
	old := s.container(t, 1)

	s.rt.CrashImages["db:1.1"] = true
	d := s.deploy(stateful("db:1.1"))

	if d.Status != api.StatusRolledBack {
		t.Fatalf("status = %s (%s), want ROLLED_BACK", d.Status, d.Error)
	}
	// The same container, not a new one: its process was stopped and started
	// again, with its data where it left it.
	after := s.container(t, 1)
	if after.ID != old.ID || !after.Running || after.DeploymentID != v1.ID {
		t.Errorf("after the rollback: %+v (was %s)", after, old.ID)
	}
	if got := names(s.rt.Containers()); got != "shipwick_web_1_1" {
		t.Errorf("containers = %s; the failed version must be gone", got)
	}
	if got := p.upstreams("web.example.com"); len(got) != 1 || got[0] != "shipwick_web_1_1:8080" {
		t.Errorf("serving %v", got)
	}
	app, _ := s.store.GetApplication(context.Background(), "web")
	if app.ActiveDeploymentID == nil || *app.ActiveDeploymentID != v1.ID {
		t.Errorf("active deployment = %v, want %d", app.ActiveDeploymentID, v1.ID)
	}
}

func TestVolumesAreTheApplicationsAndOutliveDeployments(t *testing.T) {
	s, _ := newRouted(t)
	s.deploy(stateful("db:1.0"))
	first := s.rt.Spec(s.container(t, 1).ID).Mounts
	s.deploy(stateful("db:1.1"))
	second := s.rt.Spec(s.container(t, 1).ID).Mounts

	want := []docker.Mount{{Volume: "data", Path: "/var/lib/data"}}
	if len(first) != 1 || first[0] != want[0] || len(second) != 1 || second[0] != want[0] {
		t.Errorf("mounts: %v then %v, want %v both times", first, second, want)
	}
	if docker.VolumeName("web", "data") != "shipwick_web_data" {
		t.Errorf("volume name = %s", docker.VolumeName("web", "data"))
	}
}

func TestRecreateWithoutVolumesIsAllowedAndScalesInOneBatch(t *testing.T) {
	// Recreate on its own: a service that holds a lock or a port it cannot
	// share. All replicas of the new version start together.
	s, p := newRouted(t)
	a := web("svc:1.0", 2)
	a.Deploy.Strategy = spec.StrategyRecreate
	s.deploy(a)
	s.rt.ResetPeak()

	a.Image = "svc:1.1"
	if d := s.deploy(a); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if got := p.upstreams("web.example.com"); len(got) != 2 {
		t.Errorf("serving %v", got)
	}
	if peak := s.rt.PeakContainers(); peak != 4 {
		t.Errorf("peak = %d containers; the old ones are kept (stopped) until the new ones serve", peak)
	}
}
