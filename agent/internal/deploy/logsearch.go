package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// Reading the archive, and searching it together with what the application's
// containers still hold.
//
// A search has no index to consult. It reads: the files of the entries that
// the question does not rule out by application, deployment, replica, run or
// time — which the database answers — and the logs of the replicas that
// exist, from where their last copy ended. Reading is what was measured to
// be cheap enough: about 180 MiB of lines a second through gzip, against an
// FTS5 trigram index that answers in milliseconds, takes four and a half
// times the size of the lines it indexes and 80 µs of the database's one
// connection for every line added (docs/architecture.md has the figures).
// What keeps a request short is that it is bounded: it stops after
// searchBudget bytes or when it has the lines it was asked for, and says
// where the next request goes on.

// searchBudget is how much output one request reads before it answers with
// what it has. A source that was begun is finished, so a request reads at
// most this plus one source. A variable for the tests, which have no 128 MiB
// to search through.
var searchBudget int64 = 128 << 20

const (
	// liveSearchLines is how far back a search looks into the log of a
	// container that exists: its last lines, like everything else read from
	// Docker.
	liveSearchLines = 50000
)

// ErrInvalidCursor means a search was asked to continue from somewhere no
// search ended.
var ErrInvalidCursor = errors.New("cursor is not one a search returned")

// LogArchiveQuery narrows a listing of the archive. Zero values do not
// narrow.
type LogArchiveQuery struct {
	Kind         string
	DeploymentID int64
	Replica      int
	RunID        int64
	Before       int64
	Limit        int
}

// LogArchive lists what is kept of the application's ended containers,
// newest first.
func (e *Engine) LogArchive(ctx context.Context, name string, q LogArchiveQuery) ([]api.LogArchiveEntry, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return nil, err
	}
	entries, err := e.store.ListLogArchives(ctx, store.LogArchiveFilter{
		ApplicationID: app.ID, Kind: q.Kind, DeploymentID: q.DeploymentID, Replica: q.Replica, RunID: q.RunID, Before: q.Before, Limit: q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.LogArchiveEntry, 0, len(entries))
	for _, a := range entries {
		out = append(out, logArchiveView(app.Name, a))
	}
	return out, nil
}

// ArchivedLogs returns one entry of the application with its lines, oldest
// first; tail keeps the last that many, zero all of them.
func (e *Engine) ArchivedLogs(ctx context.Context, name string, id int64, tail int) (api.LogArchiveDetail, error) {
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return api.LogArchiveDetail{}, err
	}
	a, err := e.store.GetLogArchive(ctx, id)
	if err != nil {
		return api.LogArchiveDetail{}, err
	}
	if a.ApplicationID != app.ID {
		return api.LogArchiveDetail{}, store.ErrNotFound
	}
	detail := api.LogArchiveDetail{LogArchiveEntry: logArchiveView(app.Name, a), Output: []api.LogLine{}}
	if a.StoredBytes == 0 {
		return detail, nil
	}
	path := logFilePath(e.opts.LogArchive.Dir, store.LogArchiveFile{ID: a.ID, ApplicationID: a.ApplicationID})
	err = readLogFile(path, func(at time.Time, stream string, message []byte) {
		detail.Output = append(detail.Output, api.LogLine{Replica: a.Replica, Container: a.ContainerName, Stream: stream, Time: at, Message: string(message)})
	})
	if errors.Is(err, os.ErrNotExist) {
		return api.LogArchiveDetail{}, store.ErrNotFound
	} else if err != nil {
		return api.LogArchiveDetail{}, fmt.Errorf("read archived logs: %w", err)
	}
	if tail > 0 && len(detail.Output) > tail {
		detail.Output = detail.Output[len(detail.Output)-tail:]
	}
	return detail, nil
}

