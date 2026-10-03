package dockertest

import (
	"context"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// How containers stop. A process may take its time over SIGTERM, or ignore it
// and be killed when the grace period is over; the fake does not let time
// pass for either. HoldStops makes every stop wait until the test releases
// it, and IgnoreSIGTERM makes a container's stop end the way a kill does.

// stopControl is the part of the fake's state that belongs to stopping,
// guarded by the fake's mutex.
type stopControl struct {
	hold     bool
	release  chan struct{}
	one      map[string]chan struct{} // by container ID: closed to let that stop through
	waiting  chan string              // IDs of containers whose stop is being held
	ignore   map[string]bool
	timeouts map[string]time.Duration // by container name
}

func (f *Fake) stops() *stopControl {
	if f.stop == nil {
		f.stop = &stopControl{ignore: map[string]bool{}, timeouts: map[string]time.Duration{}}
	}
	return f.stop
}

// HoldStops makes every StopContainer from now on wait until ReleaseStops —
// the process that takes its whole grace period — or until its context ends.
// The returned channel reports the ID of each container whose stop has begun.
func (f *Fake) HoldStops() <-chan string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.stops()
	s.hold, s.release, s.waiting = true, make(chan struct{}), make(chan string, 64)
	s.one = map[string]chan struct{}{}
	return s.waiting
}

// ReleaseStop lets the held stop of one container go through.
func (f *Fake) ReleaseStop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if one, held := f.stops().one[id]; held {
		close(one)
		delete(f.stops().one, id)
	}
}

// ReleaseStops lets the held stops, and all later ones, go through.
func (f *Fake) ReleaseStops() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.stops(); s.hold {
		s.hold = false
		close(s.release)
	}
}

// IgnoreSIGTERM makes containers of the image end the way a process does that
// has no handler for the signal: killed at the end of the grace period, with
// exit code 137.
func (f *Fake) IgnoreSIGTERM(image string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops().ignore[image] = true
}

// StopTimeout reports the grace period the container, by name, was last
// stopped with, and whether it was stopped at all.
func (f *Fake) StopTimeout(name string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.stops().timeouts[name]
	return t, ok
}

func (f *Fake) holdStop(ctx context.Context, id string) error {
	f.mu.Lock()
	s := f.stops()
	hold, release, waiting := s.hold, s.release, s.waiting
	var one chan struct{}
	if hold {
		if one = s.one[id]; one == nil {
			one = make(chan struct{})
			s.one[id] = one
		}
	}
	f.mu.Unlock()
	if !hold {
		return nil
	}
	select {
	case waiting <- id:
	default:
	}
	select {
	case <-release:
		return nil
	case <-one:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopExit records the grace period and answers the exit code of the stopped
// process. The caller holds f.mu.
func (f *Fake) stopExit(c *docker.Container, timeout time.Duration) int {
	s := f.stops()
	s.timeouts[c.Name] = timeout
	if s.ignore[c.Image] {
		return 137
	}
	return 0
}
