package deploy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/pkg/api"
)

// accessLine is a line as Caddy writes it with the logging configuration of
// proxy.Build; testdata/caddy_access.log holds real ones.
func accessLine(at time.Time, host, method, path string, status int, d time.Duration, size int) string {
	return fmt.Sprintf(`{"level":"info","ts":%d.%06d,"logger":"http.log.access.shipwick","msg":"handled request","request":{"client_ip":"203.0.113.7","method":%q,"host":%q,"uri":%q},"duration":%g,"size":%d,"status":%d}`,
		at.Unix(), at.Nanosecond()/1000, method, host, path, d.Seconds(), size, status)
}

// trafficked is a routed harness with one application deployed on a domain,
// whose access log lines are fed in by hand on a synthetic clock.
type trafficked struct {
	*supervised
	// minute is the start of a minute near the wall clock: the traffic
	// endpoint measures its window from the time it is asked.
	minute time.Time
}

func newTrafficked(t *testing.T) *trafficked {
	s, _ := newRouted(t)
	tr := &trafficked{supervised: s, minute: time.Now().UTC().Truncate(time.Hour).Add(-30 * time.Minute)}
	if d := s.deploy(web("web:1.0", 1)); d.Status != api.StatusActive {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}
	return tr
}

// hit feeds one request for web.example.com, `after` into the minute.
func (tr *trafficked) hit(after time.Duration, path string, status int, d time.Duration) {
	at := tr.minute.Add(after)
	tr.engine.recordAccess(context.Background(), []byte(accessLine(at, "web.example.com", "GET", path, status, d, 100)), at)
}

func (tr *trafficked) traffic(t *testing.T, name string, now time.Time) api.Traffic {
	t.Helper()
	got, err := tr.engine.trafficAt(context.Background(), name, time.Hour, time.Minute, now)
	if err != nil {
		t.Fatalf("traffic: %v", err)
	}
	return got
}

