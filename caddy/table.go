package upstreams

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"
)

// settings says how patient a lookup is. They come with every call: the same
// name may be asked for by a configuration that was just loaded and by the
// one it replaces.
type settings struct {
	// refresh is how long an answer is used before the name is asked again.
	refresh time.Duration
	// wait is how long a request waits for the answer to a question that has
	// just been asked, when an older answer could be used instead.
	wait time.Duration
	// keep is how long the last answer is used while questions go
	// unanswered. It is short: Docker gives the address of a container that
	// is gone to the next one that starts.
	keep time.Duration
	// timeout ends a question nobody answered.
	timeout time.Duration
}

// errNoReplicas is the answer "nobody carries this name", as opposed to no
// answer at all.
var errNoReplicas = errors.New("no replica carries the name")

// table holds what each name last resolved to. One lookup per name is in
// flight at most, it belongs to no request, and the requests of one name
// never wait for the lookup of another.
type table struct {
	resolve func(ctx context.Context, name string) ([]string, error)
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	// changed is told when a name stops being answered and when it is
	// answered again; err is nil for the latter.
	changed func(name string, err error)

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	addrs    []string
	answered time.Time // when addrs was last confirmed; zero before the first answer
	asked    time.Time // when the lookup in flight, or the last one, started
	failing  bool      // the last lookup ended without an answer
	err      error     // why, or errNoReplicas
	inFlight chan struct{}
	used     time.Time
}

func newTable() *table {
	return &table{
		resolve: resolveIPv4,
		now:     time.Now,
		after:   time.After,
		changed: func(string, error) {},
		entries: make(map[string]*entry),
	}
}

// resolveIPv4 asks for A records only. The networks are IPv4, and an AAAA
// question would be forwarded to the outside resolvers and hold up the answer.
func resolveIPv4(ctx context.Context, name string) ([]string, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, errNoReplicas
		}
		return nil, err
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		addrs[i] = ip.String()
	}
	return addrs, nil
}

// addresses returns the addresses behind name. An answer younger than
// s.refresh is returned as it is. An older one starts a lookup, which the
// request waits for briefly; when no answer comes, the last one is used for
// as long as s.keep allows, and only a name that was never answered, or not
// for that long, makes the request wait for the lookup itself.
func (t *table) addresses(ctx context.Context, name string, s settings) ([]string, error) {
	t.mu.Lock()
	e := t.entries[name]
	if e == nil {
		t.forget()
		e = &entry{}
		t.entries[name] = e
	}
	now := t.now()
	e.used = now

	if !e.failing && !e.answered.IsZero() && now.Sub(e.answered) < s.refresh {
		addrs, err := e.result(name)
		t.mu.Unlock()
		return addrs, err
	}
	if e.inFlight == nil && (e.asked.IsZero() || now.Sub(e.asked) >= s.refresh) {
		e.asked = now
		e.inFlight = make(chan struct{})
		go t.lookup(name, e, s.timeout)
	}
	done := e.inFlight
	kept := e.kept(now, s.keep)
	// How long this request waits is counted from the question, not from the
	// request: one lost answer delays the requests of its first moments, not
	// every request until the lookup gives up.
	patience := s.wait - now.Sub(e.asked)
	if done == nil || (kept != nil && (e.failing || patience <= 0)) {
		addrs, err := kept, e.err
		t.mu.Unlock()
		if addrs != nil {
			return addrs, nil
		}
		return nil, unanswered(name, err)
	}
	t.mu.Unlock()

	var giveUp <-chan time.Time
	if kept != nil {
		giveUp = t.after(patience)
	}
	select {
	case <-done:
	case <-giveUp:
		return kept, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !e.failing {
		return e.result(name)
	}
	if kept := e.kept(t.now(), s.keep); kept != nil {
		return kept, nil
	}
	return nil, unanswered(name, e.err)
}

func unanswered(name string, err error) error {
	return fmt.Errorf("lookup %s: %w", name, err)
}

// result is the last answer; the caller holds the lock and knows there is one.
func (e *entry) result(name string) ([]string, error) {
	if len(e.addrs) == 0 {
		return nil, unanswered(name, e.err)
	}
	return slices.Clone(e.addrs), nil
}

// kept is the last answer if it may still be used, and nil otherwise. The
// answer "nobody" is not kept: there is nothing to serve from it.
func (e *entry) kept(now time.Time, keep time.Duration) []string {
	if len(e.addrs) == 0 || now.Sub(e.answered) >= keep {
		return nil
	}
	return slices.Clone(e.addrs)
}

func (t *table) lookup(name string, e *entry, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	addrs, err := t.resolve(ctx, name)
	cancel()
	// Docker's DNS answers in an order that rotates; a list that changes its
	// order every second would restart the round robin every second.
	slices.Sort(addrs)

	t.mu.Lock()
	wasFailing := e.failing
	switch {
	case err == nil, errors.Is(err, errNoReplicas):
		e.addrs, e.answered, e.failing, e.err = addrs, t.now(), false, err
	default:
		e.failing, e.err = true, err
	}
	nowFailing, changed := e.failing, t.changed
	close(e.inFlight)
	e.inFlight = nil
	t.mu.Unlock()

	if nowFailing != wasFailing {
		if !nowFailing {
			err = nil
		}
		changed(name, err)
	}
}

// setChanged replaces who is told about a name that stops or starts being
// answered: the configuration loaded last has the logger that is alive.
func (t *table) setChanged(f func(name string, err error)) {
	t.mu.Lock()
	t.changed = f
	t.mu.Unlock()
}

// forget drops the names nobody has asked for in a while, so that the table
// does not grow with every application the server ever ran. The caller holds
// the lock.
func (t *table) forget() {
	const idle = 10 * time.Minute
	if len(t.entries) < 256 {
		return
	}
	now := t.now()
	for name, e := range t.entries {
		if e.inFlight == nil && now.Sub(e.used) > idle {
			delete(t.entries, name)
		}
	}
}
