package deploy

import (
	"context"
	"errors"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
)

// A supervision pass asks Docker for the list of its containers once, at the
// top of the tick, and hands the answer to everything the tick does: the
// question whether the daemon answers, the bookkeeping to forget, every
// application's reconciliation and the routing at the end. The list is what
// the tick costs. Asked three times and once more per application, as it was,
// it was what limited how many applications a server carries: at a hundred of
// two replicas on two processors the pass no longer fit in its second, the
// server idled at one and a half processors, and a replica that died was away
// for nine seconds instead of three.
//
// The price of one list is that it is a moment old by the time an
// application's lock is taken. A deployment that finished in between has
// replicas the list does not have, and retired replicas it still has. Neither
// is acted on from the list alone: a replica the list lacks is asked about by
// ID, and found; a container that looks like a leftover is asked about by ID
// before it is retired, and found gone. Both were the rule already for the one
// conclusion that cannot be taken back, declaring a replica gone — acting on
// a listing alone would be acting on hearsay — and each costs one question
// only when the list is in fact stale.
//
// Everything outside the tick — a rollout's routing, the API's views — lists
// for itself: it wants the current state, not the tick's.

// containerSnapshot is the list of one tick, with what Docker said about
// containers the list did not have. It belongs to the tick's goroutine alone.
type containerSnapshot struct {
	// byID is what is known now: the list, corrected by every question asked
	// since. byApp is the list as it was taken, cut up per application.
	byID  map[string]docker.Container
	byApp map[string][]docker.Container
	// gone are the containers Docker was asked about by ID and did not have.
	gone map[string]bool
	// deployments are the active deployments as the database had them when
	// the list was taken, by ID. A deployment's record never changes in what
	// the tick reads of it, so an application found pointing at one of these
	// under its lock is not asked about it again; nil asks every time.
	deployments map[int64]store.Deployment
}

func newContainerSnapshot(list []docker.Container) *containerSnapshot {
	s := &containerSnapshot{byID: make(map[string]docker.Container, len(list)), byApp: map[string][]docker.Container{}, gone: map[string]bool{}}
	for _, c := range list {
		s.byID[c.ID] = c
		s.byApp[c.App] = append(s.byApp[c.App], c)
	}
	return s
}

// ofApp is what the list had for one application, in the list's order.
func (s *containerSnapshot) ofApp(app string) []docker.Container {
	return s.byApp[app]
}

// lookup is the snapshot's answer about one container: the list's, or, for a
// container the list did not have, Docker's by ID — a replica created since
// the list was taken, or one that is gone. Docker is asked once per container
// and tick; found is false when it does not have the container.
func (s *containerSnapshot) lookup(ctx context.Context, rt Runtime, id string) (c docker.Container, found bool, err error) {
	if c, ok := s.byID[id]; ok {
		return c, true, nil
	}
	if s.gone[id] {
		return docker.Container{}, false, nil
	}
	c, err = rt.InspectContainer(ctx, id)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		s.gone[id] = true
		return docker.Container{}, false, nil
	case err != nil:
		return docker.Container{}, false, err
	}
	s.byID[id] = c
	return c, true, nil
}

// refresh asks Docker again about a container the tick has just started: the
// list has it as it was, exited, and routing would leave it nameless for a
// tick that the restart was meant to save.
func (s *containerSnapshot) refresh(ctx context.Context, rt Runtime, id string) {
	delete(s.byID, id)
	delete(s.gone, id)
	s.lookup(ctx, rt, id)
}

// complete is the snapshot as a map by ID, with every member of the plans
// looked up that the list did not have: what routing works from, so that a
// replica a rollout named a moment ago is neither forgotten nor named again.
func (s *containerSnapshot) complete(ctx context.Context, rt Runtime, plans []routingPlan) (map[string]docker.Container, error) {
	for _, p := range plans {
		for _, m := range p.members {
			if _, _, err := s.lookup(ctx, rt, m.replica.ContainerID); err != nil {
				return nil, err
			}
		}
	}
	return s.byID, nil
}