func (tr *trafficked) stored(t *testing.T, name string) int {
	t.Helper()
	app, err := tr.store.GetApplication(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tr.store.TrafficSamples(context.Background(), app.ID, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestAccessLogLinesOfARealCaddyAreParsed(t *testing.T) {
	f, err := os.Open("testdata/caddy_access.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var entries []accessEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		e, ok := parseAccessLine(sc.Bytes())
		if !ok {
			t.Fatalf("not parsed: %s", sc.Text())
		}
		entries = append(entries, e)
	}
	if len(entries) != 8 {
		t.Fatalf("parsed %d lines, want 8", len(entries))
	}

	// GET https://x.localhost:27543/api/?token=secret&page=2
	e := entries[2]
	if e.Host != "x.localhost" || e.Method != "GET" || e.Path != "/api/" || e.Status != 200 || e.Bytes != 4 || e.Client != "172.21.0.1" {
		t.Errorf("entry = %+v; the port is not part of the hostname and the query string is not kept", e)
	}
	if want := time.Date(2026, 10, 3, 12, 47, 37, 24695000, time.UTC); !e.Time.Equal(want) {
		t.Errorf("time = %s, want %s", e.Time.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
	if e.Duration < 890*time.Microsecond || e.Duration > 900*time.Microsecond {
		t.Errorf("duration = %s, want the 0.000894897s the proxy measured", e.Duration)
	}
	// Caddy logs a 5xx at level error; it is an access log entry all the same.
	if e := entries[7-1]; e.Status != 503 || e.Host != "q.localhost" || e.Duration < 3*time.Second {
		t.Errorf("entry = %+v, want the 503 that took four seconds", e)
	}
	if e := entries[3]; e.Method != "POST" || e.Path != "/missing" || e.Status != 501 {
		t.Errorf("entry = %+v", e)
	}
	for _, e := range entries {
		if strings.ContainsAny(e.Path, "?") {
			t.Errorf("path %q carries a query string", e.Path)
		}
	}
}

func TestLinesThatAreNotAccessLogEntriesAreIgnored(t *testing.T) {
	for _, line := range []string{
		``,
		`not json`,
		`{"level":"info","ts":1791030598.16295,"logger":"tls.cache.maintenance","msg":"started background certificate maintenance"}`,
		`{"level":"info","ts":1791030598.1633835,"msg":"serving initial configuration"}`,
	} {
		if e, ok := parseAccessLine([]byte(line)); ok {
			t.Errorf("%q parsed as %+v", line, e)
		}
	}
	// A configuration loaded by an older agent logs the query string; it is
	// cut off here as well.
	e, ok := parseAccessLine([]byte(accessLine(time.Now(), "web.example.com", "GET", "/reset?token=abc", 200, time.Millisecond, 1)))
	if !ok || e.Path != "/reset" {
		t.Errorf("entry = %+v ok=%v, want the path without its query", e, ok)
	}
}

func TestRequestsAreCountedPerApplicationAndMinute(t *testing.T) {
	tr := newTrafficked(t)
	tr.hit(1*time.Second, "/", 200, 3*time.Millisecond)
	tr.hit(2*time.Second, "/moved", 301, 3*time.Millisecond)
	tr.hit(3*time.Second, "/nope", 404, 3*time.Millisecond)
	tr.hit(4*time.Second, "/boom", 502, 700*time.Millisecond)
	tr.hit(70*time.Second, "/", 200, 3*time.Millisecond)

	got := tr.traffic(t, "web", tr.minute.Add(2*time.Minute))
	want := api.TrafficCounts{Requests: 5, Status2xx: 2, Status3xx: 1, Status4xx: 1, Status5xx: 1, Bytes: 500}
	total := got.Totals
	total.P50Ms, total.P95Ms, total.P99Ms = 0, 0, 0
	if total != want {
		t.Errorf("totals = %+v, want %+v", total, want)
	}
	if got.StepSeconds != 60 || len(got.Points) != 2 {
		t.Fatalf("step = %d, points = %+v; want two one-minute buckets", got.StepSeconds, got.Points)
	}
	if p := got.Points[0]; !p.T.Equal(tr.minute) || p.Requests != 4 || p.Status5xx != 1 {
		t.Errorf("first point = %+v, want the four requests of %s", p, tr.minute)
	}
	if p := got.Points[1]; !p.T.Equal(tr.minute.Add(time.Minute)) || p.Requests != 1 {
		t.Errorf("second point = %+v", p)
	}
}

func TestAMinuteIsWrittenOnceItHasEndedAndCountedOnce(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	tr.hit(10*time.Second, "/", 200, time.Millisecond)
	tr.hit(20*time.Second, "/", 200, time.Millisecond)

	// Still the same minute: nothing to write yet, but it is reported.
	tr.engine.flushTraffic(ctx, tr.minute.Add(50*time.Second))
	if n := tr.stored(t, "web"); n != 0 {
		t.Fatalf("%d rows after a flush within the minute; the current minute stays in memory", n)
	}
	if got := tr.traffic(t, "web", tr.minute.Add(50*time.Second)); got.Totals.Requests != 2 {
		t.Fatalf("requests = %d, want the 2 of the current minute", got.Totals.Requests)
	}

	tr.hit(65*time.Second, "/", 500, time.Millisecond)
	tr.engine.flushTraffic(ctx, tr.minute.Add(70*time.Second))
	if n := tr.stored(t, "web"); n != 1 {
		t.Fatalf("%d rows after the minute ended, want 1", n)
	}
	// The same flush again writes nothing twice.
	tr.engine.flushTraffic(ctx, tr.minute.Add(80*time.Second))
	if n := tr.stored(t, "web"); n != 1 {
		t.Fatalf("%d rows after a second flush, want 1", n)
	}
	got := tr.traffic(t, "web", tr.minute.Add(80*time.Second))
	if got.Totals.Requests != 3 || got.Totals.Status5xx != 1 || len(got.Points) != 2 {
		t.Errorf("traffic = %+v; want the stored minute and the current one, each once", got)
	}
}

func TestTrafficSamplesAreKeptForTheRetentionPeriod(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	tr.hit(0, "/", 200, time.Millisecond)
	tr.engine.flushTraffic(ctx, tr.minute.Add(time.Minute))
	if n := tr.stored(t, "web"); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}

	// Six days on, it is still there; the hourly prune has run.
	tr.engine.flushTraffic(ctx, tr.minute.Add(6*24*time.Hour))
	if n := tr.stored(t, "web"); n != 1 {
		t.Fatalf("%d rows after six days, want the sample kept", n)
	}
	// A prune is due once an hour, not on every flush.
	tr.engine.flushTraffic(ctx, tr.minute.Add(7*24*time.Hour+30*time.Minute))
	tr.engine.flushTraffic(ctx, tr.minute.Add(7*24*time.Hour+2*time.Hour))
	if n := tr.stored(t, "web"); n != 0 {
		t.Fatalf("%d rows after more than seven days, want none", n)
	}
}

func TestPercentilesComeFromTheHistogram(t *testing.T) {
	tr := newTrafficked(t)
	// 90 fast requests, 9 slower, 1 very slow.
	for i := 0; i < 90; i++ {
		tr.hit(time.Duration(i)*100*time.Millisecond, "/", 200, 4*time.Millisecond)
	}
	for i := 0; i < 9; i++ {
		tr.hit(10*time.Second+time.Duration(i)*time.Millisecond, "/", 200, 200*time.Millisecond)
	}
	tr.hit(20*time.Second, "/", 200, 4*time.Second)

	got := tr.traffic(t, "web", tr.minute.Add(30*time.Second)).Totals
	// Each is in the bucket its request fell into: 2.5–5ms, 100–250ms, 2.5–5s.
	if got.P50Ms < 2.5 || got.P50Ms > 5 {
		t.Errorf("p50 = %v ms, want within the 2.5–5 ms bucket", got.P50Ms)
	}
	if got.P95Ms < 100 || got.P95Ms > 250 {
		t.Errorf("p95 = %v ms, want within the 100–250 ms bucket", got.P95Ms)
	}
	if got.P99Ms < 100 || got.P99Ms > 250 {
		t.Errorf("p99 = %v ms, want within the 100–250 ms bucket: 99 of 100 requests were done by then", got.P99Ms)
	}

	var slow trafficCounts
	slow.observe(accessEntry{Status: 200, Duration: 2 * time.Minute})
	if p := slow.percentile(0.5); p != 30000 {
		t.Errorf("p50 of one two-minute request = %v ms; beyond the last bound only the bound is known", p)
	}
	if p := (&trafficCounts{}).percentile(0.95); p != 0 {
		t.Errorf("p95 of nothing = %v", p)
	}
}

func TestRequestsGoToTheApplicationWithTheLongestMatchingPath(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	// A second application on the same hostname, under /api. Stored as a
	// deployment would store it: routing of paths is not this test's subject.
	apiApp := app("api", "api:1.0", 1)
	apiApp.Domain, apiApp.Path = "web.example.com", "/api"
	d, err := tr.store.CreateDeploymentFrom(ctx, apiApp, api.KindDeploy, nil, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.store.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusHealthy, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.store.ActivateDeployment(ctx, d.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	tr.hit(1*time.Second, "/", 200, time.Millisecond)
	tr.hit(2*time.Second, "/api", 200, time.Millisecond)
	tr.hit(3*time.Second, "/api/users/7", 200, time.Millisecond)
	tr.hit(4*time.Second, "/apiary", 200, time.Millisecond) // not under /api

	now := tr.minute.Add(10 * time.Second)
	if got := tr.traffic(t, "api", now).Totals.Requests; got != 2 {
		t.Errorf("api got %d requests, want /api and /api/users/7", got)
	}
	if got := tr.traffic(t, "web", now).Totals.Requests; got != 2 {
		t.Errorf("web got %d requests, want / and /apiary", got)
	}
}

func TestAWildcardGetsTheNamesNoOtherApplicationServes(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	wild := app("wild", "wild:1.0", 1)
	wild.Domain = "*.example.com"
	d, err := tr.store.CreateDeploymentFrom(ctx, wild, api.KindDeploy, nil, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.store.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusHealthy, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.store.ActivateDeployment(ctx, d.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	tr.hit(1*time.Second, "/", 200, time.Millisecond)
	for i, host := range []string{"shop.example.com", "blog.example.com", "a.b.example.com", "example.com"} {
		at := tr.minute.Add(time.Duration(i+2) * time.Second)
		tr.engine.recordAccess(ctx, []byte(accessLine(at, host, "GET", "/", 200, time.Millisecond, 100)), at)
	}

	now := tr.minute.Add(10 * time.Second)
	if got := tr.traffic(t, "wild", now).Totals.Requests; got != 2 {
		t.Errorf("wild got %d requests, want those for shop and blog", got)
	}
	if got := tr.traffic(t, "web", now).Totals.Requests; got != 1 {
		t.Errorf("web got %d requests, want its own", got)
	}
}

func TestAliasesAndRedirectsCountAndOtherHostnamesDoNot(t *testing.T) {
	s, _ := newRouted(t)
	s.engine.opts.ExtraRoutes = []proxy.Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true}}
	a := web("web:1.0", 1)
	a.Aliases, a.Redirects = []string{"web2.example.com"}, []string{"www.example.com"}
	s.deploy(a)

	now := time.Now().UTC().Truncate(time.Minute)
	for _, host := range []string{"web.example.com", "WEB2.example.com:443", "www.example.com", "agent.example.com", "elsewhere.example.com"} {
		s.engine.recordAccess(context.Background(), []byte(accessLine(now, host, "GET", "/", 200, time.Millisecond, 1)), now)
	}
	got, err := s.engine.trafficAt(context.Background(), "web", time.Hour, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Requests != 3 {
		t.Errorf("requests = %d, want the domain's, the alias's and the redirect's; not the agent's own or a stranger's", got.Totals.Requests)
	}
}

func TestTheLast200RequestsAreKept(t *testing.T) {
	tr := newTrafficked(t)
	for i := 0; i < 250; i++ {
		tr.hit(time.Duration(i)*time.Millisecond, fmt.Sprintf("/%d", i), 200, 1500*time.Microsecond)
	}
	all, err := tr.engine.Requests(context.Background(), "web", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 200 || all[0].Path != "/50" || all[199].Path != "/249" {
		t.Fatalf("kept %d requests, %s … %s; want the last 200, oldest first", len(all), all[0].Path, all[len(all)-1].Path)
	}
	tail, _ := tr.engine.Requests(context.Background(), "web", 3)
	if len(tail) != 3 || tail[0].Path != "/247" || tail[2].Path != "/249" {
		t.Errorf("tail = %+v, want the three most recent, oldest first", tail)
	}
	want := api.Request{Time: tr.minute.Add(249 * time.Millisecond), Method: "GET", Path: "/249", Status: 200, DurationMs: 1.5, Bytes: 100, Client: "203.0.113.7"}
	if tail[2] != want {
		t.Errorf("request = %+v, want %+v", tail[2], want)
	}

	// An application nobody has asked anything of has an empty list, not an error.
	tr.deploy(app("quiet", "quiet:1.0", 1))
	if got, err := tr.engine.Requests(context.Background(), "quiet", 50); err != nil || got == nil || len(got) != 0 {
		t.Errorf("requests of a quiet application = %v, %v; want an empty list", got, err)
	}
}

func TestTheProxyLogIsFollowedAndResumedWhereItEnded(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Minute)
	line := func(after time.Duration, path string) string {
		return accessLine(start.Add(after), "web.example.com", "GET", path, 200, time.Millisecond, 1)
	}

	// Caddy stops after three lines: the stream ends, and the next reading
	// starts at the last line seen.
	tr.rt.WriteOutput(dockertest.ProxyID, line(-time.Hour, "/before-the-agent-started"), line(time.Second, "/a"), line(2*time.Second, "/b"))
	tr.rt.EndOutput(dockertest.ProxyID)
	next := tr.engine.followProxyOnce(ctx, start)
	if want := start.Add(2 * time.Second); !next.Equal(want) {
		t.Fatalf("resume at %s, want the time of the last line, %s", next, want)
	}

	// Docker repeats the line of that moment; it is not counted twice.
	tr.rt.WriteOutput(dockertest.ProxyID, line(2*time.Second, "/b"), line(3*time.Second, "/c"))
	tr.rt.EndOutput(dockertest.ProxyID)
	tr.engine.followProxyOnce(ctx, next)

	if since := tr.rt.Follows(); len(since) != 2 || !since[0].Equal(start) || !since[1].Equal(next) {
		t.Errorf("followed since %v, want %s and then %s", since, start, next)
	}
	got, _ := tr.engine.Requests(ctx, "web", 50)
	var paths []string
	for _, r := range got {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, " ") != "/a /b /c" {
		t.Errorf("requests = %v, want /a /b /c: nothing from before the agent started, nothing twice", paths)
	}
}

func TestTrafficIsUnavailableWithoutAProxyContainerToRead(t *testing.T) {
	ctx := context.Background()

	// No proxy at all.
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	if _, err := h.engine.Traffic(ctx, "my-api", time.Hour, time.Minute); !errors.Is(err, ErrTrafficUnavailable) {
		t.Errorf("without a proxy: err = %v, want ErrTrafficUnavailable", err)
	}
	if _, err := h.engine.Requests(ctx, "my-api", 10); !errors.Is(err, ErrTrafficUnavailable) {
		t.Errorf("without a proxy: err = %v, want ErrTrafficUnavailable", err)
	}

	// A proxy that is not a container of this daemon.
	tr := newTrafficked(t)
	tr.rt.ProxyErr = docker.ErrNoProxyContainer
	tr.engine.followProxyOnce(ctx, time.Now())
	if _, err := tr.engine.Traffic(ctx, "web", time.Hour, time.Minute); !errors.Is(err, ErrTrafficUnavailable) {
		t.Errorf("proxy outside Docker: err = %v, want ErrTrafficUnavailable", err)
	}

	// A proxy that is restarting is one that will be read again: what is
	// known is served.
	tr.rt.ProxyErr = errors.New("the proxy container is not running")
	tr.engine.followProxyOnce(ctx, time.Now())
	if _, err := tr.engine.Traffic(ctx, "web", time.Hour, time.Minute); err != nil {
		t.Errorf("proxy restarting: err = %v, want the traffic known so far", err)
	}
}

func TestAStaticApplicationHasTrafficToo(t *testing.T) {
	s, _ := newStaticHarness(t)
	ctx := context.Background()
	if d := s.deployStatic(site("site"), map[string]string{"index.html": "<h1>hi</h1>"}); d.Status != api.StatusActive {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}
	at := time.Now().UTC().Truncate(time.Minute)
	s.engine.recordAccess(ctx, []byte(accessLine(at, "site.example.com", "GET", "/index.html", 200, 200*time.Microsecond, 512)), at)
	got, err := s.engine.trafficAt(ctx, "site", time.Hour, time.Minute, at.Add(time.Second))
	if err != nil {
		t.Fatalf("traffic: %v", err)
	}
	if got.Totals.Requests != 1 || got.Totals.Bytes != 512 {
		t.Errorf("totals = %+v, want the one request the proxy answered from the folder", got.Totals)
	}
}

func TestTrafficOfADeletedApplicationIsForgotten(t *testing.T) {
	tr := newTrafficked(t)
	ctx := context.Background()
	tr.hit(time.Second, "/", 200, time.Millisecond)
	if err := tr.engine.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// The minute ends after the application is gone; writing it must not
	// fail the minutes of the others.
	tr.deploy(app("other", "other:1.0", 1))
	tr.engine.flushTraffic(ctx, tr.minute.Add(2*time.Minute))
	tr.engine.traffic.mu.Lock()
	left := len(tr.engine.traffic.minutes) + len(tr.engine.traffic.recent)
	tr.engine.traffic.mu.Unlock()
	if left != 0 {
		t.Errorf("%d applications still remembered after the only one with traffic was deleted", left)
	}
}
