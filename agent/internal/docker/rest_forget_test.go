package docker

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// fakeProxy is the proxy as the rest sees it: asked to forget names, and
// answering or not.
type fakeProxy struct {
	err    error
	asked  [][]string
	during func(ctx context.Context)
}

func (p *fakeProxy) forget(ctx context.Context, names []string) error {
	p.asked = append(p.asked, names)
	if p.during != nil {
		p.during(ctx)
	}
	return p.err
}

// stopped is a running replica of app that the agent has just stopped.
func stopped(id, app string) holder {
	return holder{id: id, app: app, names: []string{app, app + "_80"}, running: true}
}

func TestAnAddressTheProxyForgotRestsBriefly(t *testing.T) {
	l := newRestLab()
	proxy := &fakeProxy{}
	l.forgetWith(proxy.forget)

	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	if len(proxy.asked) != 1 || !slices.Equal(proxy.asked[0], []string{"alpha", "alpha_80"}) {
		t.Fatalf("the proxy was asked to forget %v, want the names the replica carried", proxy.asked)
	}
	if waited := l.waitFor(t, "beta"); waited != ForgottenRest {
		t.Errorf("beta waited %v for an address the proxy has forgotten, want %v", waited, ForgottenRest)
	}
	if waited := l.waitFor(t, "beta"); waited != 0 {
		t.Errorf("beta waited %v more", waited)
	}
}

func TestAnAddressTheProxyDidNotForgetRestsInFull(t *testing.T) {
	for name, proxy := range map[string]*fakeProxy{
		"unreachable":      {err: errors.New("cannot reach Caddy's admin endpoint: dial unix /run/caddy/admin.sock: connect: no such file or directory")},
		"not Shipwick's":   {err: errors.New("the proxy did not forget (HTTP 404)")},
		"answers too late": {err: context.DeadlineExceeded},
	} {
		l := newRestLab()
		l.forgetWith(proxy.forget)
		l.addressRest.release(context.Background(), stopped("a1", "alpha"))
		if len(proxy.asked) != 1 {
			t.Errorf("%s: asked %d times, want once", name, len(proxy.asked))
		}
		if waited := l.waitFor(t, "beta"); waited != AddressRest {
			t.Errorf("%s: beta waited %v, want the full rest of %v", name, waited, AddressRest)
		}
		if l.forgot("a1", l.clock) {
			t.Errorf("%s: the container counts as forgotten", name)
		}
	}

	// And with nobody to ask.
	l := newRestLab()
	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	if waited := l.waitFor(t, "beta"); waited != AddressRest {
		t.Errorf("without a proxy beta waited %v", waited)
	}
}

func TestAContainerThatStoppedByItselfIsNotTheProxysToForget(t *testing.T) {
	l := newRestLab()
	proxy := &fakeProxy{}
	l.forgetWith(proxy.forget)

	// alpha's replica crashed a moment ago, and the agent removes it now.
	// Between the crash and now, the proxy was sending it requests; what it
	// forgets now says nothing about who took the address meanwhile.
	crashed := l.clock.Add(-100 * time.Millisecond)
	l.exited["alpha"] = crashed
	l.addressRest.release(context.Background(), holder{id: "a1", app: "alpha", names: []string{"alpha", "alpha_80"}, finished: &crashed})
	delete(l.exited, "alpha")

	if len(proxy.asked) != 0 {
		t.Errorf("the proxy was asked to forget %v", proxy.asked)
	}
	if waited := l.waitFor(t, "beta"); waited != AddressRest {
		t.Errorf("beta waited %v, want the full rest", waited)
	}
}

func TestAReplicaWithoutNamesIsConfirmedByTheProxyToo(t *testing.T) {
	l := newRestLab()
	proxy := &fakeProxy{err: errors.New("the proxy did not forget (HTTP 404)")}
	l.forgetWith(proxy.forget)

	// A newcomer leaves the network to come back with its names. A proxy
	// that cannot forget has not said that it holds nothing of the others.
	l.addressRest.release(context.Background(), holder{id: "a2", app: "alpha", running: true})
	if len(proxy.asked) != 1 || len(proxy.asked[0]) != 0 {
		t.Fatalf("asked %v, want one question without names", proxy.asked)
	}
	if waited := l.waitFor(t, "beta"); waited != AddressRest {
		t.Errorf("beta waited %v, want the full rest", waited)
	}
}

func TestTheProxyIsToldEvenWhenTheRequestThatStoppedTheReplicaIsGone(t *testing.T) {
	l := newRestLab()
	var asked error
	var bounded bool
	proxy := &fakeProxy{during: func(ctx context.Context) {
		asked = ctx.Err()
		_, bounded = ctx.Deadline()
	}}
	l.forgetWith(proxy.forget)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.addressRest.release(ctx, stopped("a1", "alpha"))
	if asked != nil || !bounded {
		t.Errorf("the proxy was asked with err=%v, bounded=%v; want a live context with a deadline", asked, bounded)
	}
	if waited := l.waitFor(t, "beta"); waited != ForgottenRest {
		t.Errorf("beta waited %v", waited)
	}
}

