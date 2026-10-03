package deploy

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// What the proxy sees — status codes, latency, how many requests — comes from
// its access log, which Caddy writes to its standard output and the agent
// follows through the Engine API, as it follows the logs of a replica. Every
// request is attributed to an application by its hostname and counted into
// the application's current minute; a minute that has ended is written to the
// database as one row and kept as long as the metric samples are. Nothing but
// counts is stored: the requests themselves, the last few of each application,
// are held in memory only and are gone when the agent restarts.

// latencyBounds are the upper bounds, in milliseconds, of the buckets request
// durations are counted in; one more bucket takes everything slower. They are
// fixed: the rows of a week are added up bucket by bucket, so changing them
// would mix two histograms. Percentiles are read from the histogram and are as
// exact as a bucket is wide.
var latencyBounds = [...]float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

const latencyBuckets = len(latencyBounds) + 1

const (
	// requestsKept is how many of an application's most recent requests are
	// remembered, in memory.
	requestsKept = 200
	// ownersFor is how long the table of who owns which hostname is used
	// before it is read from the database again: how long a hostname that
	// was just deployed goes uncounted.
	ownersFor = 5 * time.Second
	// trafficRetry is the pause before the proxy's log is opened again after
	// it ended: Caddy was restarted, or its container replaced.
	trafficRetry = 2 * time.Second
)

// TrafficWindow resolves the traffic endpoint's `since` to the window it
// covers and the bucket it is served in: the history's windows, with the
// minute a sample covers as the smallest bucket.
func TrafficWindow(since string) (window, step time.Duration, ok bool) {
	window, step, ok = HistoryWindow(since)
	return window, max(step, time.Minute), ok
}

// ErrTrafficUnavailable means there is no access log the agent could read.
var ErrTrafficUnavailable = errors.New("the proxy's access log cannot be read: traffic is recorded when the proxy runs as the caddy service of the agent's compose project")

// trafficCounts are the requests of one application over one stretch of time.
type trafficCounts struct {
	requests  int64
	status2xx int64
	status3xx int64
	status4xx int64
	status5xx int64
	bytes     int64
	latency   [latencyBuckets]int64
}

func (c *trafficCounts) observe(e accessEntry) {
	c.requests++
	switch e.Status / 100 {
	case 2:
		c.status2xx++
	case 3:
		c.status3xx++
	case 4:
		c.status4xx++
	case 5:
		c.status5xx++
	}
	c.bytes += e.Bytes
	ms := float64(e.Duration) / float64(time.Millisecond)
	c.latency[sort.SearchFloat64s(latencyBounds[:], ms)]++
}

func (c *trafficCounts) add(o *trafficCounts) {
	c.requests += o.requests
	c.status2xx += o.status2xx
	c.status3xx += o.status3xx
	c.status4xx += o.status4xx
	c.status5xx += o.status5xx
	c.bytes += o.bytes
	for i, n := range o.latency {
		c.latency[i] += n
	}
}

func countsOf(s store.TrafficSample) *trafficCounts {
	c := &trafficCounts{requests: s.Requests, status2xx: s.Status2xx, status3xx: s.Status3xx,
		status4xx: s.Status4xx, status5xx: s.Status5xx, bytes: s.Bytes}
	copy(c.latency[:], s.Latency)
	return c
}

func (c *trafficCounts) sample(appID int64, at time.Time) store.TrafficSample {
	return store.TrafficSample{ApplicationID: appID, At: at, Requests: c.requests, Status2xx: c.status2xx, Status3xx: c.status3xx,
		Status4xx: c.status4xx, Status5xx: c.status5xx, Bytes: c.bytes, Latency: append([]int64(nil), c.latency[:]...)}
}

func (c *trafficCounts) view() api.TrafficCounts {
	return api.TrafficCounts{
		Requests: c.requests, Status2xx: c.status2xx, Status3xx: c.status3xx, Status4xx: c.status4xx, Status5xx: c.status5xx,
		Bytes: c.bytes,
		P50Ms: c.percentile(0.50), P95Ms: c.percentile(0.95), P99Ms: c.percentile(0.99),
	}
}

// percentile estimates the duration, in milliseconds, that the fraction q of
// the requests stayed under: the bucket the q-th request fell into, and a
// straight line across it. A request in the last bucket is only known to have
// been slower than the last bound, which is what is reported.
func (c *trafficCounts) percentile(q float64) float64 {
	if c.requests == 0 {
		return 0
	}
	var total int64
	for _, n := range c.latency {
		total += n
	}
	rank := q * float64(total)
	var below int64
	for i, n := range c.latency {
		if n == 0 || float64(below+n) < rank {
			below += n
			continue
		}
		if i == len(latencyBounds) {
			return latencyBounds[i-1]
		}
		lower := 0.0
		if i > 0 {
			lower = latencyBounds[i-1]
		}
		ms := lower + (latencyBounds[i]-lower)*(rank-float64(below))/float64(n)
		return math.Round(ms*10) / 10
	}
	return 0
}

