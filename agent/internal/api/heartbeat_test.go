package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

// handTimer replaces the stream's idle timer with one the test fires: every
// Reset is counted on resets, Stop closes stopped.
type handTimer struct {
	fire    chan time.Time
	resets  chan struct{}
	stopped chan struct{}
}

func newHandTimer(f *fixture) *handTimer {
	h := &handTimer{fire: make(chan time.Time), resets: make(chan struct{}, 64), stopped: make(chan struct{})}
	f.api.newTimer = func(time.Duration) streamTimer {
		return streamTimer{C: h.fire, Reset: func(time.Duration) { h.resets <- struct{}{} }, Stop: func() { close(h.stopped) }}
	}
	return h
}

// elapse lets the idle interval pass: the stream must be waiting on it.
func (h *handTimer) elapse(t *testing.T, now time.Time) {
	t.Helper()
	select {
	case h.fire <- now:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream is not waiting on its idle timer")
	}
}

// streamRecord is one line of the stream, whichever kind it is.
type streamRecord struct {
	api.LogLine
	Heartbeat bool   `json:"heartbeat"`
	End       string `json:"end"`
}

func readRecord(t *testing.T, sc *bufio.Scanner) streamRecord {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("stream ended: %v", sc.Err())
	}
	var rec streamRecord
	if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
		t.Fatalf("decode %q: %v", sc.Bytes(), err)
	}
	return rec
}

func TestFollowLogsSendsAHeartbeatOnlyWhileTheApplicationIsQuiet(t *testing.T) {
	f := newFixture(t)
	timer := newHandTimer(f)
	clock := time.Date(2026, 10, 11, 10, 0, 20, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := f.openStream(ctx, "/api/v1/applications/my-api/logs?follow=true&tail=10")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)

	// Two replicas, a line each: the stream is busy, and each line starts
	// the idle interval over.
	for range 2 {
		if rec := readRecord(t, sc); rec.Heartbeat || rec.Message == "" {
			t.Fatalf("want a log line, got %+v", rec)
		}
	}
	for range 2 {
		select {
		case <-timer.resets:
		case <-time.After(5 * time.Second):
			t.Fatal("a line was sent and the idle interval was not started over")
		}
	}

	// Then nothing: the interval passes, and a heartbeat goes out with the
	// agent's clock; another interval, another heartbeat.
	timer.elapse(t, clock)
	if rec := readRecord(t, sc); !rec.Heartbeat || !rec.Time.Equal(clock) || rec.Message != "" {
		t.Fatalf("want a heartbeat at %s, got %+v", clock, rec)
	}
	clock = clock.Add(heartbeatInterval)
	timer.elapse(t, clock)
	if rec := readRecord(t, sc); !rec.Heartbeat || !rec.Time.Equal(clock) {
		t.Fatalf("want a second heartbeat at %s, got %+v", clock, rec)
	}

	// The agent shutting down ends the stream with a line that says so, and
	// the heartbeat stops with it.
	f.api.Close()
	if rec := readRecord(t, sc); rec.End != api.LogEndShutdown || rec.Heartbeat || rec.Message != "" {
		t.Errorf("want the stream to end with %q, got %+v", api.LogEndShutdown, rec)
	}
	if sc.Scan() {
		t.Errorf("after the stream ended: %q", sc.Bytes())
	}
	select {
	case <-timer.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle timer was not stopped with the stream")
	}
}

func TestFollowLogsSinceBeginsAtTheTimeAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	// Each replica printed a line before the time and one after it.
	var since time.Time
	for _, c := range f.rt.Containers() {
		f.rt.WriteLogs(c.ID, "before")
	}
	for _, c := range f.rt.Containers() {
		f.rt.ReadLogs(context.Background(), c.ID, docker.LogWindow{}, func(e docker.LogEntry) {
			if e.Time.After(since) {
				since = e.Time
			}
		})
	}
	since = since.Add(time.Microsecond)
	for _, c := range f.rt.Containers() {
		f.rt.WriteLogs(c.ID, "after")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := f.openStream(ctx, "/api/v1/applications/my-api/logs?follow=true&tail=5&since="+since.Format(time.RFC3339Nano))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get(api.LogSinceHeader); got != since.Format(time.RFC3339Nano) {
		t.Errorf("%s = %q, want the time as given", api.LogSinceHeader, got)
	}

	sc := bufio.NewScanner(resp.Body)
	for range 2 {
		if rec := readRecord(t, sc); rec.Message != "after" {
			t.Errorf("want only the lines from the time on, got %+v", rec)
		}
	}
	follows := f.rt.LogFollows()
	if len(follows) != 2 || !follows[0].Since.Equal(since) || !follows[1].Since.Equal(since) {
		t.Errorf("the runtime was asked to follow from %v, want %s twice", follows, since)
	}
}

func TestLogsSinceIsRefusedOutsideAFollowAndWhenItIsNotATime(t *testing.T) {
	f := newFixture(t)
	for path, want := range map[string]string{
		"/api/v1/applications/my-api/logs?since=2026-10-11T10:00:00Z":             "since goes with follow=true",
		"/api/v1/applications/my-api/logs?follow=true&since=yesterday":            "since must be a time in RFC 3339",
		"/api/v1/applications/my-api/logs?follow=true&since=2026-10-11+10:00:00Z": "since must be a time in RFC 3339",
	} {
		status, body := f.do("GET", path, "")
		e := decodeError(t, body)
		if status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, want) {
			t.Errorf("%s: status %d, %s %q; want 400 with %q", path, status, e.Code, e.Message, want)
		}
	}
}
