package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// The proxy uses the Cloudflare token when a certificate is due, which can be
// weeks after the agent started, and says what went wrong in its own log. The
// agent asks Cloudflare first — once at start, and again when doctor asks —
// and keeps the answer in memory: GET /server carries it, doctor prints it,
// and a certificate that fails for want of a DNS record is explained with it
// (certfailures.go). A check writes a record in every zone the token sees, so
// it is never repeated within a minute and never run in a loop.

// CloudflareVerifier checks what the token can do: cloudflare.Verifier in
// production, a fake in tests.
type CloudflareVerifier interface {
	Verify(ctx context.Context) api.CloudflareStatus
}

const (
	// cloudflareRecheckAfter is how soon after a check the next one may run.
	cloudflareRecheckAfter = time.Minute
	// cloudflareCheckTimeout bounds one check: five requests to Cloudflare,
	// each with a timeout of its own, plus two per zone.
	cloudflareCheckTimeout = 2 * time.Minute
)

// ErrCloudflareNotConfigured is a check asked of an agent without a token.
var ErrCloudflareNotConfigured = errors.New("this agent has no Cloudflare API token to check: SHIPWICK_CLOUDFLARE_API_TOKEN is not set")

// CloudflareCheckedRecentlyError refuses a check within a minute of the last
// one, and says when the next is possible.
type CloudflareCheckedRecentlyError struct {
	CheckedAt  time.Time
	RetryAfter time.Duration
}

func (e *CloudflareCheckedRecentlyError) Error() string {
	return fmt.Sprintf("the Cloudflare token was checked at %s; a check writes a DNS record in every zone, so the next one is possible in %d seconds, and GET /server has the result of the last",
		e.CheckedAt.UTC().Format(time.RFC3339), int(e.RetryAfter.Seconds()+0.5))
}

// cloudflareCheck is the last result and the check in progress.
type cloudflareCheck struct {
	now func() time.Time // tests set the clock

	mu   sync.Mutex
	last *api.CloudflareStatus
	// running is closed when the check in progress ends; nil while none
	// runs. A second caller joins the first instead of writing a second
	// record in every zone; joined counts those, for tests.
	running chan struct{}
	joined  int
}

func (c *cloudflareCheck) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// StartCloudflareCheck verifies the token once, in the background, and logs
// one line about what it found. Without a token there is nothing to check.
func (e *Engine) StartCloudflareCheck() {
	if e.opts.Cloudflare == nil {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		ctx, cancel := context.WithTimeout(e.baseCtx, cloudflareCheckTimeout)
		defer cancel()
		e.checkCloudflare(ctx, false)
	}()
}

// VerifyCloudflare checks the token now, for whoever asks. A check that is
// running is joined rather than doubled; one that finished less than a minute
// ago is refused with the time to wait, since GET /server has its result.
func (e *Engine) VerifyCloudflare(ctx context.Context) (api.CloudflareStatus, error) {
	if e.opts.Cloudflare == nil {
		return api.CloudflareStatus{}, ErrCloudflareNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, cloudflareCheckTimeout)
	defer cancel()
	return e.checkCloudflare(ctx, true)
}

// checkCloudflare runs the check, or waits for the one that is running, and
// returns what it found. With refuseRecent, a check that finished less than a
// minute ago is refused instead of repeated. Running, recent and starting are
// decided under one lock: a caller that found a check running must not start
// another the moment it ends.
func (e *Engine) checkCloudflare(ctx context.Context, refuseRecent bool) (api.CloudflareStatus, error) {
	c := &e.cloudflare
	c.mu.Lock()
	if done := c.running; done != nil {
		c.joined++
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return api.CloudflareStatus{}, ctx.Err()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return *c.last, nil
	}
	if refuseRecent && c.last != nil {
		if since := c.clock().Sub(c.last.CheckedAt); since < cloudflareRecheckAfter {
			c.mu.Unlock()
			return api.CloudflareStatus{}, &CloudflareCheckedRecentlyError{CheckedAt: c.last.CheckedAt, RetryAfter: cloudflareRecheckAfter - since}
		}
	}
	done := make(chan struct{})
	c.running = done
	c.mu.Unlock()

	status := e.opts.Cloudflare.Verify(ctx)
	status.CheckedAt = c.clock().UTC()
	if status.Zones == nil {
		status.Zones = []api.CloudflareZone{}
	}
	if status.Problems == nil {
		status.Problems = []string{}
	}
	e.logCloudflare(status)

	c.mu.Lock()
	c.last = &status
	c.running = nil
	c.mu.Unlock()
	close(done)
	return status, nil
}

// logCloudflare is the one line about the token. It names zones and the
// address Cloudflare saw, never anything of the token.
func (e *Engine) logCloudflare(status api.CloudflareStatus) {
	zones := make([]string, 0, len(status.Zones))
	for _, z := range status.Zones {
		zones = append(zones, z.Name)
	}
	if status.OK {
		e.log.Info("Cloudflare token verified: it can edit the DNS records of its zones", "zones", strings.Join(zones, ", "), "address", status.Address)
		return
	}
	e.log.Warn("Cloudflare token cannot do its job", "problems", strings.Join(status.Problems, "; "), "zones", strings.Join(zones, ", "), "address", status.Address)
}

// cloudflareStatus is the last result, for GET /server; nil without a token
// or before the first check has finished. The result is never written
// again once stored, so handing it out is safe.
func (e *Engine) cloudflareStatus() *api.CloudflareStatus {
	c := &e.cloudflare
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil
	}
	status := *c.last
	return &status
}

// cloudflareProblems are the sentences of the last check that was not in
// order, for a certificate failure that the DNS record explains.
func (e *Engine) cloudflareProblems() []string {
	status := e.cloudflareStatus()
	if status == nil || status.OK {
		return nil
	}
	return status.Problems
}