// trafficOwner is an application on a hostname, and the path it serves there.
type trafficOwner struct {
	appID int64
	path  string // without a trailing slash; "" is the whole hostname
}

// requestRing holds the last requestsKept requests of one application.
type requestRing struct {
	entries [requestsKept]api.Request
	next    int
	full    bool
}

func (r *requestRing) add(req api.Request) {
	r.entries[r.next] = req
	r.next = (r.next + 1) % requestsKept
	r.full = r.full || r.next == 0
}

// last returns the most recent n requests, oldest first.
func (r *requestRing) last(n int) []api.Request {
	size := r.next
	if r.full {
		size = requestsKept
	}
	n = min(n, size)
	out := make([]api.Request, 0, n)
	for i := size - n; i < size; i++ {
		idx := i
		if r.full {
			idx = (r.next + i) % requestsKept
		}
		out = append(out, r.entries[idx])
	}
	return out
}

// trafficRecorder is the agent's memory of the access log: the minutes that
// have not been written yet and the recent requests, by application.
type trafficRecorder struct {
	mu       sync.Mutex
	owners   map[string][]trafficOwner // by hostname, the longest path first
	ownersAt time.Time
	minutes  map[int64]map[int64]*trafficCounts // application → start of the minute, Unix → counts
	recent   map[int64]*requestRing
	// Entries up to resumeAfter have been counted already: after the log was
	// opened again, Docker repeats the lines of the moment it was cut.
	resumeAfter time.Time
	lastSeen    time.Time
	// unreadable is why the log could not be opened the last time it was
	// tried; nil while it is being read.
	unreadable error
	lastPrune  time.Time

	// flush holds it while minutes move from memory to the database, readers
	// while they add the two up: no minute is counted twice or not at all.
	flushing sync.RWMutex
}

func newTrafficRecorder() *trafficRecorder {
	return &trafficRecorder{
		owners:  map[string][]trafficOwner{},
		minutes: map[int64]map[int64]*trafficCounts{},
		recent:  map[int64]*requestRing{},
	}
}

// ownerOf attributes a request: among the applications on its hostname, the
// one with the longest path the request's path is under. A wildcard is asked
// after the name itself, as the proxy does. Called with t.mu held.
func (t *trafficRecorder) ownerOf(host, path string) (int64, bool) {
	names := []string{host}
	if _, parent, ok := strings.Cut(host, "."); ok {
		names = append(names, "*."+parent)
	}
	for _, name := range names {
		for _, o := range t.owners[name] {
			if o.path == "" || path == o.path || strings.HasPrefix(path, o.path+"/") {
				return o.appID, true
			}
		}
	}
	return 0, false
}

// record counts one request. A request for a hostname no application owns —
// the agent's own, the dashboard's, a name nothing is served at — is dropped.
func (t *trafficRecorder) record(e accessEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !e.Time.After(t.resumeAfter) {
		return
	}
	if e.Time.After(t.lastSeen) {
		t.lastSeen = e.Time
	}
	appID, ok := t.ownerOf(e.Host, e.Path)
	if !ok {
		return
	}
	minutes := t.minutes[appID]
	if minutes == nil {
		minutes = map[int64]*trafficCounts{}
		t.minutes[appID] = minutes
	}
	minute := e.Time.Truncate(time.Minute).Unix()
	counts := minutes[minute]
	if counts == nil {
		counts = &trafficCounts{}
		minutes[minute] = counts
	}
	counts.observe(e)

	ring := t.recent[appID]
	if ring == nil {
		ring = &requestRing{}
		t.recent[appID] = ring
	}
	ring.add(api.Request{
		Time:       e.Time,
		Method:     e.Method,
		Path:       e.Path,
		Status:     e.Status,
		DurationMs: math.Round(float64(e.Duration)/float64(time.Microsecond)) / 1000,
		Bytes:      e.Bytes,
		Client:     e.Client,
	})
}

