package docker

import (
	"context"
	"sync"
	"time"
)

// AddressRest is how long an address on the services network stays unused
// after a container gave it up, before a container of another application may
// take one.
//
// Docker gives the address of a container that is gone to the next container
// that asks, and the reverse proxy goes on using what it learned about a name
// for a while: a second by itself, two while Docker's DNS is silent (see
// proxy.resolveKeep). Without the rest, the replica of one application could
// start on the address the proxy still holds for another, and answer its
// requests. Within one application that is only a replica reached a moment
// early, so its own containers are not made to wait for each other.
const AddressRest = 2500 * time.Millisecond

// addressRest knows when the containers of each application last gave up an
// address, and makes those of the others wait.
type addressRest struct {
	rest  time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu sync.Mutex
	// since is when this process started. What was given up before, it does
	// not know, and counts as given up then.
	since    time.Time
	released map[string]time.Time
}

func newAddressRest(rest time.Duration) *addressRest {
	return &addressRest{
		rest:     rest,
		now:      time.Now,
		sleep:    sleepContext,
		since:    time.Now(),
		released: map[string]time.Time{},
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release records that a container of app has just given up its address: it
// was stopped, removed or taken off the network. An empty app is a container
// nobody could name, and makes everyone wait.
func (a *addressRest) release(app string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.released[app] = a.now()
}

// lastByOthers is when a container of another application than app last gave
// up an address: one this process released, or one that exited by itself.
func (a *addressRest) lastByOthers(app string, exited map[string]time.Time) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	last := a.since
	for _, times := range []map[string]time.Time{a.released, exited} {
		for other, t := range times {
			if (other != app || other == "") && t.After(last) {
				last = t
			}
		}
	}
	return last
}

// wait returns once no container of another application has given up an
// address within the rest. exited reports the containers that stopped by
// themselves, by application; it is asked again after every wait, because
// one may have stopped meanwhile.
func (a *addressRest) wait(ctx context.Context, app string, exited func(context.Context) (map[string]time.Time, error)) error {
	if a.rest <= 0 {
		return nil
	}
	for {
		stopped, err := exited(ctx)
		if err != nil {
			return err
		}
		remaining := a.lastByOthers(app, stopped).Add(a.rest).Sub(a.now())
		if remaining <= 0 {
			return nil
		}
		if err := a.sleep(ctx, remaining); err != nil {
			return err
		}
	}
}

// tookDuring says whether a container of another application gave up an
// address at or after t: whoever took an address since then may hold that one.
func (a *addressRest) tookDuring(app string, t time.Time, exited map[string]time.Time) bool {
	return a.rest > 0 && !a.lastByOthers(app, exited).Before(t)
}