func logArchiveView(application string, a store.LogArchive) api.LogArchiveEntry {
	return api.LogArchiveEntry{
		ID:           a.ID,
		Application:  application,
		Kind:         a.Kind,
		DeploymentID: a.DeploymentID,
		Deployment:   a.Sequence,
		Version:      a.Version,
		Replica:      a.Replica,
		Job:          a.Job,
		RunID:        a.RunID,
		Container:    a.ContainerName,
		Reason:       a.Reason,
		ExitCode:     a.ExitCode,
		OOMKilled:    a.OOMKilled,
		EndedAt:      a.EndedAt,
		FirstLineAt:  a.FirstAt,
		LastLineAt:   a.LastAt,
		Lines:        a.Lines,
		Bytes:        a.Bytes,
		StoredBytes:  a.StoredBytes,
		Truncated:    a.Truncated,
	}
}

// LogSearch is a question to SearchLogs. Zero values do not narrow.
type LogSearch struct {
	// Text is looked for in every line, whatever its case; empty matches
	// every line.
	Text         string
	Since, Until time.Time
	DeploymentID int64
	Replica      int
	RunID        int64
	// Limit is how many lines one answer holds at most.
	Limit int
	// Cursor is the Next of the answer this one continues.
	Cursor string
}

// searchCursor is where a search goes on. It reads the replicas that exist
// first, from the highest index down, and then the archive from its newest
// entry to its oldest: read backwards, an answer begins with the oldest entry
// and ends with replica 1.
type searchCursor struct {
	// live: still among the replicas, at the one with this index (zero: at
	// the first of them), and of it at the lines before this time (zero: all
	// of them).
	live    bool
	replica int
	before  time.Time
	// Otherwise among the entries with at most this id, and of that very
	// entry at the lines before this one (negative: all of them).
	entry int64
	line  int
}

func parseSearchCursor(s string) (searchCursor, error) {
	if s == "" {
		return searchCursor{live: true}, nil
	}
	first, second, two := strings.Cut(s[1:], ".")
	a, err := strconv.ParseInt(first, 10, 64)
	if err != nil || a < 0 {
		return searchCursor{}, ErrInvalidCursor
	}
	b := int64(-1)
	if two {
		if b, err = strconv.ParseInt(second, 10, 64); err != nil || b < 0 {
			return searchCursor{}, ErrInvalidCursor
		}
	}
	switch s[0] {
	case 'l':
		// A replica's index: nothing a cursor of ours holds comes near the bound.
		if a > math.MaxInt32 {
			return searchCursor{}, ErrInvalidCursor
		}
		c := searchCursor{live: true, replica: int(a)}
		if two {
			c.before = time.Unix(0, b).UTC()
		}
		return c, nil
	case 'a':
		if b > math.MaxInt32 {
			return searchCursor{}, ErrInvalidCursor
		}
		return searchCursor{entry: a, line: int(b)}, nil
	}
	return searchCursor{}, ErrInvalidCursor
}

func (c searchCursor) String() string {
	switch {
	case c.live && c.before.IsZero():
		return "l" + strconv.Itoa(c.replica)
	case c.live:
		return "l" + strconv.Itoa(c.replica) + "." + strconv.FormatInt(c.before.UnixNano(), 10)
	case c.line < 0:
		return "a" + strconv.FormatInt(c.entry, 10)
	}
	return "a" + strconv.FormatInt(c.entry, 10) + "." + strconv.Itoa(c.line)
}