// recordAccess takes one line of the proxy's access log. Time is a parameter
// so that tests decide when the table of owners is stale.
func (e *Engine) recordAccess(ctx context.Context, line []byte, now time.Time) {
	entry, ok := parseAccessLine(line)
	if !ok {
		return
	}
	t := e.traffic
	t.mu.Lock()
	stale := t.ownersAt.IsZero() || now.Sub(t.ownersAt) >= ownersFor
	t.mu.Unlock()
	if stale {
		e.refreshTrafficOwners(ctx, now)
	}
	t.record(entry)
}

// refreshTrafficOwners reads who owns which hostname from the active
// deployments, and forgets what is remembered of applications that are gone.
func (e *Engine) refreshTrafficOwners(ctx context.Context, now time.Time) {
	t := e.traffic
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		// The old table serves until the next attempt.
		e.log.Warn("traffic: list applications", "error", err)
		t.mu.Lock()
		t.ownersAt = now
		t.mu.Unlock()
		return
	}
	owners := map[string][]trafficOwner{}
	exists := make(map[int64]bool, len(apps))
	for _, app := range apps {
		exists[app.ID] = true
		if app.ActiveDeploymentID == nil {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
		if err != nil {
			continue
		}
		owner := trafficOwner{appID: app.ID, path: strings.TrimRight(d.Spec.Path, "/")}
		for _, host := range hostnamesOf(d.Spec).list() {
			owners[host] = append(owners[host], owner)
		}
	}
	for _, list := range owners {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i].path) > len(list[j].path) })
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.owners, t.ownersAt = owners, now
	for id := range t.minutes {
		if !exists[id] {
			delete(t.minutes, id)
		}
	}
	for id := range t.recent {
		if !exists[id] {
			delete(t.recent, id)
		}
	}
}

// startProxyWatch follows the proxy's access log and writes its minutes down
// until the engine shuts down, and keeps an eye on the certificates the proxy
// presents (see certificates.go). Without a proxy there is nothing to watch.
func (e *Engine) startProxyWatch() {
	if e.opts.Proxy == nil {
		return
	}
	e.bg.Add(3)
	go func() {
		defer e.bg.Done()
		e.followProxy(e.baseCtx)
	}()
	go func() {
		defer e.bg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				// What the current minutes hold is worth a write: the
				// agent is usually back within seconds.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				e.flushTraffic(ctx, time.Now().Add(time.Minute))
				cancel()
				return
			case now := <-ticker.C:
				e.flushTraffic(e.baseCtx, now)
			}
		}
	}()
	go func() {
		defer e.bg.Done()
		ticker := time.NewTicker(certTick)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.checkCertificates(e.baseCtx, now)
			}
		}
	}()
}

// followProxy reads the access log from now on, and opens it again whenever
// it ends: the proxy's container was restarted or replaced.
func (e *Engine) followProxy(ctx context.Context) {
	since := time.Now()
	for ctx.Err() == nil {
		since = e.followProxyOnce(ctx, since)
		select {
		case <-ctx.Done():
		case <-time.After(trafficRetry):
		}
	}
}

// followProxyOnce reads the access log from `since` until it ends, and
// returns where the next reading has to start.
func (e *Engine) followProxyOnce(ctx context.Context, since time.Time) time.Time {
	t := e.traffic
	id, err := e.rt.ProxyContainer(ctx)
	if err == nil {
		t.mu.Lock()
		t.resumeAfter = since
		changed := t.unreadable != nil
		t.unreadable = nil
		t.mu.Unlock()
		if changed {
			e.log.Info("reading the proxy's access log again")
		}
		err = e.rt.FollowOutput(ctx, id, since, func(line []byte) { e.recordAccess(ctx, line, time.Now()) })
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		// Said once, not every two seconds for as long as it lasts.
		if t.unreadable == nil || t.unreadable.Error() != err.Error() {
			e.log.Warn("cannot read the proxy's access log; traffic is not recorded until it can be", "error", err)
		}
		t.unreadable = err
	}
	if t.lastSeen.After(since) {
		return t.lastSeen
	}
	return since
}

