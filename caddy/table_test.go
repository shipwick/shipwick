package upstreams

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

var patient = settings{refresh: time.Second, wait: 200 * time.Millisecond, keep: 10 * time.Second, timeout: 2 * time.Second}

// lab is a table whose DNS, clock and timers the test holds.
type lab struct {
	t     *testing.T
	table *table

	mu      sync.Mutex
	now     time.Time
	timers  []chan time.Time
	asked   chan question
	changes []string
}

type question struct {
	name   string
	answer chan<- answer
}

type answer struct {
	addrs []string
	err   error
}

func newLab(t *testing.T) *lab {
	l := &lab{t: t, now: time.Date(2026, 10, 4, 3, 24, 53, 0, time.UTC), asked: make(chan question)}
	l.table = newTable()
	l.table.resolve = func(ctx context.Context, name string) ([]string, error) {
		reply := make(chan answer, 1)
		l.asked <- question{name, reply}
		a := <-reply
		return a.addrs, a.err
	}
	l.table.now = func() time.Time {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.now
	}
	l.table.after = func(time.Duration) <-chan time.Time {
		l.mu.Lock()
		defer l.mu.Unlock()
		c := make(chan time.Time, 1)
		l.timers = append(l.timers, c)
		return c
	}
	l.table.changed = func(name string, err error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if err != nil {
			l.changes = append(l.changes, name+" unanswered")
		} else {
			l.changes = append(l.changes, name+" answered")
		}
	}
	return l
}

func (l *lab) pass(d time.Duration) {
	l.mu.Lock()
	l.now = l.now.Add(d)
	l.mu.Unlock()
}

// outOfPatience fires the timer of the request that is waiting, once it waits.
func (l *lab) outOfPatience() {
	for {
		l.mu.Lock()
		if len(l.timers) > 0 {
			break
		}
		l.mu.Unlock()
		runtime.Gosched()
	}
	defer l.mu.Unlock()
	for _, c := range l.timers {
		c <- time.Time{}
	}
	l.timers = nil
}

type result struct {
	addrs []string
	err   error
}

// request asks for name as a request would, and returns where its result
// will arrive.
func (l *lab) request(ctx context.Context, name string) <-chan result {
	done := make(chan result, 1)
	go func() {
		addrs, err := l.table.addresses(ctx, name, patient)
		done <- result{addrs, err}
	}()
	return done
}

// question returns the lookup the table has started.
func (l *lab) question(name string) question {
	l.t.Helper()
	q := <-l.asked
	if q.name != name {
		l.t.Fatalf("asked for %q, want %q", q.name, name)
	}
	return q
}

// answered is a request for name whose lookup is answered with addrs.
func (l *lab) answered(name string, addrs ...string) {
	l.t.Helper()
	done := l.request(context.Background(), name)
	l.question(name).answer <- answer{addrs: slices.Clone(addrs)}
	sorted := slices.Sorted(slices.Values(addrs))
	l.want(done, sorted...)
}

func (l *lab) want(done <-chan result, addrs ...string) {
	l.t.Helper()
	r := <-done
	if r.err != nil || !slices.Equal(r.addrs, addrs) {
		l.t.Fatalf("got %v, %v; want %v", r.addrs, r.err, addrs)
	}
}

// settled waits until the lookup that was just answered is recorded: the
// table has nothing in flight for name.
func (l *lab) settled(name string) {
	for {
		l.table.mu.Lock()
		e := l.table.entries[name]
		idle := e != nil && e.inFlight == nil
		l.table.mu.Unlock()
		if idle {
			return
		}
		runtime.Gosched()
	}
}

// reported waits for the n-th report: it is made after the lookup is recorded.
func (l *lab) reported(n int) []string {
	for {
		l.mu.Lock()
		changes := slices.Clone(l.changes)
		l.mu.Unlock()
		if len(changes) >= n {
			return changes
		}
		runtime.Gosched()
	}
}

func TestAnAnswerIsUsedUntilItIsOld(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.7", "172.18.0.5")

	l.pass(900 * time.Millisecond)
	// No lookup: a question here would block the test, nobody answers it.
	l.want(l.request(context.Background(), "api_8080"), "172.18.0.5", "172.18.0.7")

	l.pass(100 * time.Millisecond)
	done := l.request(context.Background(), "api_8080")
	l.question("api_8080").answer <- answer{addrs: []string{"172.18.0.9"}}
	l.want(done, "172.18.0.9")
}

