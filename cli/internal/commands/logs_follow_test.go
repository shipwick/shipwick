package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// fakeFollow answers logs?follow=true in place of the fixture's handler.
type fakeFollow struct {
	serve func(http.ResponseWriter, *http.Request)
}

// cutStream is an agent whose followed log breaks off: each request is
// answered by the next of its pages, and a page that does not end the
// stream is cut after its lines, the way a proxy cuts an idle response.
type cutStream struct {
	mu       sync.Mutex
	pages    []streamPage
	requests []string
	// older leaves the X-Shipwick-Log-Since header out, as an agent before
	// 0.9 does.
	older bool
}

type streamPage struct {
	lines []api.LogLine
	ends  bool // the agent ends the stream: the containers are gone
}

func (s *cutStream) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.RawQuery)
	var page streamPage
	if len(s.pages) > 0 {
		page, s.pages = s.pages[0], s.pages[1:]
	}
	older := s.older
	s.mu.Unlock()

	if since := r.URL.Query().Get("since"); since != "" && !older {
		w.Header().Set(api.LogSinceHeader, since)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, line := range page.lines {
		json.NewEncoder(w).Encode(line)
	}
	if page.ends {
		return
	}
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

func at(sec int) time.Time { return time.Date(2026, 10, 11, 10, 0, sec, 0, time.UTC) }

func line(replica, sec int, msg string) api.LogLine {
	return api.LogLine{Replica: replica, Container: "c", Stream: "stdout", Time: at(sec), Message: msg}
}

// noWait fires the CLI's timers at once and writes down how long each would
// have waited.
func noWait(waits *[]time.Duration) fakeTimer {
	return func(d time.Duration, fn func()) func() {
		*waits = append(*waits, d)
		fn()
		return func() {}
	}
}

func TestLogsFollowTakesACutStreamUpWhereItLeftOff(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2}}}
	s := &cutStream{pages: []streamPage{
		{lines: []api.LogLine{line(1, 1, "one"), line(2, 2, "two"), line(1, 3, "three")}},
		// since includes its own instant: the last line comes again, and
		// then what was printed while the connection was down.
		{lines: []api.LogLine{line(1, 3, "three"), line(2, 4, "four")}},
		{lines: []api.LogLine{line(1, 4, "same second, other replica"), line(1, 5, "five")}, ends: true},
	}}
	f.follow.serve = s.serve
	var waits []time.Duration
	f.after = noWait(&waits)

	out, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Fatalf("logs -f: %v\n%s", err, errOut)
	}
	want := "[1] one\n[2] two\n[1] three\n[2] four\n[1] same second, other replica\n[1] five\n"
	if out != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
	if got := strings.Count(errOut, "Reconnected; nothing was missed.\n"); got != 2 {
		t.Errorf("want the reconnection said once each time, got %d in:\n%s", got, errOut)
	}
	if !strings.Contains(errOut, "log stream ended: the containers were stopped or replaced") {
		t.Errorf("the end the agent chose must still be said:\n%s", errOut)
	}
	if len(s.requests) != 3 || s.requests[0] != "follow=true&tail=100" ||
		s.requests[1] != "follow=true&since=2026-10-11T10%3A00%3A03Z" ||
		s.requests[2] != "follow=true&since=2026-10-11T10%3A00%3A04Z" {
		t.Errorf("requests = %q; want the tail once, then since the last line seen", s.requests)
	}
	// A stream that carried lines earns a fresh first delay.
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != time.Second {
		t.Errorf("waited %v before reconnecting, want 1s each", waits)
	}
}

func TestLogsFollowResumesFromTheLastHeartbeatWhenNothingWasPrinted(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	s := &cutStream{}
	f.follow.serve = func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.URL.RawQuery)
		first := len(s.requests) == 1
		s.mu.Unlock()
		if !first {
			w.Header().Set(api.LogSinceHeader, r.URL.Query().Get("since"))
			json.NewEncoder(w).Encode(line(1, 50, "after the gap"))
			return
		}
		json.NewEncoder(w).Encode(api.LogHeartbeat{Heartbeat: true, Time: at(20)})
		json.NewEncoder(w).Encode(api.LogHeartbeat{Heartbeat: true, Time: at(40)})
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	var waits []time.Duration
	f.after = noWait(&waits)

	out, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Fatalf("logs -f: %v", err)
	}
	if out != "after the gap\n" {
		t.Errorf("stdout = %q: a heartbeat is not a line", out)
	}
	if len(s.requests) != 2 || s.requests[1] != "follow=true&since=2026-10-11T10%3A00%3A40Z" {
		t.Errorf("requests = %q; want the second since the last heartbeat", s.requests)
	}
	if !strings.Contains(errOut, "Reconnected; nothing was missed.") {
		t.Errorf("stderr:\n%s", errOut)
	}
}