// flushTraffic writes the minutes that have ended to the database and, once
// an hour, drops what is older than the retention period. Time is a parameter
// so tests can drive it.
func (e *Engine) flushTraffic(ctx context.Context, now time.Time) {
	t := e.traffic
	// First, so that minutes of an application deleted since are not written
	// against a row that is gone.
	e.refreshTrafficOwners(ctx, now)

	t.flushing.Lock()
	defer t.flushing.Unlock()

	current := now.Truncate(time.Minute).Unix()
	var rows []store.TrafficSample
	ended := map[int64]map[int64]*trafficCounts{}
	t.mu.Lock()
	for appID, minutes := range t.minutes {
		for minute, counts := range minutes {
			if minute >= current {
				continue
			}
			rows = append(rows, counts.sample(appID, time.Unix(minute, 0).UTC()))
			if ended[appID] == nil {
				ended[appID] = map[int64]*trafficCounts{}
			}
			ended[appID][minute] = counts
			delete(minutes, minute)
		}
	}
	t.mu.Unlock()
	if len(rows) > 0 {
		if err := e.store.AddTrafficSamples(ctx, rows); err != nil {
			// Back into memory, and tried again in a minute. A request logged
			// late into one of these minutes in the meantime is kept too.
			e.log.Warn("traffic: could not store samples", "error", err)
			t.mu.Lock()
			for appID, minutes := range ended {
				if t.minutes[appID] == nil {
					t.minutes[appID] = map[int64]*trafficCounts{}
				}
				for minute, counts := range minutes {
					if late := t.minutes[appID][minute]; late != nil {
						counts.add(late)
					}
					t.minutes[appID][minute] = counts
				}
			}
			t.mu.Unlock()
			return
		}
	}

	t.mu.Lock()
	due := now.Sub(t.lastPrune) >= pruneEvery
	if due {
		t.lastPrune = now
	}
	t.mu.Unlock()
	if due {
		if _, err := e.store.PruneTrafficSamples(ctx, now.Add(-e.opts.MetricsRetention)); err != nil {
			e.log.Warn("traffic: could not prune samples", "error", err)
		}
	}
}

// trafficReadable reports whether there is an access log to read at all. A
// proxy that is restarting is one; no proxy, or one outside the agent's
// compose project, is not.
func (e *Engine) trafficReadable() bool {
	if e.opts.Proxy == nil {
		return false
	}
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	return !errors.Is(e.traffic.unreadable, docker.ErrNoProxyContainer)
}

// Traffic returns what the proxy saw of the application over the last
// `window`, in buckets of `step`: the stored minutes and the ones still in
// memory.
func (e *Engine) Traffic(ctx context.Context, name string, window, step time.Duration) (api.Traffic, error) {
	return e.trafficAt(ctx, name, window, step, time.Now())
}

func (e *Engine) trafficAt(ctx context.Context, name string, window, step time.Duration, now time.Time) (api.Traffic, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.Traffic{}, err
	}
	if !e.trafficReadable() {
		return api.Traffic{}, ErrTrafficUnavailable
	}
	// On a bucket boundary, so that the first bucket is a whole one.
	since := now.Add(-window).UTC().Truncate(step)
	secs := int64(step / time.Second)

	t := e.traffic
	t.flushing.RLock()
	rows, err := e.store.TrafficSamples(ctx, app.ID, since)
	if err != nil {
		t.flushing.RUnlock()
		return api.Traffic{}, err
	}
	buckets := map[int64]*trafficCounts{}
	into := func(minute int64) *trafficCounts {
		start := minute / secs * secs
		if buckets[start] == nil {
			buckets[start] = &trafficCounts{}
		}
		return buckets[start]
	}
	for _, row := range rows {
		into(row.At.Unix()).add(countsOf(row))
	}
	t.mu.Lock()
	for minute, counts := range t.minutes[app.ID] {
		if minute >= since.Unix() {
			into(minute).add(counts)
		}
	}
	t.mu.Unlock()
	t.flushing.RUnlock()

	starts := make([]int64, 0, len(buckets))
	for start := range buckets {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	out := api.Traffic{Application: name, Since: since, StepSeconds: int(secs), Points: make([]api.TrafficPoint, 0, len(starts))}
	var totals trafficCounts
	for _, start := range starts {
		totals.add(buckets[start])
		out.Points = append(out.Points, api.TrafficPoint{T: time.Unix(start, 0).UTC(), TrafficCounts: buckets[start].view()})
	}
	out.Totals = totals.view()
	return out, nil
}

// Requests returns the application's most recent requests, oldest first, at
// most `tail` of the requestsKept the agent remembers.
func (e *Engine) Requests(ctx context.Context, name string, tail int) ([]api.Request, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	if !e.trafficReadable() {
		return nil, ErrTrafficUnavailable
	}
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	ring := e.traffic.recent[app.ID]
	if ring == nil {
		return []api.Request{}, nil
	}
	return ring.last(tail), nil
}

// forget drops what is remembered of an application that was deleted. Its
// stored samples go with its row; its id may be given to the next
// application created, which must not start out with a stranger's requests.
func (t *trafficRecorder) forget(appID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.minutes, appID)
	delete(t.recent, appID)
	t.ownersAt = time.Time{}
}
