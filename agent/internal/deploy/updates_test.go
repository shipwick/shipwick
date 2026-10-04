package deploy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/version"
)

// asker stands in for GitHub: it counts the questions and answers with tag,
// or fails.
type asker struct {
	asked int
	tag   string
	err   error
}

func (a *asker) latest(context.Context) (string, error) {
	a.asked++
	return a.tag, a.err
}

// runs sets the version the agent believes it is for the length of a test.
func runs(t *testing.T, v string) {
	t.Helper()
	was := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = was })
}

func updateHarness(t *testing.T, a *asker) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	file := filepath.Join(t.TempDir(), "update-check.json")
	h.engine.opts.Updates = UpdateOptions{Latest: a.latest, StateFile: file}
	return h, file
}

func TestTheServerSaysWhenANewerReleaseExists(t *testing.T) {
	runs(t, "v0.7.0")
	a := &asker{tag: "v0.7.1"}
	h, _ := updateHarness(t, a)
	ctx := context.Background()

	server, err := h.engine.Server(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u := server.Update; u == nil || !u.Enabled || u.LatestVersion != "" || u.CheckedAt != nil || u.Available {
		t.Fatalf("before the first answer: %+v", u)
	}

	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	h.engine.checkForUpdate(ctx, now)
	server, _ = h.engine.Server(ctx)
	if u := server.Update; u.LatestVersion != "v0.7.1" || u.CheckedAt == nil || !u.CheckedAt.Equal(now) || !u.Available {
		t.Fatalf("after the answer: %+v", u)
	}
}

func TestAReleaseThatIsNotNewerIsNotAnUpdate(t *testing.T) {
	for running, available := range map[string]bool{"v0.7.0": false, "v0.7.1": false, "v0.8.0-rc.1": false, "dev": false, "v0.6.0": true, "v0.7.0-rc.2": true} {
		t.Run(running, func(t *testing.T) {
			runs(t, running)
			h, _ := updateHarness(t, &asker{tag: "v0.7.0"})
			h.engine.checkForUpdate(context.Background(), time.Now())
			if u := h.engine.updateStatus(); u.Available != available || u.LatestVersion != "v0.7.0" {
				t.Errorf("an agent that runs %s, latest v0.7.0: %+v", running, u)
			}
		})
	}
}

func TestTheAgentAsksOnceADay(t *testing.T) {
	a := &asker{tag: "v0.7.1"}
	h, _ := updateHarness(t, a)
	ctx := context.Background()
	start := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)

	// The loop looks every minute.
	for minute := 0; minute < 24*60; minute++ {
		h.engine.checkForUpdate(ctx, start.Add(time.Duration(minute)*time.Minute))
	}
	if a.asked != 1 {
		t.Fatalf("asked %d times in a day, want once", a.asked)
	}
	a.tag = "v0.7.2"
	h.engine.checkForUpdate(ctx, start.Add(24*time.Hour))
	if u := h.engine.updateStatus(); a.asked != 2 || u.LatestVersion != "v0.7.2" {
		t.Fatalf("a day later: asked %d times, %+v", a.asked, u)
	}
}

func TestAServerThatCannotAskStaysQuietAndKeepsWhatItKnew(t *testing.T) {
	runs(t, "v0.7.0")
	a := &asker{tag: "v0.7.1"}
	h, _ := updateHarness(t, a)
	var log bytes.Buffer
	h.engine.log = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()
	start := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	h.engine.checkForUpdate(ctx, start)

	a.err = errors.New("dial tcp: i/o timeout")
	for minute := 0; minute < 2*24*60; minute++ {
		h.engine.checkForUpdate(ctx, start.Add(24*time.Hour+time.Duration(minute)*time.Minute))
	}
	if a.asked != 3 {
		t.Errorf("asked %d times in three days, want three: a failure is not asked again sooner", a.asked)
	}
	if log.Len() != 0 {
		t.Errorf("a server without a way out wrote to its log:\n%s", log.String())
	}
	if u := h.engine.updateStatus(); u.LatestVersion != "v0.7.1" || !u.CheckedAt.Equal(start) || !u.Available {
		t.Errorf("the last answer was lost: %+v", u)
	}
}

func TestARestartDoesNotAskAgain(t *testing.T) {
	runs(t, "v0.7.0")
	a := &asker{tag: "v0.7.1"}
	h, file := updateHarness(t, a)
	ctx := context.Background()
	start := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	h.engine.checkForUpdate(ctx, start)
	if info, err := os.Stat(file); err != nil || info.Size() == 0 {
		t.Fatalf("nothing was kept: %v", err)
	}

	again := &asker{tag: "v0.7.2"}
	restarted, _ := updateHarness(t, again)
	restarted.engine.opts.Updates.StateFile = file
	restarted.engine.loadUpdateState()
	restarted.engine.checkForUpdate(ctx, start.Add(time.Hour))
	if u := restarted.engine.updateStatus(); again.asked != 0 || u.LatestVersion != "v0.7.1" || !u.Available {
		t.Fatalf("an hour later, after a restart: asked %d times, %+v", again.asked, u)
	}
	restarted.engine.checkForUpdate(ctx, start.Add(25*time.Hour))
	if again.asked != 1 {
		t.Errorf("a day later: asked %d times, want once", again.asked)
	}
}

func TestAStateFileThatIsNotOneIsAskedAbout(t *testing.T) {
	a := &asker{tag: "v0.7.1"}
	h, file := updateHarness(t, a)
	for _, content := range []string{"not json", `{"asked_at":"2026-10-04T09:00:00Z","latest_version":"<script>","checked_at":"2026-10-04T09:00:00Z"}`} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		h.engine.loadUpdateState()
		if u := h.engine.updateStatus(); u.LatestVersion != "" || u.CheckedAt != nil {
			t.Errorf("%s was believed: %+v", content, u)
		}
	}
}

func TestAnAgentToldNotToAskDoesNot(t *testing.T) {
	h := newHarness(t)
	h.engine.StartUpdateCheck()
	server, err := h.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u := server.Update; u == nil || u.Enabled || u.LatestVersion != "" || u.CheckedAt != nil || u.Available {
		t.Fatalf("update = %+v, want a status that says it is off", u)
	}
}

func TestTheStateFileHoldsNothingButTheAnswer(t *testing.T) {
	h, file := updateHarness(t, &asker{tag: "v0.7.1"})
	h.engine.checkForUpdate(context.Background(), time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != `{"asked_at":"2026-10-04T09:00:00Z","latest_version":"v0.7.1","checked_at":"2026-10-04T09:00:00Z"}` {
		t.Errorf("state file = %s", got)
	}
}
