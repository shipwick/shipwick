package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/notify"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func TestMemoryAlertingNeedsThreeConsecutiveSamples(t *testing.T) {
	const limit, interval = 1000, 30 * time.Second
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	// readings are spaced one interval apart, the last one taken now; a
	// negative one stands for a sample that was never taken.
	samples := func(readings ...int64) []store.MetricSample {
		var out []store.MetricSample
		for i, bytes := range readings {
			if bytes >= 0 {
				out = append(out, store.MetricSample{At: now.Add(-time.Duration(len(readings)-1-i) * interval), MemoryBytes: bytes})
			}
		}
		return out
	}
	tests := []struct {
		name    string
		was     bool
		samples []store.MetricSample
		want    bool
	}{
		{"three samples at the threshold raise it", false, samples(900, 950, 900), true},
		{"two are not enough", false, samples(950, 950), false},
		{"one below among the last three is not consecutive", false, samples(950, 890, 950), false},
		{"only the last three count", false, samples(100, 950, 950, 950), true},
		{"a missed sample breaks the run", false, samples(950, 950, -1, 950), false},
		{"three old samples say nothing about now", false, samples(950, 950, 950, -1, -1, -1), false},
		{"once raised it stands above the clear level", true, samples(950, 950, 800), true},
		{"and is cleared below it", true, samples(950, 950, 799), false},
		{"a replica that is not sampled keeps its alert", true, nil, true},
		{"and does not get one", false, nil, false},
	}
	for _, tt := range tests {
		if got := memoryAlerting(tt.was, tt.samples, now, interval, limit, 90); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

func TestDiskSeverityHasDistanceBetweenRaisingAndClearing(t *testing.T) {
	tests := []struct {
		was     string
		percent int64
		want    string
	}{
		{"", 84, ""},
		{"", 85, api.SeverityWarning},
		{api.SeverityWarning, 80, api.SeverityWarning},
		{api.SeverityWarning, 79, ""},
		{"", 95, api.SeverityCritical},
		{api.SeverityWarning, 96, api.SeverityCritical},
		{api.SeverityCritical, 90, api.SeverityCritical},
		{api.SeverityCritical, 89, api.SeverityWarning},
		{api.SeverityCritical, 79, ""},
	}
	for _, tt := range tests {
		if got := diskSeverity(tt.was, api.DiskUsage{TotalBytes: 100, UsedBytes: tt.percent}, 85); got != tt.want {
			t.Errorf("was %q, %d%% used: got %q, want %q", tt.was, tt.percent, got, tt.want)
		}
	}
}

func TestRestartAlertingCountsRestartsWithinTenMinutes(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ago := func(minutes ...int) []time.Time {
		var out []time.Time
		for _, m := range minutes {
			out = append(out, now.Add(-time.Duration(m)*time.Minute))
		}
		return out
	}
	tests := []struct {
		name     string
		was      bool
		restarts []time.Time
		held     bool
		want     bool
	}{
		{"three within the window", false, ago(9, 5, 0), false, true},
		{"the first has left the window", false, ago(11, 5, 0), false, false},
		{"which also clears it", true, ago(11, 5, 0), false, false},
		{"not raised while the application is down or the replica crash-looping", false, ago(2, 1, 0), true, false},
		{"nor cleared", true, ago(30), true, true},
	}
	for _, tt := range tests {
		if got := restartAlerting(tt.was, tt.restarts, now, tt.held); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

func TestUnhealthySeverityGrowsWithTime(t *testing.T) {
	since := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for after, want := range map[time.Duration]string{
		0:                           "",
		5*time.Minute - time.Second: "",
		5 * time.Minute:             api.SeverityWarning,
		time.Hour - time.Second:     api.SeverityWarning,
		time.Hour:                   api.SeverityCritical,
		24 * time.Hour:              api.SeverityCritical,
		-time.Minute:                "",
	} {
		if got := unhealthySeverity(since, since.Add(after)); got != want {
			t.Errorf("after %s: got %q, want %q", after, got, want)
		}
	}
}

// alertKinds lists the active alerts as "kind severity", oldest first.
func alertKinds(e *Engine) string {
	var out []string
	for _, a := range e.Alerts() {
		out = append(out, a.Kind+" "+a.Severity)
	}
	return strings.Join(out, ", ")
}

func TestMemoryAlertIsRaisedOnceAndClearedOnce(t *testing.T) {
	ctx := context.Background()
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 1)) // limited to 256 MB
	rec := notified(s.harness)
	round := func() {
		s.advance(30 * time.Second)
		s.engine.checkAlerts(ctx, s.now)
	}

	s.rt.MemoryUsed = 240 << 20
	for range 3 { // the first reading primes the CPU rate; then two samples
		round()
	}
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Fatalf("notifications after two samples = %v; it takes three in a row", kinds)
	}
	round()
	ev := lastEvent(t, rec, notify.AlertRaised)
	want := "my-api replica 1 is at 93% of its memory limit (240 MB of 256 MB). At the limit it is killed and restarted; raise resources.memory in deploy.yaml, or watch it with: shipwick status my-api"
	if ev.Message != want || ev.Application != "my-api" || ev.Alert == nil || *ev.Alert != (notify.Alert{Kind: "memory", Severity: "warning", Replica: 1}) || !ev.At.Equal(s.now) {
		t.Errorf("event = %+v (alert %+v)\nwant message %q", ev, ev.Alert, want)
	}
	alerts := s.engine.Alerts()
	if len(alerts) != 1 || alerts[0] != (api.Alert{Kind: "memory", Severity: "warning", Application: "my-api", Replica: 1, Message: want, Since: s.now}) {
		t.Errorf("active alerts = %+v", alerts)
	}

	// It lasts, and hovers between the two levels: nothing more to say.
	round()
	s.rt.MemoryUsed = 210 << 20 // 82 %
	round()
	if kinds := rec.Kinds(); len(kinds) != 1 {
		t.Fatalf("notifications = %v; an alert is raised once while it lasts", kinds)
	}

	s.rt.MemoryUsed = 200 << 20 // 78 %
	round()
	if ev := lastEvent(t, rec, notify.AlertCleared); ev.Message != "my-api replica 1 is back at 78% of its memory limit (200 MB of 256 MB)" || ev.Alert.Kind != "memory" {
		t.Errorf("event = %+v", ev)
	}
	round()
	if kinds := rec.Kinds(); len(kinds) != 2 || len(s.engine.Alerts()) != 0 {
		t.Errorf("notifications = %v, active = %q; cleared once, and gone", kinds, alertKinds(s.engine))
	}

	// Both are in the application's own feed.
	events, err := s.engine.Events(ctx, "my-api", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != api.EventAlert || events[1].Level != api.LevelWarn || events[1].Message != want || events[0].Level != api.LevelInfo {
		t.Errorf("events = %+v; the alert and its clearing", events)
	}
}

func TestMemoryAlertNeedsAMemoryLimit(t *testing.T) {
	s := newSampled(t)
	a := app("my-api", "my-api:1.0", 1)
	a.Resources.MemoryBytes = 0
	s.deploy(a)
	rec := notified(s.harness)

	s.rt.MemoryUsed = 64 << 30
	for range 6 {
		s.advance(30 * time.Second)
		s.engine.checkAlerts(context.Background(), s.now)
	}
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Errorf("notifications = %v; without resources.memory there is no limit to come close to", kinds)
	}
}

