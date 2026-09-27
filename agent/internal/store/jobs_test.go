package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestJobRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)

	run, err := s.CreateJobRun(ctx, JobRun{ApplicationID: d.ApplicationID, DeploymentID: &d.ID, Job: "nightly", Kind: api.RunKindScheduled, Command: []string{"node", "report.js"}}, now)
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	if run.ID == 0 || run.Application != "my-api" || run.Status != api.RunRunning || !run.StartedAt.Equal(now) {
		t.Errorf("unexpected run: %+v", run)
	}

	code := 1
	if err := s.FinishJobRun(ctx, run.ID, api.RunFailed, &code, "boom\n", now.Add(time.Minute)); err != nil {
		t.Fatalf("FinishJobRun: %v", err)
	}
	got, err := s.GetJobRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetJobRun: %v", err)
	}
	if got.Status != api.RunFailed || got.ExitCode == nil || *got.ExitCode != 1 || got.Output != "boom\n" ||
		got.FinishedAt == nil || !got.FinishedAt.Equal(now.Add(time.Minute)) || got.Command[1] != "report.js" ||
		got.DeploymentID == nil || *got.DeploymentID != d.ID {
		t.Errorf("unexpected run: %+v", got)
	}

	// Finishing is once: a second outcome does not overwrite the first.
	if err := s.FinishJobRun(ctx, run.ID, api.RunSucceeded, nil, "", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetJobRun(ctx, run.ID); again.Status != api.RunFailed {
		t.Errorf("a finished run was changed: %+v", again)
	}

	if _, err := s.GetJobRun(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJobRun(999) = %v, want ErrNotFound", err)
	}
}

func TestJobRunListingsAndPruning(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("my-api", "nginx:1"), time.Now())
	other, _ := s.CreateDeployment(ctx, testApp("other", "nginx:1"), time.Now())
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	var ids []int64
	for i := 0; i < 3; i++ {
		r, _ := s.CreateJobRun(ctx, JobRun{ApplicationID: d.ApplicationID, Job: "a", Kind: api.RunKindScheduled, Command: []string{"x"}}, now.Add(time.Duration(i)*time.Minute))
		s.FinishJobRun(ctx, r.ID, api.RunSucceeded, new(int), "output "+string(rune('0'+i)), now)
		ids = append(ids, r.ID)
	}
	b, _ := s.CreateJobRun(ctx, JobRun{ApplicationID: d.ApplicationID, Job: "b", Kind: api.RunKindManual, Command: []string{"y"}}, now)
	s.CreateJobRun(ctx, JobRun{ApplicationID: other.ApplicationID, Job: "a", Kind: api.RunKindScheduled, Command: []string{"z"}}, now)

	all, err := s.ListJobRuns(ctx, d.ApplicationID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].ID != b.ID || all[1].ID != ids[2] || all[0].Output != "" {
		t.Errorf("unexpected listing: %+v", all)
	}
	onlyA, _ := s.ListJobRuns(ctx, d.ApplicationID, "a", 2)
	if len(onlyA) != 2 || onlyA[0].ID != ids[2] || onlyA[1].ID != ids[1] {
		t.Errorf("unexpected filtered listing: %+v", onlyA)
	}

	last, err := s.LastJobRuns(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 || last["a"].ID != ids[2] || last["b"].Status != api.RunRunning {
		t.Errorf("unexpected last runs: %+v", last)
	}

	if err := s.PruneJobRuns(ctx, d.ApplicationID, "a", 1); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListJobRuns(ctx, d.ApplicationID, "a", 0); len(left) != 1 || left[0].ID != ids[2] {
		t.Errorf("pruning kept the wrong runs: %+v", left)
	}
	if others, _ := s.ListJobRuns(ctx, other.ApplicationID, "", 0); len(others) != 1 {
		t.Errorf("pruning touched another application's runs: %+v", others)
	}

	n, err := s.MarkJobRunsInterrupted(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("MarkJobRunsInterrupted = %d, %v; want 2 (one per application)", n, err)
	}
	if got, _ := s.GetJobRun(ctx, b.ID); got.Status != api.RunInterrupted || got.FinishedAt == nil {
		t.Errorf("running run not interrupted: %+v", got)
	}

	// The runs go with the application.
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJobRun(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("run survived its application: %v", err)
	}
}