// What happened on a real server: one answer from Docker's DNS never came.
func TestALostAnswerDelaysNobodyForLong(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5")
	l.pass(time.Second)

	first := l.request(context.Background(), "api_8080")
	lost := l.question("api_8080")
	l.outOfPatience()
	l.want(first, "172.18.0.5")

	// The question is still out. A request that comes once it is older than
	// the wait does not wait at all.
	l.pass(300 * time.Millisecond)
	l.want(l.request(context.Background(), "api_8080"), "172.18.0.5")

	// The resolver gives up; the answer is kept and the name asked again.
	l.pass(1700 * time.Millisecond)
	lost.answer <- answer{err: context.DeadlineExceeded}
	l.settled("api_8080")
	again := l.request(context.Background(), "api_8080")
	l.want(again, "172.18.0.5")
	l.question("api_8080").answer <- answer{addrs: []string{"172.18.0.5", "172.18.0.6"}}
	l.settled("api_8080")
	l.want(l.request(context.Background(), "api_8080"), "172.18.0.5", "172.18.0.6")

	if got, want := l.reported(2), []string{"api_8080 unanswered", "api_8080 answered"}; !slices.Equal(got, want) {
		t.Errorf("reported %v, want %v", got, want)
	}
}

func TestOneNameDoesNotWaitForAnother(t *testing.T) {
	l := newLab(t)
	l.answered("web_3000", "172.18.0.4")
	l.pass(time.Second)

	// api's first lookup hangs; there is nothing to serve it from.
	api := l.request(context.Background(), "api_8080")
	hung := l.question("api_8080")

	web := l.request(context.Background(), "web_3000")
	l.question("web_3000").answer <- answer{addrs: []string{"172.18.0.4"}}
	l.want(web, "172.18.0.4")

	hung.answer <- answer{addrs: []string{"172.18.0.5"}}
	l.want(api, "172.18.0.5")
}

func TestAnAbandonedRequestTakesNoQuestionWithIt(t *testing.T) {
	l := newLab(t)
	ctx, abandon := context.WithCancel(context.Background())
	gone := l.request(ctx, "api_8080")
	q := l.question("api_8080")
	stays := l.request(context.Background(), "api_8080")

	abandon()
	if r := <-gone; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("the abandoned request got %v, %v", r.addrs, r.err)
	}
	q.answer <- answer{addrs: []string{"172.18.0.5"}}
	l.want(stays, "172.18.0.5")
}

func TestTheLastAnswerIsNotKeptForever(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5")

	// Unanswered for longer than an address can be trusted to belong to the
	// same container.
	l.pass(11 * time.Second)
	done := l.request(context.Background(), "api_8080")
	l.question("api_8080").answer <- answer{err: context.DeadlineExceeded}
	if r := <-done; r.err == nil {
		t.Fatalf("an answer 11s old was used: %v", r.addrs)
	}

	// Not asked again before the refresh interval has passed.
	if r := <-l.request(context.Background(), "api_8080"); r.err == nil {
		t.Fatalf("got %v without an answer", r.addrs)
	}
}

func TestNobodyIsAnAnswer(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5")
	l.pass(time.Second)

	// The replicas are gone: the name no longer exists. That is not a lookup
	// that failed, and the addresses of a second ago are not served.
	done := l.request(context.Background(), "api_8080")
	l.question("api_8080").answer <- answer{err: errNoReplicas}
	if r := <-done; !errors.Is(r.err, errNoReplicas) {
		t.Fatalf("got %v, %v", r.addrs, r.err)
	}
	if r := <-l.request(context.Background(), "api_8080"); !errors.Is(r.err, errNoReplicas) {
		t.Fatalf("got %v, %v", r.addrs, r.err)
	}
	if len(l.changes) != 0 {
		t.Errorf("reported %v; a name nobody carries is answered", l.changes)
	}

	l.pass(time.Second)
	done = l.request(context.Background(), "api_8080")
	l.question("api_8080").answer <- answer{addrs: []string{"172.18.0.8"}}
	l.want(done, "172.18.0.8")
}

func TestManyRequestsAskOnce(t *testing.T) {
	l := newLab(t)
	var waiting []<-chan result
	for range 50 {
		waiting = append(waiting, l.request(context.Background(), "api_8080"))
	}
	l.question("api_8080").answer <- answer{addrs: []string{"172.18.0.5"}}
	for _, done := range waiting {
		l.want(done, "172.18.0.5")
	}
}

func TestNamesNobodyAsksForAreForgotten(t *testing.T) {
	l := newLab(t)
	for i := range 256 {
		name := "app" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "_80"
		l.answered(name, "172.18.0.5")
	}
	l.pass(11 * time.Minute)
	l.answered("new_80", "172.18.0.6")
	if n := len(l.table.entries); n != 1 {
		t.Errorf("%d names remembered, want the one in use", n)
	}
}