func TestMemoryAlertThresholdIsConfigurable(t *testing.T) {
	s := newSampled(t)
	s.engine.opts.AlertMemoryPercent = 75
	s.deploy(app("my-api", "my-api:1.0", 1))
	rec := notified(s.harness)

	s.rt.MemoryUsed = 200 << 20 // 78 %
	for range 4 {
		s.advance(30 * time.Second)
		s.engine.checkAlerts(context.Background(), s.now)
	}
	lastEvent(t, rec, notify.AlertRaised)
}

func TestDiskAlertWarnsTurnsCriticalAndClears(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rec := notified(h)
	used, known := int64(84), true
	h.engine.opts.DiskUsage = func() (api.DiskUsage, bool) {
		return api.DiskUsage{TotalBytes: 100 << 30, UsedBytes: used << 30}, known
	}
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	check := func(percent int64) {
		used, now = percent, now.Add(30*time.Second)
		h.engine.checkAlerts(ctx, now)
	}

	check(84)
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Fatalf("notifications at 84%% = %v", kinds)
	}
	check(86)
	raised := now
	ev := lastEvent(t, rec, notify.AlertRaised)
	if ev.Message != "The server's disk is 86% full (14 GB of 100 GB free). See what takes the space with: docker system df" ||
		ev.Application != "" || *ev.Alert != (notify.Alert{Kind: "disk", Severity: "warning"}) {
		t.Errorf("event = %+v (alert %+v)", ev, ev.Alert)
	}
	check(90)
	if kinds := rec.Kinds(); len(kinds) != 1 {
		t.Fatalf("notifications = %v; a warning that lasts is one message", kinds)
	}

	check(96)
	ev = lastEvent(t, rec, notify.AlertRaised)
	if !strings.HasPrefix(ev.Message, "The server's disk is 96% full (4 GB of 100 GB free). Deployments, databases and logs fail when it runs out.") || ev.Alert.Severity != "critical" {
		t.Errorf("event = %+v (alert %+v)", ev, ev.Alert)
	}
	if alerts := h.engine.Alerts(); len(alerts) != 1 || alerts[0].Severity != "critical" || !alerts[0].Since.Equal(raised) {
		t.Errorf("active alerts = %+v; one alert that grew more severe, since when it was first raised", alerts)
	}

	// Down again, level by level: the state follows, nobody is told.
	check(92)
	if got := alertKinds(h.engine); got != "disk critical" {
		t.Errorf("at 92%% after 96%%: %q", got)
	}
	check(88)
	if got := alertKinds(h.engine); got != "disk warning" {
		t.Errorf("at 88%%: %q", got)
	}
	// The disk cannot be measured for a moment: nothing changes.
	known = false
	check(10)
	known = true
	if kinds := rec.Kinds(); len(kinds) != 2 || alertKinds(h.engine) != "disk warning" {
		t.Fatalf("notifications = %v, active = %q", kinds, alertKinds(h.engine))
	}

	check(79)
	if ev := lastEvent(t, rec, notify.AlertCleared); ev.Message != "The server's disk is back to 79% full (21 GB of 100 GB free)" {
		t.Errorf("message = %q", ev.Message)
	}
	check(79)
	if kinds := rec.Kinds(); len(kinds) != 3 || len(h.engine.Alerts()) != 0 {
		t.Errorf("notifications = %v, active = %q", kinds, alertKinds(h.engine))
	}
}