func TestAWaitEndsWhenTheProxyConfirms(t *testing.T) {
	l := newRestLab()
	asking, confirm, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	proxy := &fakeProxy{during: func(context.Context) {
		close(asking)
		<-confirm
	}}
	l.forgetWith(proxy.forget)

	go func() {
		l.addressRest.release(context.Background(), stopped("a1", "alpha"))
		close(released)
	}()
	// The replica is stopped and the proxy has not answered yet: beta starts
	// to wait for the full rest, and the answer comes while it waits.
	<-asking
	l.asleep = func() {
		close(confirm)
		<-released
	}
	if waited := l.waitFor(t, "beta"); waited != ForgottenRest {
		t.Errorf("beta waited %v, want %v from the proxy's answer", waited, ForgottenRest)
	}
}

func TestAnAddressGivenUpWhileAnotherWasTakenIsTakenAgainForgottenOrNot(t *testing.T) {
	l := newRestLab()
	l.forgetWith((&fakeProxy{}).forget)
	started := l.clock
	l.clock = l.clock.Add(100 * time.Millisecond)
	// While beta's container was starting, alpha's was stopped. Until the
	// proxy forgot it, a moment later, beta may have been given its address.
	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	if !l.tookDuring("beta", started, l.exited) {
		t.Error("beta may hold the address alpha gave up")
	}
	l.clock = l.clock.Add(time.Millisecond)
	if l.tookDuring("beta", l.clock, l.exited) {
		t.Error("an address taken after the proxy forgot it is beta's")
	}
	// Once the forgotten address has rested, no request is on its way to it,
	// and taking it again would gain nothing.
	l.clock = l.clock.Add(ForgottenRest)
	if l.tookDuring("beta", started, l.exited) {
		t.Error("beta gives back an address nothing leads to any more")
	}

	// An address the proxy never held, a newcomer's that had no names yet,
	// is nobody's concern from the moment the proxy says so.
	started = l.clock
	l.addressRest.release(context.Background(), holder{id: "a2", app: "alpha", running: true})
	if l.tookDuring("beta", started, l.exited) {
		t.Error("beta gives back an address because a container without names left the network")
	}
	if waited := l.waitFor(t, "beta"); waited != 0 {
		t.Errorf("beta waited %v for an address the proxy never held", waited)
	}

	// Not forgotten, it counts as it always did.
	l.forgetWith((&fakeProxy{err: errors.New("the proxy did not forget (HTTP 404)")}).forget)
	started = l.clock
	l.addressRest.release(context.Background(), stopped("a3", "alpha"))
	l.clock = l.clock.Add(AddressRest)
	if !l.tookDuring("beta", started, l.exited) {
		t.Error("beta keeps an address alpha gave up while it was taking one")
	}
}

func TestAReplicaTheAgentStoppedIsNotCountedAgainAsExited(t *testing.T) {
	l := newRestLab()
	proxy := &fakeProxy{}
	l.forgetWith(proxy.forget)

	finished := l.clock
	l.clock = l.clock.Add(10 * time.Millisecond)
	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	// Docker goes on reporting when the container stopped, as it does for one
	// that crashed.
	if !l.forgot("a1", finished) {
		t.Fatal("the stopped replica would rest in full after all, as an exited container")
	}
	if l.forgot("a2", finished) {
		t.Error("a container nobody stopped counts as forgotten")
	}
	l.clock = l.clock.Add(ForgottenRest)

	// Removing it later gives nothing up, and asks nothing.
	l.addressRest.release(context.Background(), holder{id: "a1", app: "alpha", names: []string{"alpha", "alpha_80"}, finished: &finished})
	if waited := l.waitFor(t, "beta"); waited != 0 || len(proxy.asked) != 1 {
		t.Errorf("beta waited %v for the removal of a container whose address had rested; the proxy was asked %d times", waited, len(proxy.asked))
	}

	// Started again and crashed, it is an exited container like any other.
	crashed := l.clock.Add(time.Second)
	if l.forgot("a1", crashed) {
		t.Error("a later crash counts as forgotten")
	}
	l.clock = crashed.Add(time.Second)
	l.addressRest.release(context.Background(), holder{id: "a1", app: "alpha", finished: &crashed})
	if waited := l.waitFor(t, "beta"); waited != AddressRest {
		t.Errorf("beta waited %v after the crashed container was removed, want the full rest", waited)
	}

	l.removed("a1")
	if l.forgot("a1", finished) {
		t.Error("a removed container is remembered")
	}
}

func TestAnApplicationsOwnReplicasNeverWaitForTheProxy(t *testing.T) {
	l := newRestLab()
	l.forgetWith((&fakeProxy{err: errors.New("unreachable")}).forget)
	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	if waited := l.waitFor(t, "alpha"); waited != 0 {
		t.Errorf("alpha waited %v for its own replica", waited)
	}
}

func TestNoRestNothingToForget(t *testing.T) {
	l := newRestLab()
	l.rest = 0
	proxy := &fakeProxy{}
	l.forgetWith(proxy.forget)
	l.addressRest.release(context.Background(), stopped("a1", "alpha"))
	if len(proxy.asked) != 0 {
		t.Errorf("asked %v", proxy.asked)
	}
}
