package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestTrafficSamplesComeBackInOrderWithTheirHistogram(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	other, _ := s.CreateDeployment(ctx, testApp("web", "nginx:1"), time.Now())

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	sample := func(appID int64, minute int, requests int64) TrafficSample {
		return TrafficSample{ApplicationID: appID, At: base.Add(time.Duration(minute) * time.Minute), Requests: requests,
			Status2xx: requests - 3, Status3xx: 1, Status4xx: 1, Status5xx: 1, Bytes: requests * 1000, Latency: []int64{0, requests, 0, 7}}
	}
	// Written out of order, as a minute stored late would be.
	if err := s.AddTrafficSamples(ctx, []TrafficSample{sample(d.ApplicationID, 2, 30), sample(d.ApplicationID, 0, 10), sample(other.ApplicationID, 0, 99), sample(d.ApplicationID, 1, 20)}); err != nil {
		t.Fatalf("AddTrafficSamples: %v", err)
	}

	got, err := s.TrafficSamples(ctx, d.ApplicationID, base)
	if err != nil {
		t.Fatalf("TrafficSamples: %v", err)
	}
	if len(got) != 3 || got[0].Requests != 10 || got[1].Requests != 20 || got[2].Requests != 30 {
		t.Fatalf("samples = %+v, want this application's three, oldest first", got)
	}
	if want := sample(d.ApplicationID, 1, 20); !reflect.DeepEqual(got[1], want) {
		t.Errorf("sample = %+v\nwant     %+v", got[1], want)
	}
	// since is a lower bound.
	if late, _ := s.TrafficSamples(ctx, d.ApplicationID, base.Add(time.Minute)); len(late) != 2 {
		t.Errorf("%d samples since 10:01, want 2", len(late))
	}
}

func TestPruneTrafficSamples(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	old := TrafficSample{ApplicationID: d.ApplicationID, At: now.Add(-8 * 24 * time.Hour), Requests: 1, Latency: []int64{1}}
	kept := TrafficSample{ApplicationID: d.ApplicationID, At: now.Add(-6 * 24 * time.Hour), Requests: 2, Latency: []int64{2}}
	if err := s.AddTrafficSamples(ctx, []TrafficSample{old, kept}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneTrafficSamples(ctx, now.Add(-7*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (%v), want the one sample older than a week", n, err)
	}
	if got, _ := s.TrafficSamples(ctx, d.ApplicationID, time.Unix(0, 0)); len(got) != 1 || got[0].Requests != 2 {
		t.Errorf("left = %+v, want the six-day-old sample", got)
	}
}

func TestTrafficSamplesGoWithTheirApplication(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, _ := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now())
	if err := s.AddTrafficSamples(ctx, []TrafficSample{{ApplicationID: d.ApplicationID, At: time.Now(), Requests: 1, Latency: []int64{1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_samples`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d samples left after the application was deleted (%v)", n, err)
	}
}