func TestDiskIsUnknownWithoutAMeasurement(t *testing.T) {
	h := newHarness(t)
	rec := notified(h)
	h.engine.checkAlerts(context.Background(), time.Now())
	if h.engine.Disk() != nil || len(rec.Kinds()) != 0 {
		t.Errorf("disk = %+v, notifications = %v; where the disk cannot be measured there is nothing to report", h.engine.Disk(), rec.Kinds())
	}
	server, err := h.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if server.Disk != nil || server.Alerts == nil || len(server.Alerts) != 0 {
		t.Errorf("server view: disk = %+v, alerts = %#v; want null and an empty list", server.Disk, server.Alerts)
	}
}

func TestRestartAlertForAReplicaThatKeepsStopping(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 2))
	rec := notified(s.harness)

	// Replica 1 dies every two minutes: it stays up long enough to be
	// forgiven each time, so it never becomes a crash loop, and its sibling
	// keeps the application up, so nothing is ever "down".
	crash := func() {
		s.rt.Crash(s.container(t, 1).ID, 137)
		s.advance(time.Second)
		s.advance(time.Second)
		if !s.container(t, 1).Running {
			t.Fatal("precondition: replica 1 restarted")
		}
	}
	crash()
	s.advance(2 * time.Minute)
	crash()
	s.advance(2 * time.Minute)
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Fatalf("notifications after two restarts = %v", kinds)
	}
	crash()
	ev := lastEvent(t, rec, notify.AlertRaised)
	want := "my-api replica 1 was restarted 3 times in the last 10 minutes. See why it keeps stopping with: shipwick logs my-api"
	if ev.Message != want || *ev.Alert != (notify.Alert{Kind: "restarts", Severity: "warning", Replica: 1}) {
		t.Errorf("event = %+v (alert %+v)\nwant message %q", ev, ev.Alert, want)
	}
	if _, looping := s.engine.sup.snapshot(s.container(t, 1).ID); looping {
		t.Fatal("precondition: not a crash loop")
	}

	s.advance(2 * time.Minute)
	crash()
	if kinds := rec.Kinds(); len(kinds) != 1 {
		t.Fatalf("notifications = %v; a fourth restart is the same alert", kinds)
	}

	// Ten quiet minutes later the restarts have left the window.
	s.advance(5 * time.Minute)
	if got := alertKinds(s.engine); got != "restarts warning" {
		t.Fatalf("five minutes later: %q", got)
	}
	s.advance(6 * time.Minute)
	if ev := lastEvent(t, rec, notify.AlertCleared); ev.Message != "my-api replica 1 has stayed up: fewer than 3 restarts in the last 10 minutes" {
		t.Errorf("message = %q", ev.Message)
	}
	if kinds := rec.Kinds(); len(kinds) != 2 || len(s.engine.Alerts()) != 0 {
		t.Errorf("notifications = %v, active = %q", kinds, alertKinds(s.engine))
	}
}

