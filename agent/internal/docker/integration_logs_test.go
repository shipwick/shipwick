//go:build integration

package docker

import (
	"fmt"
	"testing"
	"time"
)

// What the log archive rests on: a container that is started again keeps the
// log of its earlier run and says when that run stopped, and the daemon
// returns the part of the log between two times — after it has taken the
// tail, which is why a run that ended is read without one.
func TestIntegrationReadLogsOfARunThatEnded(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-logs-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage,
		// The pauses are inside the container: the daemon reads the two
		// streams through pipes of their own, and lines written within
		// microseconds of each other on different streams have no order.
		Entrypoint: []string{"sh"}, Command: []string{"-c", "echo one; sleep 0.2; echo two >&2; sleep 0.2; echo three; exit 3"},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(ctx, id) })

	run := func() Container {
		t.Helper()
		if err := rt.StartContainer(ctx, id); err != nil {
			t.Fatalf("StartContainer: %v", err)
		}
		if code, err := rt.WaitContainer(ctx, id); err != nil || code != 3 {
			t.Fatalf("WaitContainer = %d, %v", code, err)
		}
		c, err := rt.InspectContainer(ctx, id)
		if err != nil {
			t.Fatalf("InspectContainer: %v", err)
		}
		if c.Running || c.FinishedAt == nil || c.StartedAt == nil || c.FinishedAt.Before(*c.StartedAt) {
			t.Fatalf("a stopped container says when it stopped: %+v", c)
		}
		return c
	}
	read := func(w LogWindow) []LogEntry {
		t.Helper()
		var lines []LogEntry
		if err := rt.ReadLogs(ctx, id, w, func(e LogEntry) { lines = append(lines, e) }); err != nil {
			t.Fatalf("ReadLogs(%+v): %v", w, err)
		}
		return lines
	}

	if c, _ := rt.InspectContainer(ctx, id); c.FinishedAt != nil {
		t.Errorf("a container that never ran has no stop time: %v", c.FinishedAt)
	}
	first := run()
	lines := read(LogWindow{})
	if len(lines) != 3 || lines[0].Message != "one" || lines[1].Message != "two" || lines[1].Stream != "stderr" || lines[2].Stream != "stdout" ||
		lines[0].Time.IsZero() || lines[2].Time.After(*first.FinishedAt) {
		t.Fatalf("the first run's lines, in the order they were written and before its stop: %+v (stopped %v)", lines, first.FinishedAt)
	}
	if tail := read(LogWindow{Tail: 2}); len(tail) != 2 || tail[0].Message != "two" {
		t.Errorf("tail 2: %+v", tail)
	}

	second := run()
	if !second.FinishedAt.After(*first.FinishedAt) {
		t.Errorf("stop times: %v then %v", first.FinishedAt, second.FinishedAt)
	}
	if all := read(LogWindow{}); len(all) != 6 {
		t.Fatalf("the log of both runs: %+v", all)
	}
	// The first run alone, and the second alone from where the first ended.
	if got := read(LogWindow{Until: *first.FinishedAt}); len(got) != 3 || !got[2].Time.Equal(lines[2].Time) {
		t.Errorf("until the first stop: %+v", got)
	}
	if got := read(LogWindow{Since: first.FinishedAt.Add(time.Nanosecond)}); len(got) != 3 || !got[0].Time.After(*first.FinishedAt) {
		t.Errorf("since the first stop: %+v", got)
	}
	if got := read(LogWindow{Since: lines[2].Time.Add(time.Nanosecond), Until: *first.FinishedAt}); len(got) != 0 {
		t.Errorf("after the first run's last line and before its stop there is nothing: %+v", got)
	}
	// The tail is taken first: the first run is not in the last two lines.
	if got := read(LogWindow{Until: *first.FinishedAt, Tail: 2}); len(got) != 0 {
		t.Errorf("until the first stop, tail 2: %+v, want nothing", got)
	}
}