// SearchLogs answers one page of a search through the application's output:
// what its replicas still hold, and what the archive keeps.
func (e *Engine) SearchLogs(ctx context.Context, name string, q LogSearch) (api.LogSearchResult, error) {
	result := api.LogSearchResult{Lines: []api.LogMatch{}}
	cur, err := parseSearchCursor(q.Cursor)
	if err != nil {
		return result, err
	}
	app, err := e.store.GetApplication(ctx, name)
	if err != nil {
		return result, err
	}
	if q.DeploymentID != 0 {
		if d, err := e.store.GetDeployment(ctx, q.DeploymentID); err != nil {
			return result, err
		} else if d.ApplicationID != app.ID {
			return result, store.ErrNotFound
		}
	}
	needle := bytes.ToLower([]byte(q.Text))
	full := func() bool { return len(result.Lines) >= q.Limit || result.Bytes >= searchBudget }

	if cur.live {
		replicas, d, err := e.liveLogSources(ctx, app, q)
		if err != nil {
			return result, err
		}
		for _, r := range replicas {
			if cur.replica != 0 && r.Index > cur.replica {
				continue
			}
			before := time.Time{}
			if r.Index == cur.replica {
				before = cur.before
			}
			if full() {
				result.Next = searchCursor{live: true, replica: r.Index, before: before}.String()
				return result, nil
			}
			found, more, err := e.searchContainer(ctx, r, q, needle, before, q.Limit-len(result.Lines), &result)
			if err != nil {
				return result, err
			}
			for _, line := range found {
				result.Lines = append(result.Lines, api.LogMatch{LogLine: line, DeploymentID: &d.ID, Deployment: d.Sequence})
			}
			if more {
				result.Next = searchCursor{live: true, replica: r.Index, before: found[len(found)-1].Time}.String()
				return result, nil
			}
		}
		cur = searchCursor{entry: math.MaxInt64, line: -1}
	}

	if e.opts.LogArchive.Dir == "" {
		return result, nil
	}
	before := cur.entry
	if before < math.MaxInt64 {
		before++
	}
	entries, err := e.store.ListLogArchives(ctx, store.LogArchiveFilter{
		ApplicationID: app.ID, DeploymentID: q.DeploymentID, Replica: q.Replica, RunID: q.RunID,
		Since: q.Since, Until: q.Until, Before: before,
	})
	if err != nil {
		return result, err
	}
	for _, a := range entries {
		if a.StoredBytes == 0 {
			continue
		}
		upTo := -1
		if a.ID == cur.entry {
			upTo = cur.line
		}
		if full() {
			result.Next = searchCursor{entry: a.ID, line: upTo}.String()
			return result, nil
		}
		found, next, err := e.searchEntry(a, q, needle, upTo, q.Limit-len(result.Lines), &result)
		if errors.Is(err, os.ErrNotExist) {
			continue // removed by the retention while this search ran
		} else if err != nil {
			return result, fmt.Errorf("read archived logs: %w", err)
		}
		for _, line := range found {
			id := a.ID
			result.Lines = append(result.Lines, api.LogMatch{LogLine: line, ArchiveID: &id, DeploymentID: a.DeploymentID, Deployment: a.Sequence, Job: a.Job, RunID: a.RunID})
		}
		if next >= 0 {
			result.Next = searchCursor{entry: a.ID, line: next}.String()
			return result, nil
		}
	}
	return result, nil
}

// liveLogSources are the containers whose own log a search reads: the
// replicas of the active deployment, running or not, unless the question
// rules them out. The containers of jobs are not among them: a run's output
// is in the archive when the run has ended.
func (e *Engine) liveLogSources(ctx context.Context, app store.Application, q LogSearch) ([]store.Replica, store.Deployment, error) {
	if _, ok := e.rt.(logReader); !ok || app.ActiveDeploymentID == nil || q.RunID != 0 {
		return nil, store.Deployment{}, nil
	}
	if q.DeploymentID != 0 && q.DeploymentID != *app.ActiveDeploymentID {
		return nil, store.Deployment{}, nil
	}
	d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if err != nil {
		return nil, store.Deployment{}, err
	}
	if d.StaticDigest != "" {
		return nil, d, nil
	}
	replicas, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return nil, d, err
	}
	var out []store.Replica
	for _, r := range replicas {
		if q.Replica == 0 || q.Replica == r.Index {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index > out[j].Index })
	return out, d, nil
}

// lastMatches keeps the newest matching lines of a source that is read
// oldest first: a ring, so that a source in which everything matches costs
// no more than one in which nothing does.
type lastMatches struct {
	keep  int
	lines []api.LogLine
	at    []int // each kept line's place in its source
	next  int   // where the next line goes once the ring is full
	more  bool  // older matches were dropped
}

func (m *lastMatches) add(line api.LogLine, at int) {
	if len(m.lines) < m.keep {
		m.lines, m.at = append(m.lines, line), append(m.at, at)
		return
	}
	m.lines[m.next], m.at[m.next] = line, at
	m.next = (m.next + 1) % m.keep
	m.more = true
}

