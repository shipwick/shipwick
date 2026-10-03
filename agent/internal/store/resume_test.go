package store

import (
	"context"
	"testing"
	"time"
)

func TestCompleteDeploymentsExceptLeavesTheResumedOnesOpen(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	done, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	cutOff, _ := s.CreateDeployment(ctx, testApp("api", "nginx:2"), time.Now())
	resumed, _ := s.CreateDeployment(ctx, testApp("web", "nginx:3"), time.Now())
	early := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.CompleteDeployment(ctx, done.ID, early)

	if err := s.CompleteDeploymentsExcept(ctx, []int64{resumed.ID}, early.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteDeploymentsExcept: %v", err)
	}
	if got, _ := s.GetDeployment(ctx, done.ID); !got.CompletedAt.Equal(early) {
		t.Error("an existing completion time must be preserved")
	}
	if got, _ := s.GetDeployment(ctx, cutOff.ID); got.CompletedAt == nil {
		t.Error("the deployment that was cut off should have been stamped")
	}
	if got, _ := s.GetDeployment(ctx, resumed.ID); got.CompletedAt != nil {
		t.Error("a deployment that resumes is not completed: its clients are still polling it")
	}

	// With nothing to leave out, everything is stamped.
	if err := s.CompleteDeploymentsExcept(ctx, nil, early.Add(2*time.Hour)); err != nil {
		t.Fatalf("CompleteDeploymentsExcept: %v", err)
	}
	if got, _ := s.GetDeployment(ctx, resumed.ID); got.CompletedAt == nil {
		t.Error("without exceptions every unfinished deployment is stamped")
	}
}