func TestRestartAlertLeavesAnOutageToItsOwnNotification(t *testing.T) {
	s := newSupervised(t)
	s.deploy(app("my-api", "my-api:1.0", 1))
	rec := notified(s.harness)

	s.rt.CrashImages["my-api:1.0"] = true
	s.rt.Crash(s.container(t, 1).ID, 1)
	for range 120 { // five restarts within a minute, into the crash loop
		s.advance(time.Second)
	}
	if kinds := rec.Kinds(); len(kinds) != 1 || kinds[0] != notify.ApplicationDown {
		t.Errorf("notifications = %v; the application is down and was reported as that", kinds)
	}
	if got := alertKinds(s.engine); got != "" {
		t.Errorf("active alerts = %q", got)
	}
}

func TestUnhealthyAlertAfterFiveMinutesAndAgainAfterAnHour(t *testing.T) {
	ctx := context.Background()
	s := newSupervised(t)
	a := app("my-api", "my-api:1.0", 2)
	a.Restart.Policy = spec.RestartNever
	s.deploy(a)
	rec := notified(s.harness)

	id := s.container(t, 1).ID
	s.rt.Crash(id, 1)
	s.advance(time.Second)
	s.advance(4 * time.Minute)
	if kinds := rec.Kinds(); len(kinds) != 0 {
		t.Fatalf("notifications after four minutes = %v; one of two replicas missing is not an outage, and not yet an alert", kinds)
	}
	s.advance(time.Minute)
	ev := lastEvent(t, rec, notify.AlertRaised)
	if ev.Message != "my-api has not been healthy for 5 minutes: 1/2 replicas ready. See why with: shipwick status my-api" ||
		*ev.Alert != (notify.Alert{Kind: "unhealthy", Severity: "warning"}) {
		t.Errorf("event = %+v (alert %+v)", ev, ev.Alert)
	}

	s.advance(54 * time.Minute)
	if kinds := rec.Kinds(); len(kinds) != 1 {
		t.Fatalf("notifications = %v; nothing between five minutes and the hour", kinds)
	}
	s.advance(time.Minute)
	ev = lastEvent(t, rec, notify.AlertRaised)
	if ev.Message != "my-api has not been healthy for an hour: 1/2 replicas ready. See why with: shipwick status my-api" || ev.Alert.Severity != "critical" {
		t.Errorf("event = %+v (alert %+v)", ev, ev.Alert)
	}
	s.advance(24 * time.Hour)
	if kinds := rec.Kinds(); len(kinds) != 2 || alertKinds(s.engine) != "unhealthy critical" {
		t.Fatalf("notifications = %v, active = %q; after the hour it is not repeated", kinds, alertKinds(s.engine))
	}

	// Degraded never was "down", so nothing else announces the recovery.
	if err := s.rt.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if ev := lastEvent(t, rec, notify.AlertCleared); ev.Message != "my-api is healthy again: 2/2 replicas ready" {
		t.Errorf("message = %q", ev.Message)
	}
	if kinds := rec.Kinds(); len(kinds) != 3 || len(s.engine.Alerts()) != 0 {
		t.Errorf("notifications = %v, active = %q", kinds, alertKinds(s.engine))
	}
}

