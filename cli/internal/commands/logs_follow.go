package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

// `logs -f` over a connection that does not last: a proxy in between closes a
// response it finds idle (Cloudflare's does, after about a minute), a network
// changes, the agent restarts for an upgrade. The stream is taken up again
// from the last time the agent told us, and only what was not printed yet is
// printed.

const (
	// followRetries is how many times in a row the stream may be lost without
	// a line or a heartbeat getting through before `logs -f` gives up. With
	// the delays doubling from a second, five tries span 31 seconds: an
	// agent restarting for an upgrade, image pull included.
	followRetries = 5
	// followRetryDelay is the wait before the first reconnect; it doubles
	// with every loss in a row.
	followRetryDelay = time.Second
)

// streamLostError is the stream lost followRetries times in a row: the last
// error, and how many.
type streamLostError struct {
	err   error
	times int
}

func (e *streamLostError) Error() string { return e.err.Error() }
func (e *streamLostError) Unwrap() error { return e.err }

// followLogs follows the application's log until the agent ends the stream
// or ctx is cancelled, reconnecting whenever the stream is lost.
func (c *cli) followLogs(ctx context.Context, cl *client.Client, name string, tail int, print func(api.LogLine)) error {
	s := &resumedStream{c: c, print: print}
	opts := client.FollowOptions{Tail: tail}
	lost, delay := 0, followRetryDelay
	for {
		s.received = false
		err := cl.Follow(ctx, name, opts, s)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if s.received {
			lost, delay = 0, followRetryDelay
		}
		if !connectionLost(err, s.connections > 0) {
			return err
		}
		if lost++; lost > followRetries {
			return &streamLostError{err: err, times: lost - 1}
		}
		if err := c.pause(ctx, delay); err != nil {
			return nil // Ctrl-C while waiting is the normal way out
		}
		delay *= 2
		opts = s.resume(tail)
	}
}

// connectionLost reports whether err is a stream that broke off, an agent
// that ended it to restart, or — once the stream was open before, so the
// address and the token are right — an agent that does not answer: the
// proxy's 502 while the agent restarts. An agent that refuses the request is
// none of these.
func connectionLost(err error, reconnecting bool) bool {
	var cut *client.StreamCutError
	var away *client.UnreachableError
	return errors.As(err, &cut) || errors.Is(err, client.ErrAgentStopping) || (reconnecting && errors.As(err, &away))
}

// pause waits d, or until ctx is cancelled, which is the error.
func (c *cli) pause(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	stop := c.schedule(d, func() { close(done) })
	defer stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// schedule calls f once d has passed and returns how to call that off:
// c.after, or time.AfterFunc when nothing replaced it.
func (c *cli) schedule(d time.Duration, f func()) (stop func()) {
	if c.after != nil {
		return c.after(d, f)
	}
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// resumedStream prints a followed log and remembers enough of it to take the
// stream up where it left off: the latest time the agent has told it — the
// last line's or the last heartbeat's, the agent's clock either way — and,
// per replica and stream, what was printed at the last moment. Lines the
// agent sends again at the boundary (since includes its own instant, and an
// agent before 0.9 sends its tail again) are not printed twice.
type resumedStream struct {
	c     *cli
	print func(api.LogLine)

	latest time.Time
	seen   map[string]*boundary
	// connections counts the streams opened; from the second on, lines may
	// repeat. received is whether the current one carried anything.
	connections int
	received    bool
}

// boundary is the last moment of one replica's stream: the time of its last
// printed line, and every message printed at exactly that time.
type boundary struct {
	at       time.Time
	messages map[string]bool
}

func (s *resumedStream) Connected(resumed bool) {
	s.connections++
	if s.connections == 1 {
		return
	}
	switch {
	case resumed:
		s.c.ui.Note("Reconnected; nothing was missed.")
	case s.latest.IsZero():
		s.c.ui.Note("Reconnected; lines printed while the connection was down may be missing.")
	default:
		s.c.ui.Note("Reconnected; lines between %s and %s may be missing: this agent is older than 0.9 and begins with its last lines again instead of where the stream stopped.",
			s.latest.Local().Format("15:04:05"), s.c.now().Local().Format("15:04:05"))
	}
}

func (s *resumedStream) Line(l api.LogLine) {
	s.received = true
	if l.Time.After(s.latest) {
		s.latest = l.Time
	}
	key := fmt.Sprintf("%d/%s", l.Replica, l.Stream)
	if s.seen == nil {
		s.seen = map[string]*boundary{}
	}
	b := s.seen[key]
	switch {
	case b == nil:
		s.seen[key] = &boundary{at: l.Time, messages: map[string]bool{l.Message: true}}
	case l.Time.After(b.at):
		b.at, b.messages = l.Time, map[string]bool{l.Message: true}
	case l.Time.Equal(b.at) && !b.messages[l.Message]:
		b.messages[l.Message] = true
	case s.connections > 1:
		return // printed before the stream was lost
	}
	s.print(l)
}

func (s *resumedStream) Heartbeat(at time.Time) {
	s.received = true
	if at.After(s.latest) {
		s.latest = at
	}
}

// resume is where the next stream begins: at the latest time seen, or, when
// nothing was seen yet, where the first one began.
func (s *resumedStream) resume(tail int) client.FollowOptions {
	if s.latest.IsZero() {
		return client.FollowOptions{Tail: tail}
	}
	return client.FollowOptions{Since: s.latest}
}
