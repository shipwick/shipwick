package deploy

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestOneTickListsTheContainersOnceHoweverManyApplicationsThereAre(t *testing.T) {
	s, _ := newRouted(t)
	for i := 1; i <= 5; i++ {
		a := app(fmt.Sprintf("app%d", i), "app:1.0", 2)
		a.Domain = fmt.Sprintf("app%d.example.com", i)
		s.deploy(a)
	}

	before := s.rt.ListCalls()
	s.advance(time.Second)
	if n := s.rt.ListCalls() - before; n != 1 {
		t.Fatalf("one tick over 5 applications listed the containers %d times, want once: the list is what a tick costs the daemon", n)
	}

	// The one list is enough for the tick's whole job: a replica that died
	// is found, restarted and routed to again, and the restart is the only
	// question asked by ID.
	victim := s.container(t, 1)
	s.rt.Crash(victim.ID, 137)
	before = s.rt.ListCalls()
	s.advance(time.Second)
	s.advance(time.Second)
	if n := s.rt.ListCalls() - before; n != 2 {
		t.Errorf("two ticks listed the containers %d times, want twice", n)
	}
	if !s.container(t, 1).Running {
		t.Fatal("the crashed replica was not restarted")
	}
	if got := s.rt.Resolve(victim.App); len(got) != 2 {
		t.Errorf("%s resolves to %v, want both replicas: the restarted one must be routed to in the tick that restarted it", victim.App, got)
	}
}

func TestATickWorkingFromAListOlderThanADeploymentConcludesNothingFromIt(t *testing.T) {
	ctx := context.Background()
	s, _ := newRouted(t)
	s.deploy(web("web:1.0", 2))
	// The list of a tick that began before the deployment below finished: it
	// has the replicas the deployment retired and lacks the ones it created.
	listed, err := s.rt.ListContainers(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	snap := newContainerSnapshot(listed)
	s.deploy(web("web:1.1", 2))
	before := names(s.rt.Containers())
	renames := s.rt.NameChanges()
	events := len(s.appEvents(t, "web"))

	app, err := s.store.GetApplication(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	s.engine.sup.superviseApp(ctx, s.now, app, snap)
	if err := s.engine.syncRoutes(ctx, snap); err != nil {
		t.Fatal(err)
	}
	s.engine.Wait()

	if got := names(s.rt.Containers()); got != before {
		t.Errorf("containers = %s, want %s: the new replicas looked missing, or the retired ones like leftovers", got, before)
	}
	if got := s.appEvents(t, "web"); len(got) != events {
		t.Errorf("the tick concluded something from a list that was out of date:\n%v", got[events:])
	}
	// The new replicas' names were given by the deployment; a tick that did
	// not see the replicas must not forget them, or the next tick gives the
	// names again and cuts what the replicas have open.
	s.advance(time.Second)
	if n := s.rt.NameChanges() - renames; n != 0 {
		t.Errorf("replicas were renamed %d times after a tick with an old list, want none", n)
	}
}
