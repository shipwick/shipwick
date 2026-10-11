package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// FollowOptions says where a followed log begins.
type FollowOptions struct {
	// Tail is how many of its last lines each replica begins with; 0 is only
	// what is logged from now on.
	Tail int
	// Since, when set, begins with every line logged at or after it instead,
	// which is how a stream that was lost is taken up where it left off.
	// Tail is then not sent.
	Since time.Time
}

// FollowHandler receives a followed log.
type FollowHandler interface {
	// Connected is called once the agent has begun the stream. resumed is
	// whether it honoured Since: an agent before 0.9 does not know it and
	// begins with the last Tail lines instead.
	Connected(resumed bool)
	// Line is called for every log line, in the order the agent sent them.
	Line(api.LogLine)
	// Heartbeat is called for every heartbeat — the line the agent sends
	// while the application prints nothing — with the agent's clock.
	Heartbeat(time.Time)
}

// StreamCutError is a followed log that ended before the agent ended it: the
// connection was lost, or something between the agent and here closed it.
type StreamCutError struct{ Err error }

func (e *StreamCutError) Error() string { return "log stream interrupted: " + e.Err.Error() }
func (e *StreamCutError) Unwrap() error { return e.Err }

// ErrAgentStopping is a followed log the agent ended because it is shutting
// down — a restart, an upgrade. It is back in a moment; a caller that wants
// to go on reconnects.
var ErrAgentStopping = errors.New("log stream ended: the agent is stopping")

// Follow streams the application's log to h until ctx is cancelled or the
// agent ends the stream because its containers are gone (nil in both cases),
// because it is shutting down (ErrAgentStopping), or the stream is lost
// (*StreamCutError). An agent that refuses the request is an *APIError, one
// that cannot be reached an *UnreachableError.
func (c *Client) Follow(ctx context.Context, name string, opts FollowOptions, h FollowHandler) error {
	q := url.Values{"follow": {"true"}}
	if opts.Since.IsZero() {
		q.Set("tail", strconv.Itoa(opts.Tail))
	} else {
		q.Set("since", opts.Since.UTC().Format(time.RFC3339Nano))
	}
	resp, err := c.send(ctx, c.stream, http.MethodGet, "/applications/"+url.PathEscape(name)+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeError(resp)
	}
	h.Connected(resp.Header.Get(api.LogSinceHeader) != "")

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	end := ""
	for sc.Scan() {
		var line streamLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return fmt.Errorf("malformed log stream: %w", err)
		}
		switch {
		case line.Heartbeat:
			h.Heartbeat(line.Time)
		case line.End != "":
			end = line.End
		default:
			h.Line(line.LogLine)
		}
	}
	err = sc.Err()
	switch {
	case ctx.Err() != nil:
		return nil
	case err == nil && end == api.LogEndShutdown:
		return ErrAgentStopping
	case err == nil:
		return nil
	case errors.Is(err, bufio.ErrTooLong):
		// Not a stream that broke off: one the agent did not send.
		return fmt.Errorf("malformed log stream: %w", err)
	}
	return &StreamCutError{Err: err}
}

// streamLine is what one line of the stream may be — a log line, a
// heartbeat, or the last line saying why the stream ends — told apart by
// the field.
type streamLine struct {
	api.LogLine
	Heartbeat bool   `json:"heartbeat"`
	End       string `json:"end"`
}

// lineHandler is a FollowHandler that wants the lines alone.
type lineHandler func(api.LogLine)

func (lineHandler) Connected(bool)       {}
func (h lineHandler) Line(l api.LogLine) { h(l) }
func (lineHandler) Heartbeat(time.Time)  {}
