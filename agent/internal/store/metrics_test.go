package store

import (
	"context"
	"testing"
	"time"
)

func TestMetricSamplesAreBucketedOnRead(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	other, _ := s.CreateDeployment(ctx, testApp("web", "nginx:1"), time.Now())

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC) // on a 5-minute boundary
	var rows []MetricSample
	for i := 0; i < 10; i++ { // 30-second samples across a 5-minute bucket
		rows = append(rows,
			MetricSample{ApplicationID: d.ApplicationID, Replica: 1, At: base.Add(time.Duration(i) * 30 * time.Second), CPUPercent: float64(i), MemoryBytes: int64(100 + i)},
			MetricSample{ApplicationID: d.ApplicationID, Replica: 2, At: base.Add(time.Duration(i) * 30 * time.Second), CPUPercent: 50, MemoryBytes: 500},
		)
	}
	rows = append(rows, MetricSample{ApplicationID: other.ApplicationID, Replica: 1, At: base, CPUPercent: 99, MemoryBytes: 999})
	if err := s.AddSamples(ctx, rows); err != nil {
		t.Fatalf("AddSamples: %v", err)
	}

	got, err := s.MetricHistory(ctx, d.ApplicationID, base.Add(-time.Hour), 5*time.Minute)
	if err != nil {
		t.Fatalf("MetricHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("buckets = %+v, want one per replica", got)
	}
	// Average CPU, peak memory, bucket start; the other application's rows stay out.
	if got[0].Replica != 1 || got[0].CPUPercent != 4.5 || got[0].MemoryBytes != 109 || !got[0].At.Equal(base) {
		t.Errorf("replica 1 bucket = %+v", got[0])
	}
	if got[1].Replica != 2 || got[1].CPUPercent != 50 || got[1].MemoryBytes != 500 {
		t.Errorf("replica 2 bucket = %+v", got[1])
	}

	// A finer step keeps the raw samples apart, aligned to the step.
	fine, _ := s.MetricHistory(ctx, d.ApplicationID, base.Add(-time.Hour), 30*time.Second)
	if len(fine) != 20 || !fine[1].At.Equal(base.Add(30*time.Second)) || fine[1].CPUPercent != 1 {
		t.Errorf("30s buckets = %d, second = %+v", len(fine), fine[1])
	}
	// since is a lower bound on the raw samples.
	late, _ := s.MetricHistory(ctx, d.ApplicationID, base.Add(4*time.Minute), 30*time.Second)
	if len(late) != 4 {
		t.Errorf("buckets since 10:04 = %d, want the 2 samples of each replica from then on", len(late))
	}
	if _, err := s.MetricHistory(ctx, d.ApplicationID, base, 0); err == nil {
		t.Error("a zero step should be refused, not divide by zero in SQL")
	}
}

func TestPruneSamples(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	s.AddSamples(ctx, []MetricSample{
		{ApplicationID: d.ApplicationID, Replica: 1, At: now.Add(-8 * 24 * time.Hour), CPUPercent: 1, MemoryBytes: 1},
		{ApplicationID: d.ApplicationID, Replica: 1, At: now.Add(-6 * 24 * time.Hour), CPUPercent: 1, MemoryBytes: 1},
		{ApplicationID: d.ApplicationID, Replica: 1, At: now, CPUPercent: 1, MemoryBytes: 1},
	})

	n, err := s.PruneSamples(ctx, now.Add(-7*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneSamples = %d, %v; want 1 row removed", n, err)
	}
	got, _ := s.MetricHistory(ctx, d.ApplicationID, now.Add(-30*24*time.Hour), time.Hour)
	if len(got) != 2 {
		t.Errorf("%d buckets remain, want 2", len(got))
	}

	// Deleting the application takes its history with it.
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_samples`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d samples left after deleting the application (%v)", left, err)
	}
}

func TestAddSamplesIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	now := time.Now()
	err := s.AddSamples(ctx, []MetricSample{
		{ApplicationID: d.ApplicationID, Replica: 1, At: now, CPUPercent: 1, MemoryBytes: 1},
		{ApplicationID: 9999, Replica: 1, At: now, CPUPercent: 1, MemoryBytes: 1}, // no such application
	})
	if err == nil {
		t.Fatal("a sample of an unknown application should be refused")
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_samples`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows stored from a batch that failed; all or nothing", n)
	}
}
