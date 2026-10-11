package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// recorder is a FollowHandler that writes down what it was handed.
type recorder struct {
	connected []bool
	lines     []api.LogLine
	beats     []time.Time
}

func (r *recorder) Connected(resumed bool) { r.connected = append(r.connected, resumed) }
func (r *recorder) Line(l api.LogLine)     { r.lines = append(r.lines, l) }
func (r *recorder) Heartbeat(t time.Time)  { r.beats = append(r.beats, t) }

func TestFollowHandsHeartbeatsOverApartFromTheLines(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tail") != "10" || r.URL.Query().Has("since") {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"replica": 1, "stream": "stdout", "time": "2026-10-11T10:00:00Z", "message": "first"}`)
		fmt.Fprintln(w, `{"heartbeat": true, "time": "2026-10-11T10:00:20Z"}`)
		fmt.Fprintln(w, `{"replica": 1, "stream": "stdout", "time": "2026-10-11T10:00:25Z", "message": "second"}`)
	})
	rec := &recorder{}
	if err := c.Follow(context.Background(), "my-api", FollowOptions{Tail: 10}, rec); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(rec.connected) != 1 || rec.connected[0] {
		t.Errorf("connected = %v, want once and not resumed", rec.connected)
	}
	if len(rec.lines) != 2 || rec.lines[0].Message != "first" || rec.lines[1].Message != "second" {
		t.Errorf("lines = %+v", rec.lines)
	}
	if len(rec.beats) != 1 || !rec.beats[0].Equal(time.Date(2026, 10, 11, 10, 0, 20, 0, time.UTC)) {
		t.Errorf("heartbeats = %v", rec.beats)
	}
}

func TestFollowSinceSendsTheTimeInsteadOfTheTailAndReadsTheAnswer(t *testing.T) {
	since := time.Date(2026, 10, 11, 10, 0, 25, 123456789, time.UTC)
	honoured := true
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("tail") || r.URL.Query().Get("since") != "2026-10-11T10:00:25.123456789Z" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		if honoured {
			w.Header().Set(api.LogSinceHeader, r.URL.Query().Get("since"))
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
	})
	rec := &recorder{}
	if err := c.Follow(context.Background(), "my-api", FollowOptions{Tail: 100, Since: since}, rec); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(rec.connected) != 1 || !rec.connected[0] {
		t.Errorf("connected = %v, want resumed", rec.connected)
	}

	// An agent before 0.9 answers without the header: not resumed.
	honoured = false
	rec = &recorder{}
	if err := c.Follow(context.Background(), "my-api", FollowOptions{Since: since}, rec); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(rec.connected) != 1 || rec.connected[0] {
		t.Errorf("connected = %v, want not resumed", rec.connected)
	}
}

func TestFollowTellsAStreamThatBrokeOffFromOneTheAgentEnded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"replica": 1, "stream": "stdout", "message": "first"}`)
		w.(http.Flusher).Flush()
		// The connection is closed with the response unfinished, as a proxy
		// that gives up on an idle response closes it.
		panic(http.ErrAbortHandler)
	})
	rec := &recorder{}
	err := c.Follow(context.Background(), "my-api", FollowOptions{}, rec)
	var cut *StreamCutError
	if !errors.As(err, &cut) || !strings.HasPrefix(err.Error(), "log stream interrupted: ") {
		t.Fatalf("err = %#v, want a StreamCutError", err)
	}
	if len(rec.lines) != 1 {
		t.Errorf("the line before the cut was lost: %+v", rec.lines)
	}

	// An agent that ends the stream to restart says so, and that is not a
	// cut either: it is ErrAgentStopping. Its containers going away is a
	// plain end.
	for end, want := range map[string]error{api.LogEndShutdown: ErrAgentStopping, api.LogEndContainers: nil} {
		c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, `{"replica": 1, "stream": "stdout", "message": "last"}`)
			fmt.Fprintf(w, `{"end": %q}`+"\n", end)
		})
		rec = &recorder{}
		if err := c.Follow(context.Background(), "my-api", FollowOptions{}, rec); !errors.Is(err, want) || (want == nil && err != nil) {
			t.Errorf("end %q: err = %v, want %v", end, err, want)
		}
		if len(rec.lines) != 1 || rec.lines[0].Message != "last" {
			t.Errorf("end %q: the end line is not a log line: %+v", end, rec.lines)
		}
	}

	// A line that is not JSON is the agent's fault, not the connection's.
	c = newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `not json`)
	})
	err = c.Follow(context.Background(), "my-api", FollowOptions{}, &recorder{})
	if errors.As(err, &cut) || err == nil || !strings.HasPrefix(err.Error(), "malformed log stream") {
		t.Errorf("err = %v, want a malformed stream", err)
	}
}