// newestFirst returns the kept lines in the order a search answers in, and
// the place of the oldest of them.
func (m *lastMatches) newestFirst() (lines []api.LogLine, oldestAt int) {
	n := len(m.lines)
	if n == 0 {
		return nil, -1
	}
	lines = make([]api.LogLine, 0, n)
	for i := 1; i <= n; i++ {
		lines = append(lines, m.lines[(m.next-i+2*n)%n])
	}
	return lines, m.at[m.next%n]
}

// searchContainer reads a replica's own log, from where its last archived
// run ended, and returns its matching lines before the given time, newest
// first and at most keep; more says that older ones match as well.
func (e *Engine) searchContainer(ctx context.Context, r store.Replica, q LogSearch, needle []byte, before time.Time, keep int, result *api.LogSearchResult) (found []api.LogLine, more bool, err error) {
	through, err := e.store.LogArchivedThrough(ctx, r.ContainerID)
	if err != nil {
		return nil, false, err
	}
	window := docker.LogWindow{Since: q.Since, Until: q.Until, Tail: liveSearchLines}
	if !through.IsZero() && !window.Since.After(through) {
		window.Since = through.Add(time.Nanosecond)
	}
	if !before.IsZero() && (window.Until.IsZero() || !window.Until.Before(before)) {
		window.Until = before.Add(-time.Nanosecond)
	}
	matches := lastMatches{keep: keep}
	err = e.rt.(logReader).ReadLogs(ctx, r.ContainerID, window, func(entry docker.LogEntry) {
		result.Bytes += int64(len(entry.Message))
		if containsFold([]byte(entry.Message), needle) {
			matches.add(logLineView(r, entry), 0)
		}
	})
	if errors.Is(err, docker.ErrNotFound) {
		return nil, false, nil // removed a moment ago: its output is in the archive, or on its way there
	} else if err != nil {
		// A logging driver that keeps nothing to read has nothing to search.
		e.log.Debug("could not read a container's log for a search", "container", r.ContainerName, "error", err)
		return nil, false, nil
	}
	result.Sources++
	found, _ = matches.newestFirst()
	return found, matches.more, nil
}

// searchEntry reads one entry's file and returns its matching lines before
// line upTo (negative: all), newest first and at most keep. next is the line
// the search goes on before in this entry, negative when the entry is done.
func (e *Engine) searchEntry(a store.LogArchive, q LogSearch, needle []byte, upTo, keep int, result *api.LogSearchResult) (found []api.LogLine, next int, err error) {
	matches := lastMatches{keep: keep}
	n := 0
	path := logFilePath(e.opts.LogArchive.Dir, store.LogArchiveFile{ID: a.ID, ApplicationID: a.ApplicationID})
	err = readLogFile(path, func(at time.Time, stream string, message []byte) {
		at0 := n
		n++
		result.Bytes += int64(len(message))
		if upTo >= 0 && at0 >= upTo {
			return
		}
		if (!q.Since.IsZero() && at.Before(q.Since)) || (!q.Until.IsZero() && at.After(q.Until)) {
			return
		}
		if containsFold(message, needle) {
			matches.add(api.LogLine{Replica: a.Replica, Container: a.ContainerName, Stream: stream, Time: at, Message: string(message)}, at0)
		}
	})
	if err != nil {
		return nil, -1, err
	}
	result.Sources++
	found, oldest := matches.newestFirst()
	if !matches.more {
		oldest = -1
	}
	return found, oldest, nil
}

// containsFold reports whether line contains needle, which is in lower case,
// whatever the case of the line. A needle of ASCII is compared byte by byte
// without copying the line, which is what nearly every search is.
func containsFold(line, needle []byte) bool {
	n := len(needle)
	if n == 0 {
		return true
	}
	for _, b := range needle {
		if b >= 0x80 {
			return bytes.Contains(bytes.ToLower(line), needle)
		}
	}
	first := needle[0]
	for i := 0; i+n <= len(line); i++ {
		if lowerASCII(line[i]) != first {
			continue
		}
		j := 1
		for j < n && lowerASCII(line[i+j]) == needle[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}