func TestLogsFollowSaysWhatMayBeMissingWithAnOlderAgent(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	s := &cutStream{older: true, pages: []streamPage{
		{lines: []api.LogLine{line(1, 1, "one"), line(1, 2, "two")}},
		// The older agent sends its tail again, which is not printed again.
		{lines: []api.LogLine{line(1, 1, "one"), line(1, 2, "two"), line(1, 9, "nine")}, ends: true},
	}}
	f.follow.serve = s.serve
	var waits []time.Duration
	f.after = noWait(&waits)

	out, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Fatalf("logs -f: %v", err)
	}
	if out != "one\ntwo\nnine\n" {
		t.Errorf("stdout = %q", out)
	}
	want := "Reconnected; lines between " + at(2).Local().Format("15:04:05") + " and " + fixedNow.Local().Format("15:04:05") + " may be missing: this agent is older than 0.9"
	if !strings.Contains(errOut, want) {
		t.Errorf("stderr:\n%s\nwant %q", errOut, want)
	}
}

func TestLogsFollowGivesUpAfterFiveLossesInARow(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	s := &cutStream{pages: []streamPage{{lines: []api.LogLine{line(1, 1, "one")}}}}
	f.follow.serve = s.serve
	var waits []time.Duration
	f.after = noWait(&waits)

	out, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err == nil {
		t.Fatal("a stream lost again and again must end in an error")
	}
	if out != "one\n" {
		t.Errorf("stdout = %q", out)
	}
	// The first stream carried a line; the next five carried nothing.
	if len(s.requests) != 6 {
		t.Errorf("%d requests, want the stream and five tries: %q", len(s.requests), s.requests)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waited %v; want a second after the stream that carried lines, then twice as long each time", waits)
	}
	if got := Render(err); !strings.HasPrefix(got, "Error: log stream interrupted: ") ||
		!strings.Contains(got, "The stream was lost 5 times in a row and nothing came through in between. See whether the agent is reachable with: shipwick doctor") {
		t.Errorf("rendered:\n%s", got)
	}
	if strings.Count(errOut, "Reconnected") != 5 {
		t.Errorf("stderr:\n%s", errOut)
	}
}

func TestLogsFollowReconnectsWhenTheAgentStopsAndWaitsForItToComeBack(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	requests := 0
	f.follow.serve = func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			// The agent ends the stream to restart, and says so.
			json.NewEncoder(w).Encode(line(1, 1, "one"))
			json.NewEncoder(w).Encode(api.LogEnd{End: api.LogEndShutdown})
		case 2:
			// The proxy answers for an agent that is not back yet.
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.Header().Set(api.LogSinceHeader, r.URL.Query().Get("since"))
			json.NewEncoder(w).Encode(line(1, 1, "one"))
			json.NewEncoder(w).Encode(line(1, 2, "two"))
			json.NewEncoder(w).Encode(api.LogEnd{End: api.LogEndContainers})
		}
	}
	var waits []time.Duration
	f.after = noWait(&waits)

	out, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Fatalf("logs -f: %v", err)
	}
	if out != "one\ntwo\n" || requests != 3 {
		t.Errorf("stdout = %q after %d requests", out, requests)
	}
	if !strings.Contains(errOut, "Reconnected; nothing was missed.") || !strings.Contains(errOut, "log stream ended: the containers were stopped or replaced") {
		t.Errorf("stderr:\n%s", errOut)
	}
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Errorf("waited %v, want 1s then 2s", waits)
	}
}

func TestLogsFollowEndsAtOnceOnCtrlCWhileReconnecting(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	s := &cutStream{}
	f.follow.serve = s.serve

	ctx, cancel := context.WithCancel(context.Background())
	// Ctrl-C arrives while the CLI waits before reconnecting: the timer
	// never fires.
	f.after = func(time.Duration, func()) func() {
		cancel()
		return func() {}
	}
	_, _, err := f.runCtx(ctx, t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Errorf("Ctrl-C is the normal way out, got %v", err)
	}
	if len(s.requests) != 1 {
		t.Errorf("requests after Ctrl-C: %q", s.requests)
	}
}

func TestLogsFollowDoesNotRetryWhatTheAgentRefused(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	requests := 0
	f.follow.serve = func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			json.NewEncoder(w).Encode(line(1, 1, "one"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		// Deleted while the connection was down.
		respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "application my-api not found"})
	}
	var waits []time.Duration
	f.after = noWait(&waits)

	_, _, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err == nil || !strings.Contains(err.Error(), "not found") || requests != 2 {
		t.Errorf("err = %v after %d requests; want the agent's refusal at once", err, requests)
	}
}
