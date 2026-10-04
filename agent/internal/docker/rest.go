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

// ForgottenRest is the rest of an address the proxy has confirmed it forgot.
// From then on the proxy hands the address to no request, but one it was
// handed to a moment before is still on its way there, for as long as the
// proxy tries to connect: half a second (the dial timeout in proxy/config.go).
// It is what AddressRest is longer than proxy.resolveKeep by, for the same
// reason.
const ForgottenRest = 500 * time.Millisecond

// forgetTimeout bounds the wait for the proxy's confirmation, which takes a
// millisecond or does not come: without it the address rests in full.
const forgetTimeout = time.Second

// addressRest knows when the containers of each application last gave up an
// address, and makes those of the others wait.
type addressRest struct {
	rest      time.Duration
	forgotten time.Duration
	now       func() time.Time
	// sleep returns after d, or sooner when wake is closed.
	sleep func(ctx context.Context, d time.Duration, wake <-chan struct{}) error

	mu sync.Mutex
	// since is when this process started. What was given up before, it does
	// not know, and counts as given up then.
	since    time.Time
	released map[string]time.Time
	// resting are the addresses given up within the rest, and until when
	// each keeps the other applications waiting.
	resting []*restingAddress
	// forget tells the proxy that the replicas behind names are gone, and
	// returns nil once the proxy has confirmed that it forgot them. Nil
	// without a proxy.
	forget func(ctx context.Context, names []string) error
	// confirmed is when the proxy forgot a container this process stopped,
	// by container ID: Docker goes on reporting that it stopped.
	confirmed map[string]time.Time
	// shortened is closed when a rest ends sooner than it was going to.
	shortened chan struct{}
}

type restingAddress struct {
	app string
	// at is when the address was given up.
	at    time.Time
	until time.Time
}

// holder is a container about to give up its address, as it was just before.
type holder struct {
	// id is empty, like app, for a container that could not be inspected.
	id  string
	app string
	// names are the ones it carries on the services network.
	names   []string
	running bool
	// finished is when a container that is not running stopped.
	finished *time.Time
}

func newAddressRest(rest time.Duration) *addressRest {
	return &addressRest{
		rest:      rest,
		forgotten: min(ForgottenRest, rest),
		now:       time.Now,
		sleep:     sleepContext,
		since:     time.Now(),
		released:  map[string]time.Time{},
		confirmed: map[string]time.Time{},
		shortened: make(chan struct{}),
	}
}

func sleepContext(ctx context.Context, d time.Duration, wake <-chan struct{}) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// forgetWith sets who is told that replicas are gone: see addressRest.forget.
func (a *addressRest) forgetWith(forget func(ctx context.Context, names []string) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forget = forget
}

// release records that a container has just given up its address: it was
// stopped, removed or taken off the network. A container nobody could name
// (an empty app) makes everyone wait.
//
// The address rests in full unless the proxy confirms that it forgot the
// names the container carried, which is asked for a container that was
// running: this process took it out of Docker's DNS, and nothing older than
// the proxy's confirmation can lead a request to it. One that was not running
// stopped by itself, at a moment nobody told the proxy about.
func (a *addressRest) release(ctx context.Context, h holder) {
	if a.rest <= 0 {
		return
	}
	a.mu.Lock()
	now := a.now()
	if at, ok := a.confirmed[h.id]; ok && !h.running && h.finished != nil && !h.finished.After(at) {
		// Stopped by this process and forgotten by the proxy since: there
		// is nothing left to give up.
		a.mu.Unlock()
		return
	}
	a.released[h.app] = now
	address := &restingAddress{app: h.app, at: now, until: now.Add(a.rest)}
	rested := 0
	for _, r := range a.resting {
		if r.until.After(now) {
			a.resting[rested] = r
			rested++
		}
	}
	a.resting = append(a.resting[:rested], address)
	forget := a.forget
	a.mu.Unlock()

	if !h.running || forget == nil {
		return
	}
	// The container is stopped whatever became of the request that asked for
	// it, and the proxy should hear of it all the same.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgetTimeout)
	defer cancel()
	if err := forget(ctx, h.names); err != nil {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now = a.now()
	until := now
	if len(h.names) > 0 {
		until = now.Add(a.forgotten)
	}
	if until.Before(address.until) {
		address.until = until
	}
	if a.released[h.app].Equal(address.at) {
		// From here on the address counts for as long as it rests, and no
		// longer: see tookDuring.
		delete(a.released, h.app)
	}
	a.confirmed[h.id] = now
	close(a.shortened)
	a.shortened = make(chan struct{})
}

// removed forgets a container that no longer exists.
func (a *addressRest) removed(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.confirmed, id)
}

// forgot says whether the proxy has confirmed, since the container stopped,
// that it forgot it.
func (a *addressRest) forgot(id string, finished time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, ok := a.confirmed[id]
	return ok && !finished.After(at)
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

// restsUntil is when the last address given up by a container of another
// application than app has rested, and what is closed if that becomes sooner.
func (a *addressRest) restsUntil(app string, exited map[string]time.Time) (time.Time, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	until := a.since.Add(a.rest)
	for _, r := range a.resting {
		if (r.app != app || r.app == "") && r.until.After(until) {
			until = r.until
		}
	}
	for other, t := range exited {
		if (other != app || other == "") && t.Add(a.rest).After(until) {
			until = t.Add(a.rest)
		}
	}
	return until, a.shortened
}

// wait returns once no address given up by a container of another
// application is still resting. exited reports the containers that stopped
// by themselves, by application; it is asked again after every wait, because
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
		until, shortened := a.restsUntil(app, stopped)
		remaining := until.Sub(a.now())
		if remaining <= 0 {
			return nil
		}
		if err := a.sleep(ctx, remaining, shortened); err != nil {
			return err
		}
	}
}

// tookDuring says whether a container of another application gave up an
// address at or after t: whoever took an address since then may hold that
// one. An address the proxy has forgotten counts while it still rests; after
// that nothing leads a request to it, whoever holds it.
func (a *addressRest) tookDuring(app string, t time.Time, exited map[string]time.Time) bool {
	if a.rest <= 0 {
		return false
	}
	if !a.lastByOthers(app, exited).Before(t) {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for _, r := range a.resting {
		if (r.app != app || r.app == "") && !r.at.Before(t) && r.until.After(now) {
			return true
		}
	}
	return false
}
