package deploy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

func (h *harness) search(name string, q LogSearch) api.LogSearchResult {
	h.t.Helper()
	if q.Limit == 0 {
		q.Limit = 100
	}
	result, err := h.engine.SearchLogs(context.Background(), name, q)
	if err != nil {
		h.t.Fatalf("SearchLogs(%+v): %v", q, err)
	}
	return result
}

// searchAll follows a search to its end and returns every line it found.
func (h *harness) searchAll(name string, q LogSearch) (lines []api.LogMatch, pages int) {
	h.t.Helper()
	for {
		result := h.search(name, q)
		lines = append(lines, result.Lines...)
		pages++
		if result.Next == "" {
			return lines, pages
		}
		if pages > 1000 {
			h.t.Fatal("search does not end")
		}
		q.Cursor = result.Next
	}
}

func messages(lines []api.LogMatch) []string {
	out := []string{}
	for _, line := range lines {
		out = append(out, line.Message)
	}
	return out
}

// history deploys two versions of my-api with two replicas each, and leaves
// the first in the archive and the second running. Each replica printed
// three lines.
func history(h *harness) (v1, v2 store.Deployment) {
	h.t.Helper()
	keepLogs(h)
	print := func(version string) {
		for _, c := range h.rt.Containers() {
			h.rt.WriteLogs(c.ID,
				fmt.Sprintf("%s replica %d started", version, c.Replica),
				fmt.Sprintf("%s replica %d: Connection RESET by peer", version, c.Replica),
				fmt.Sprintf("%s replica %d served GET /orders", version, c.Replica))
		}
	}
	v1 = h.deploy(app("my-api", "my-api:1.0", 2))
	print("v1")
	v2 = h.deploy(app("my-api", "my-api:1.1", 2))
	print("v2")
	return v1, v2
}

func TestSearchFindsTextInTheArchiveAndInTheRunningReplicasWhateverItsCase(t *testing.T) {
	h := newHarness(t)
	v1, v2 := history(h)

	result := h.search("my-api", LogSearch{Text: "connection reset"})
	wantLines(t, "matches, running replicas first", messages(result.Lines),
		"v2 replica 2: Connection RESET by peer",
		"v2 replica 1: Connection RESET by peer",
		"v1 replica 2: Connection RESET by peer",
		"v1 replica 1: Connection RESET by peer")
	if result.Next != "" || result.Sources != 4 || result.Bytes == 0 {
		t.Errorf("next=%q sources=%d bytes=%d, want everything read in one answer", result.Next, result.Sources, result.Bytes)
	}

	live, kept := result.Lines[0], result.Lines[3]
	if live.ArchiveID != nil || live.DeploymentID == nil || *live.DeploymentID != v2.ID || live.Deployment != v2.Sequence || live.Replica != 2 || live.Time.IsZero() || live.Stream != "stdout" {
		t.Errorf("a line of a running replica: %+v", live)
	}
	if kept.ArchiveID == nil || *kept.DeploymentID != v1.ID || kept.Deployment != v1.Sequence || kept.Replica != 1 || kept.Container == "" || kept.Time.IsZero() {
		t.Errorf("a line of the archive: %+v", kept)
	}
}

func TestSearchWithoutTextReturnsEveryLineNewestFirstWithinItsSource(t *testing.T) {
	h := newHarness(t)
	history(h)
	result := h.search("my-api", LogSearch{Replica: 2})
	wantLines(t, "replica 2", messages(result.Lines),
		"v2 replica 2 served GET /orders", "v2 replica 2: Connection RESET by peer", "v2 replica 2 started",
		"v1 replica 2 served GET /orders", "v1 replica 2: Connection RESET by peer", "v1 replica 2 started")
}

