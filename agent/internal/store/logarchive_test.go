package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestLogArchiveEntriesAreListedNewestFirstAndNarrowed(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateJobRun(ctx, JobRun{ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: "nightly", Kind: api.RunKindScheduled, Command: []string{"report"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	first, last := at.Add(-time.Minute), at.Add(-time.Second)
	code := 1

	crashed, err := s.AddLogArchive(ctx, LogArchive{
		ApplicationID: d.ApplicationID, Kind: api.LogKindReplica, DeploymentID: &d.ID, Replica: 2,
		ContainerID: "abc", ContainerName: "shipwick_my-api_1_2", Reason: api.LogReasonCrashed, ExitCode: &code,
		EndedAt: at, FirstAt: &first, LastAt: &last, Lines: 40, Bytes: 4000, StoredBytes: 700, Truncated: true,
	}, at)
	if err != nil {
		t.Fatalf("AddLogArchive: %v", err)
	}
	silent, err := s.AddLogArchive(ctx, LogArchive{
		ApplicationID: d.ApplicationID, Kind: api.LogKindReplica, DeploymentID: &d.ID, Replica: 1,
		ContainerID: "def", ContainerName: "shipwick_my-api_1_1", Reason: api.LogReasonOOMKilled, OOMKilled: true, EndedAt: at.Add(time.Hour),
	}, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ofRun, err := s.AddLogArchive(ctx, LogArchive{
		ApplicationID: d.ApplicationID, Kind: api.LogKindRun, RunID: &run.ID, Job: "nightly",
		ContainerID: "ghi", ContainerName: "shipwick_my-api_job_nightly_1", Reason: string(api.RunSucceeded),
		EndedAt: at.Add(2 * time.Hour), FirstAt: &at, LastAt: &at, Lines: 1, Bytes: 10, StoredBytes: 30,
	}, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLogArchive(ctx, crashed)
	if err != nil {
		t.Fatalf("GetLogArchive: %v", err)
	}
	if got.Sequence != d.Sequence || got.Version != d.Version || got.Replica != 2 || got.Reason != api.LogReasonCrashed ||
		got.ExitCode == nil || *got.ExitCode != 1 || got.OOMKilled || !got.EndedAt.Equal(at) || !got.FirstAt.Equal(first) || !got.LastAt.Equal(last) ||
		got.Lines != 40 || got.Bytes != 4000 || got.StoredBytes != 700 || !got.Truncated || got.RunID != nil {
		t.Errorf("entry read back: %+v", got)
	}
	if got, _ := s.GetLogArchive(ctx, silent); got.FirstAt != nil || got.LastAt != nil || got.ExitCode != nil || !got.OOMKilled {
		t.Errorf("an entry without lines: %+v", got)
	}
	if _, err := s.GetLogArchive(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}

	ids := func(f LogArchiveFilter) []int64 {
		t.Helper()
		f.ApplicationID = d.ApplicationID
		entries, err := s.ListLogArchives(ctx, f)
		if err != nil {
			t.Fatalf("ListLogArchives(%+v): %v", f, err)
		}
		out := []int64{}
		for _, e := range entries {
			out = append(out, e.ID)
		}
		return out
	}
	for name, c := range map[string]struct {
		filter LogArchiveFilter
		want   []int64
	}{
		"all":            {LogArchiveFilter{}, []int64{ofRun, silent, crashed}},
		"limit":          {LogArchiveFilter{Limit: 2}, []int64{ofRun, silent}},
		"before":         {LogArchiveFilter{Before: silent}, []int64{crashed}},
		"replicas":       {LogArchiveFilter{Kind: api.LogKindReplica}, []int64{silent, crashed}},
		"replica 2":      {LogArchiveFilter{Replica: 2}, []int64{crashed}},
		"run":            {LogArchiveFilter{RunID: run.ID}, []int64{ofRun}},
		"deployment":     {LogArchiveFilter{DeploymentID: d.ID}, []int64{silent, crashed}},
		"other":          {LogArchiveFilter{DeploymentID: d.ID + 1}, []int64{}},
		"since its last": {LogArchiveFilter{Since: last, Kind: api.LogKindReplica}, []int64{silent, crashed}},
		"after its last": {LogArchiveFilter{Since: last.Add(time.Nanosecond), Kind: api.LogKindReplica}, []int64{silent}},
		"until first":    {LogArchiveFilter{Until: first}, []int64{crashed}},
		"before first":   {LogArchiveFilter{Until: first.Add(-time.Nanosecond)}, []int64{}},
	} {
		got := ids(c.filter)
		same := len(got) == len(c.want)
		for i := 0; same && i < len(got); i++ {
			same = got[i] == c.want[i]
		}
		if !same {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestLogArchivedThroughIsTheLaterOfTheLastLineAndTheEnd(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	at := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)

	if through, err := s.LogArchivedThrough(ctx, "abc"); err != nil || !through.IsZero() {
		t.Fatalf("a container never archived: %v, %v", through, err)
	}
	add := func(ended, last time.Time) {
		t.Helper()
		if _, err := s.AddLogArchive(ctx, LogArchive{ApplicationID: d.ApplicationID, Kind: api.LogKindReplica, ContainerID: "abc", ContainerName: "c",
			Reason: api.LogReasonCrashed, EndedAt: ended, FirstAt: &last, LastAt: &last, Lines: 1}, ended); err != nil {
			t.Fatal(err)
		}
	}
	add(at, at.Add(-time.Second))
	if through, _ := s.LogArchivedThrough(ctx, "abc"); !through.Equal(at) {
		t.Errorf("through = %v, want the end %v", through, at)
	}
	add(at.Add(time.Minute), at.Add(time.Minute+time.Millisecond))
	if through, _ := s.LogArchivedThrough(ctx, "abc"); !through.Equal(at.Add(time.Minute + time.Millisecond)) {
		t.Errorf("through = %v, want the last line of the newest entry", through)
	}
	if through, _ := s.LogArchivedThrough(ctx, "another"); !through.IsZero() {
		t.Errorf("another container: %v", through)
	}
}

func TestLogArchiveIsPrunedByAgeAndBySizeOldestFirst(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var ids []int64
	for i := 0; i < 5; i++ {
		ended := at.Add(time.Duration(i) * 24 * time.Hour)
		id, err := s.AddLogArchive(ctx, LogArchive{ApplicationID: d.ApplicationID, Kind: api.LogKindReplica, ContainerID: "c", ContainerName: "c",
			Reason: api.LogReasonReplaced, EndedAt: ended, FirstAt: &ended, LastAt: &ended, Lines: 1, Bytes: 100, StoredBytes: 100}, ended)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if n, bytes, err := s.LogArchiveUsage(ctx); err != nil || n != 5 || bytes != 500 {
		t.Fatalf("usage = %d entries, %d bytes, %v", n, bytes, err)
	}

	removed, err := s.PruneLogArchivesBefore(ctx, at.Add(24*time.Hour))
	if err != nil || len(removed) != 1 || removed[0].ID != ids[0] || removed[0].ApplicationID != d.ApplicationID {
		t.Fatalf("pruned by age: %+v, %v; want the first entry", removed, err)
	}
	if removed, err = s.PruneLogArchivesTo(ctx, 400); err != nil || len(removed) != 0 {
		t.Fatalf("within its size: %+v, %v", removed, err)
	}
	removed, err = s.PruneLogArchivesTo(ctx, 250)
	if err != nil || len(removed) != 2 || removed[0].ID != ids[1] || removed[1].ID != ids[2] {
		t.Fatalf("pruned by size: %+v, %v; want the two oldest", removed, err)
	}
	files, err := s.LogArchiveFiles(ctx, 0)
	if err != nil || len(files) != 2 || files[0].ID != ids[3] || files[1].ID != ids[4] {
		t.Errorf("files left: %+v, %v", files, err)
	}
	if removed, _ = s.PruneLogArchivesTo(ctx, 0); len(removed) != 2 {
		t.Errorf("a size of nothing keeps nothing: %+v", removed)
	}
}

func TestLogArchiveGoesWithItsApplicationAndTheOutputOfARunWithItsRun(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	keep, _ := s.CreateDeployment(ctx, testApp("other", "nginx:1"), time.Now())
	run, _ := s.CreateJobRun(ctx, JobRun{ApplicationID: d.ApplicationID, Job: "nightly", Kind: api.RunKindScheduled, Command: []string{"x"}}, time.Now())
	now := time.Now()
	entry := func(app int64, runID *int64) LogArchive {
		return LogArchive{ApplicationID: app, Kind: api.LogKindReplica, RunID: runID, ContainerID: "c", ContainerName: "c", Reason: "r", EndedAt: now, Lines: 1, StoredBytes: 1}
	}
	if _, err := s.AddLogArchive(ctx, entry(d.ApplicationID, nil), now); err != nil {
		t.Fatal(err)
	}
	ofRun, err := s.AddLogArchive(ctx, entry(d.ApplicationID, &run.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLogArchive(ctx, entry(keep.ApplicationID, nil), now); err != nil {
		t.Fatal(err)
	}

	if err := s.PruneJobRuns(ctx, d.ApplicationID, "nightly", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLogArchive(ctx, ofRun); !errors.Is(err, ErrNotFound) {
		t.Errorf("the entry of a pruned run: %v, want not found", err)
	}
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := s.LogArchiveUsage(ctx); n != 1 {
		t.Errorf("entries left = %d, want the other application's one", n)
	}

	// Output that arrives for an application that is gone is not kept.
	if _, err := s.AddLogArchive(ctx, entry(d.ApplicationID, nil), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("an entry for a deleted application: %v, want ErrNotFound", err)
	}
	gone := int64(4242)
	if _, err := s.AddLogArchive(ctx, entry(keep.ApplicationID, &gone), now); !errors.Is(err, ErrNotFound) {
		t.Errorf("an entry for a pruned run: %v, want ErrNotFound", err)
	}
}

func TestLogArchiveOverItsSizeTakesFromTheApplicationThatHoldsTheMost(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	quiet, _ := s.CreateDeployment(ctx, testApp("quiet", "nginx:1"), time.Now())
	noisy, _ := s.CreateDeployment(ctx, testApp("noisy", "nginx:1"), time.Now())
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	add := func(app int64, bytes int64, after time.Duration) int64 {
		t.Helper()
		ended := at.Add(after)
		id, err := s.AddLogArchive(ctx, LogArchive{ApplicationID: app, Kind: api.LogKindReplica, ContainerID: "c", ContainerName: "c",
			Reason: api.LogReasonCrashed, EndedAt: ended, Lines: 1, Bytes: bytes, StoredBytes: bytes}, ended)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	crash := add(quiet.ApplicationID, 100, 0) // the oldest entry of all
	silent := add(quiet.ApplicationID, 0, time.Minute)
	var chatter []int64
	for i := 1; i <= 5; i++ {
		chatter = append(chatter, add(noisy.ApplicationID, 300, time.Duration(i)*time.Hour))
	}

	removed, err := s.PruneLogArchivesTo(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0].ID != chatter[0] || removed[1].ID != chatter[1] || removed[0].ApplicationID != noisy.ApplicationID {
		t.Fatalf("removed = %+v, want the two oldest entries of the application that holds the most", removed)
	}
	for _, id := range []int64{crash, silent} {
		if _, err := s.GetLogArchive(ctx, id); err != nil {
			t.Errorf("the quiet application lost entry %d: %v", id, err)
		}
	}

	// Once the two hold about as much, the one that holds more gives next.
	removed, err = s.PruneLogArchivesTo(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.LogArchiveUsage(ctx); total != 0 || len(removed) != 4 {
		t.Errorf("down to nothing: %d bytes left, removed %+v", total, removed)
	}
	if _, err := s.GetLogArchive(ctx, silent); err != nil {
		t.Errorf("an entry without lines takes no space and is not pruned by size: %v", err)
	}
}
