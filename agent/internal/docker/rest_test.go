package docker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// restLab is an addressRest whose clock moves only when something sleeps.
type restLab struct {
	*addressRest
	clock  time.Time
	slept  []time.Duration
	exited map[string]time.Time
	// onSleep runs during a sleep: what happens while a container waits.
	onSleep func()
	// asleep runs as a sleep begins; if it ends a rest sooner, the sleep is
	// over before the clock has moved.
	asleep func()
}

func newRestLab() *restLab {
	l := &restLab{clock: time.Date(2026, 10, 4, 5, 0, 0, 0, time.UTC), exited: map[string]time.Time{}}
	l.addressRest = newAddressRest(AddressRest)
	// Started long ago: the tests are about what happened since.
	l.since = l.clock.Add(-time.Hour)
	l.now = func() time.Time { return l.clock }
	l.sleep = func(_ context.Context, d time.Duration, wake <-chan struct{}) error {
		if l.asleep != nil {
			f := l.asleep
			l.asleep = nil
			f()
		}
		select {
		case <-wake:
			return nil
		default:
		}
		l.slept = append(l.slept, d)
		l.clock = l.clock.Add(d)
		if l.onSleep != nil {
			f := l.onSleep
			l.onSleep = nil
			f()
		}
		return nil
	}
	return l
}

// release is a container of app that gives up its address without the agent
// having stopped it while it ran: the proxy is not asked.
func (l *restLab) release(app string) {
	l.addressRest.release(context.Background(), holder{app: app})
}

func (l *restLab) waitFor(t *testing.T, app string) time.Duration {
	t.Helper()
	l.slept = nil
	if err := l.wait(context.Background(), app, func(context.Context) (map[string]time.Time, error) { return l.exited, nil }); err != nil {
		t.Fatal(err)
	}
	var total time.Duration
	for _, d := range l.slept {
		total += d
	}
	return total
}

func TestAnAddressAnotherApplicationGaveUpRests(t *testing.T) {
	l := newRestLab()
	l.release("alpha")
	l.clock = l.clock.Add(time.Second)

	if waited := l.waitFor(t, "beta"); waited != AddressRest-time.Second {
		t.Errorf("beta waited %v, want the rest of %v", waited, AddressRest)
	}
	if waited := l.waitFor(t, "beta"); waited != 0 {
		t.Errorf("beta waited %v once the address had rested", waited)
	}
}

func TestAnApplicationDoesNotWaitForItself(t *testing.T) {
	l := newRestLab()
	l.release("alpha")
	if waited := l.waitFor(t, "alpha"); waited != 0 {
		t.Errorf("alpha waited %v for an address it gave up itself: a rollout would wait for each of its own replicas", waited)
	}
}

func TestAContainerThatStoppedByItselfCounts(t *testing.T) {
	l := newRestLab()
	// alpha's replica crashed half a second ago; nobody released anything.
	l.exited["alpha"] = l.clock.Add(-500 * time.Millisecond)
	if waited := l.waitFor(t, "beta"); waited != AddressRest-500*time.Millisecond {
		t.Errorf("beta waited %v", waited)
	}
	if waited := l.waitFor(t, "alpha"); waited != 0 {
		t.Errorf("alpha waited %v for its own replica", waited)
	}
}

func TestWhatHappensDuringTheWaitIsWaitedForToo(t *testing.T) {
	l := newRestLab()
	l.release("alpha")
	l.onSleep = func() { l.release("gamma") }
	if waited := l.waitFor(t, "beta"); waited != 2*AddressRest {
		t.Errorf("beta waited %v, want the rest after alpha and then the rest after gamma", waited)
	}
}

func TestAContainerNobodyCanNameMakesEveryoneWait(t *testing.T) {
	l := newRestLab()
	l.release("")
	for _, app := range []string{"alpha", ""} {
		if waited := l.waitFor(t, app); waited != AddressRest {
			t.Errorf("%q waited %v", app, waited)
		}
		l.clock = l.clock.Add(-AddressRest)
	}
}

func TestWhatAnEarlierProcessReleasedIsUnknown(t *testing.T) {
	l := newRestLab()
	l.since = l.clock
	if waited := l.waitFor(t, "beta"); waited != AddressRest {
		t.Errorf("beta waited %v right after the agent started, want a full rest", waited)
	}
}

func TestTheWaitEndsWithItsContext(t *testing.T) {
	l := newRestLab()
	l.release("alpha")
	l.sleep = func(context.Context, time.Duration, <-chan struct{}) error { return context.Canceled }
	err := l.wait(context.Background(), "beta", func(context.Context) (map[string]time.Time, error) { return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestNoRestNoWait(t *testing.T) {
	l := newRestLab()
	l.rest = 0
	l.release("alpha")
	asked := false
	err := l.wait(context.Background(), "beta", func(context.Context) (map[string]time.Time, error) {
		asked = true
		return nil, nil
	})
	if err != nil || asked || len(l.slept) != 0 {
		t.Errorf("err=%v asked=%v slept=%v", err, asked, l.slept)
	}
	if l.tookDuring("beta", l.clock, nil) {
		t.Error("nothing rests, so nothing was taken too early")
	}
}

func TestAnAddressGivenUpWhileAnotherWasTaken(t *testing.T) {
	l := newRestLab()
	started := l.clock
	if l.tookDuring("beta", started, l.exited) {
		t.Error("nobody gave anything up")
	}
	// While beta's container was starting, alpha's stopped.
	l.clock = l.clock.Add(200 * time.Millisecond)
	l.exited["alpha"] = l.clock
	if !l.tookDuring("beta", started, l.exited) {
		t.Error("beta may hold the address alpha gave up")
	}
	if l.tookDuring("alpha", started, l.exited) {
		t.Error("alpha's own replica is no concern of alpha's")
	}
}