func TestRecoveryOfADownApplicationClearsItsAlertWithoutASecondMessage(t *testing.T) {
	s := newSupervised(t)
	a := app("my-api", "my-api:1.0", 1)
	a.Restart.Policy = spec.RestartNever
	s.deploy(a)
	rec := notified(s.harness)

	id := s.container(t, 1).ID
	s.rt.Crash(id, 1)
	s.advance(time.Second)
	s.advance(5 * time.Minute)
	if kinds := strings.Join(rec.Kinds(), " "); kinds != "application.down alert.raised" {
		t.Fatalf("notifications = %s", kinds)
	}
	if err := s.rt.StartContainer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if kinds := strings.Join(rec.Kinds(), " "); kinds != "application.down alert.raised application.recovered" || len(s.engine.Alerts()) != 0 {
		t.Errorf("notifications = %s, active = %q; the recovery says it all", kinds, alertKinds(s.engine))
	}
	if events := s.appEvents(t, "my-api"); events[len(events)-1] != "my-api is healthy again: 1/1 replicas ready" {
		t.Errorf("events = %q; the clearing is still in the feed", events)
	}
}

func TestStoppingAnApplicationDropsItsAlerts(t *testing.T) {
	s := newSupervised(t)
	a := app("my-api", "my-api:1.0", 2)
	a.Restart.Policy = spec.RestartNever
	s.deploy(a)
	rec := notified(s.harness)

	s.rt.Crash(s.container(t, 1).ID, 1)
	s.advance(time.Second)
	s.advance(5 * time.Minute)
	if got := alertKinds(s.engine); got != "unhealthy warning" {
		t.Fatalf("precondition: active = %q", got)
	}
	if err := s.engine.Stop(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	s.advance(time.Second)
	if kinds := rec.Kinds(); len(kinds) != 1 || len(s.engine.Alerts()) != 0 {
		t.Errorf("notifications = %v, active = %q; whoever stopped it knows, and a stopped application has no alerts", kinds, alertKinds(s.engine))
	}
}

func TestAlertsAreKeptWithoutANotifier(t *testing.T) {
	h := newHarness(t)
	h.engine.opts.DiskUsage = func() (api.DiskUsage, bool) { return api.DiskUsage{TotalBytes: 100, UsedBytes: 97}, true }
	h.engine.checkAlerts(context.Background(), time.Now())
	if got := alertKinds(h.engine); got != "disk critical" {
		t.Errorf("active = %q; the server view shows alerts whether or not a webhook is configured", got)
	}
}
