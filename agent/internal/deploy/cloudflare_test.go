package deploy

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// fakeVerifier answers for Cloudflare: what the check finds, how often it was
// asked, and — when block is set — a check that takes its time.
type fakeVerifier struct {
	mu     sync.Mutex
	calls  int
	status api.CloudflareStatus
	// started is sent to when a check begins; block, when set, holds the
	// check until it is closed.
	started chan struct{}
	block   chan struct{}
}

func (f *fakeVerifier) Verify(context.Context) api.CloudflareStatus {
	f.mu.Lock()
	f.calls++
	started, block, status := f.started, f.block, f.status
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	return status
}

func (f *fakeVerifier) times() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// cloudflared is a certified harness whose agent has a Cloudflare token,
// checked by the fake on the synthetic clock.
type cloudflared struct {
	*certified
	verifier *fakeVerifier
}

func newCloudflared(t *testing.T) *cloudflared {
	c := newCertified(t)
	f := &fakeVerifier{status: api.CloudflareStatus{OK: true, Address: "2a01:4f8:1c1b:5e07::1",
		Zones: []api.CloudflareZone{{Name: "example.com", DNSEdit: true}}}}
	c.engine.opts.Cloudflare = f
	c.engine.opts.DNSChallenge = true
	c.engine.cloudflare.now = func() time.Time { return c.now }
	return &cloudflared{certified: c, verifier: f}
}

func TestTheTokenIsCheckedAtStartAndTheResultIsInServer(t *testing.T) {
	c := newCloudflared(t)
	c.verifier.started = make(chan struct{}, 1)
	c.verifier.block = make(chan struct{})
	c.engine.StartCloudflareCheck()
	<-c.verifier.started
	// A request that comes in while the start's check runs joins it, and
	// gets its result; one after that is refused as recent. The check is
	// held until the request has joined, or it would be over first.
	type answer struct {
		got api.CloudflareStatus
		err error
	}
	joined := make(chan answer, 1)
	go func() {
		got, err := c.engine.VerifyCloudflare(context.Background())
		joined <- answer{got, err}
	}()
	for c.engine.cloudflare.joiners() == 0 {
		runtime.Gosched()
	}
	close(c.verifier.block)
	if a := <-joined; a.err != nil || !a.got.OK {
		t.Fatalf("while the start's check runs: got %+v, %v; want to join it", a.got, a.err)
	}
	var recent *CloudflareCheckedRecentlyError
	if _, err := c.engine.VerifyCloudflare(context.Background()); !errors.As(err, &recent) {
		t.Fatalf("a second check right after the first: %v; want it refused as recent", err)
	}
	server, err := c.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if server.Cloudflare == nil || !server.Cloudflare.OK || server.Cloudflare.Address != "2a01:4f8:1c1b:5e07::1" || len(server.Cloudflare.Zones) != 1 {
		t.Fatalf("server.cloudflare = %+v, want the check's result", server.Cloudflare)
	}
	if !server.Cloudflare.CheckedAt.Equal(c.now) {
		t.Errorf("checked_at = %s, want the clock's %s", server.Cloudflare.CheckedAt, c.now)
	}
	if n := c.verifier.times(); n != 1 {
		t.Errorf("Cloudflare was asked %d times, want once", n)
	}
}

func TestACheckIsNotRepeatedWithinAMinuteAndRunsAfterIt(t *testing.T) {
	c := newCloudflared(t)
	if _, err := c.engine.VerifyCloudflare(context.Background()); err != nil {
		t.Fatalf("first check: %v", err)
	}
	c.now = c.now.Add(30 * time.Second)
	_, err := c.engine.VerifyCloudflare(context.Background())
	var recent *CloudflareCheckedRecentlyError
	if !errors.As(err, &recent) || recent.RetryAfter != 30*time.Second {
		t.Fatalf("within a minute: err = %v, want refused with 30s to wait", err)
	}
	if n := c.verifier.times(); n != 1 {
		t.Errorf("Cloudflare was asked %d times within a minute, want once", n)
	}

	c.verifier.status = api.CloudflareStatus{Problems: []string{"the token is not valid"}}
	c.now = c.now.Add(31 * time.Second)
	got, err := c.engine.VerifyCloudflare(context.Background())
	if err != nil || got.OK || len(got.Problems) != 1 {
		t.Fatalf("after a minute: %+v, %v; want a new result", got, err)
	}
	if server, _ := c.engine.Server(context.Background()); server.Cloudflare == nil || server.Cloudflare.OK {
		t.Errorf("server.cloudflare = %+v, want the new result", server.Cloudflare)
	}
	if n := c.verifier.times(); n != 2 {
		t.Errorf("Cloudflare was asked %d times, want twice", n)
	}
}

func TestTwoCallersOfOneCheckShareIt(t *testing.T) {
	c := newCloudflared(t)
	c.verifier.started = make(chan struct{}, 1)
	c.verifier.block = make(chan struct{})

	results := make(chan api.CloudflareStatus, 2)
	ask := func() {
		got, err := c.engine.VerifyCloudflare(context.Background())
		if err != nil {
			t.Errorf("VerifyCloudflare: %v", err)
		}
		results <- got
	}
	go ask()
	<-c.verifier.started
	// The second caller arrives while the first's check runs, and waits for
	// it; the check is let go only once it does.
	go ask()
	for c.engine.cloudflare.joiners() == 0 {
		runtime.Gosched()
	}
	close(c.verifier.block)
	first, second := <-results, <-results
	if !first.OK || !second.OK || !first.CheckedAt.Equal(second.CheckedAt) {
		t.Errorf("results differ: %+v and %+v", first, second)
	}
	if n := c.verifier.times(); n != 1 {
		t.Errorf("Cloudflare was asked %d times for two callers at once, want once: a check writes a record in every zone", n)
	}
}

func TestWithoutATokenThereIsNothingToCheck(t *testing.T) {
	c := newCertified(t)
	c.engine.StartCloudflareCheck()
	if _, err := c.engine.VerifyCloudflare(context.Background()); !errors.Is(err, ErrCloudflareNotConfigured) {
		t.Errorf("err = %v, want ErrCloudflareNotConfigured", err)
	}
	server, err := c.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if server.Cloudflare != nil {
		t.Errorf("server.cloudflare = %+v without a token; nothing may read its presence as a token", server.Cloudflare)
	}
}

func TestTheResultIsHandedOutAsACopyWithEmptyListsNotNull(t *testing.T) {
	c := newCloudflared(t)
	c.verifier.status = api.CloudflareStatus{OK: true}
	got, err := c.engine.VerifyCloudflare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Zones == nil || got.Problems == nil {
		t.Errorf("zones = %v, problems = %v; the JSON must carry [] and not null", got.Zones, got.Problems)
	}
	status := c.engine.cloudflareStatus()
	status.OK = false
	if again := c.engine.cloudflareStatus(); !again.OK {
		t.Error("a caller's change reached the stored result")
	}
}