func TestSearchIsNarrowedByDeploymentReplicaAndTime(t *testing.T) {
	h := newHarness(t)
	v1, v2 := history(h)

	wantLines(t, "deployment 1", messages(h.search("my-api", LogSearch{Text: "started", DeploymentID: v1.ID}).Lines),
		"v1 replica 2 started", "v1 replica 1 started")
	wantLines(t, "deployment 2, replica 1", messages(h.search("my-api", LogSearch{Text: "started", DeploymentID: v2.ID, Replica: 1}).Lines),
		"v2 replica 1 started")

	all := h.search("my-api", LogSearch{Text: "served"}).Lines
	oldest := all[len(all)-1]
	wantLines(t, "until the oldest match", messages(h.search("my-api", LogSearch{Text: "served", Until: oldest.Time}).Lines), oldest.Message)
	newest := all[0]
	wantLines(t, "since the newest match", messages(h.search("my-api", LogSearch{Text: "served", Since: newest.Time, Replica: newest.Replica}).Lines), newest.Message)

	other := h.deploy(app("other", "other:1.0", 1))
	if _, err := h.engine.SearchLogs(context.Background(), "my-api", LogSearch{DeploymentID: other.ID, Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another application's deployment: %v, want not found", err)
	}
	if _, err := h.engine.SearchLogs(context.Background(), "nope", LogSearch{Limit: 10}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown application: %v, want not found", err)
	}
}

func TestSearchIsPagedAndEveryLineComesOnce(t *testing.T) {
	h := newHarness(t)
	history(h)
	want := messages(h.search("my-api", LogSearch{}).Lines)
	if len(want) != 12 {
		t.Fatalf("lines = %d, want 12", len(want))
	}
	for _, limit := range []int{1, 2, 5, 12} {
		got, pages := h.searchAll("my-api", LogSearch{Limit: limit})
		wantLines(t, fmt.Sprintf("pages of %d", limit), messages(got), want...)
		if minimum := (12 + limit - 1) / limit; pages < minimum {
			t.Errorf("limit %d: %d pages, want at least %d", limit, pages, minimum)
		}
	}
}

func TestSearchStopsAtItsBudgetAndGoesOnFromThere(t *testing.T) {
	h := newHarness(t)
	history(h)
	defer func(was int64) { searchBudget = was }(searchBudget)
	searchBudget = 1 // every source is over it

	first := h.search("my-api", LogSearch{Text: "v1 replica 1 started"})
	if len(first.Lines) != 0 || first.Next == "" || first.Sources != 1 {
		t.Fatalf("first answer = %+v, want one source read, nothing found yet, and a next", first)
	}
	got, pages := h.searchAll("my-api", LogSearch{Text: "v1 replica 1 started"})
	wantLines(t, "found in the last source", messages(got), "v1 replica 1 started")
	if pages != 4 {
		t.Errorf("pages = %d, want one per source", pages)
	}
}

func TestSearchDoesNotFindALineTwiceInTheArchiveAndInItsContainer(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.deploy(app("my-api", "my-api:1.0", 1))
	id := h.replicaAt(1).ID
	h.rt.WriteLogs(id, "needle before the stop")
	if err := h.engine.Stop(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	if err := h.engine.Start(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	h.rt.WriteLogs(id, "needle after the start")

	result := h.search("my-api", LogSearch{Text: "needle"})
	wantLines(t, "one of each", messages(result.Lines), "needle after the start", "needle before the stop")
	if result.Lines[0].ArchiveID != nil || result.Lines[1].ArchiveID == nil {
		t.Errorf("sources: %+v", result.Lines)
	}
}

func TestSearchFindsTheOutputOfARun(t *testing.T) {
	h := newHarness(t)
	keepLogs(h)
	h.rt.PrintOnStart("my-api:1.0", "applying migration 0042")
	h.deploy(app("my-api", "my-api:1.0", 1))
	run, err := h.engine.RunCommand(context.Background(), "my-api", []string{"migrate"})
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()

	result := h.search("my-api", LogSearch{Text: "MIGRATION", RunID: run.ID})
	if len(result.Lines) != 1 || result.Lines[0].RunID == nil || *result.Lines[0].RunID != run.ID || result.Lines[0].Job != commandJobName || result.Lines[0].Replica != 0 {
		t.Fatalf("lines = %+v, want the run's line and nothing of the replica", result.Lines)
	}
}

func TestSearchRefusesACursorItDidNotReturn(t *testing.T) {
	h := newHarness(t)
	history(h)
	for _, cursor := range []string{"x", "a", "l", "a-1", "a1.2.3", "l1.x", "b12", "a99999999999999999999", "a1.99999999999"} {
		if _, err := h.engine.SearchLogs(context.Background(), "my-api", LogSearch{Limit: 10, Cursor: cursor}); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor %q: %v, want ErrInvalidCursor", cursor, err)
		}
	}
	for _, c := range []searchCursor{{live: true}, {live: true, replica: 2}, {live: true, replica: 1, before: time.Unix(0, 1790000000123456789).UTC()}, {entry: 7, line: -1}, {entry: 7, line: 0}, {entry: 7, line: 41}} {
		back, err := parseSearchCursor(c.String())
		if c == (searchCursor{live: true}) {
			back, err = parseSearchCursor("")
		}
		if err != nil || back != c {
			t.Errorf("cursor %+v became %q and came back as %+v (%v)", c, c.String(), back, err)
		}
	}
}

func TestContainsFold(t *testing.T) {
	for _, c := range []struct {
		line, needle string
		want         bool
	}{
		{"Connection RESET by peer", "reset", true},
		{"Connection RESET by peer", "connection reset by peer", true},
		{"Connection RESET by peer", "peer!", false},
		{"abc", "abcd", false},
		{"aab", "ab", true},
		{"anything", "", true},
		{"", "x", false},
		{"Zahlung ÜBERFÄLLIG", "überfällig", true},
		{"status=500", "STATUS", false}, // the needle is given in lower case
	} {
		if got := containsFold([]byte(c.line), []byte(c.needle)); got != c.want {
			t.Errorf("containsFold(%q, %q) = %v, want %v", c.line, c.needle, got, c.want)
		}
	}
}
