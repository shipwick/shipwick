package upstreams

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// What one request costs in the source, once a name is known: the answer is
// younger than refresh, and nothing is asked.
func BenchmarkAddressesAnswered(b *testing.B) {
	for _, names := range []int{10, 200} {
		b.Run(fmt.Sprintf("names=%d", names), func(b *testing.B) {
			t := newTable()
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			t.now = func() time.Time { return now }
			t.resolve = func(_ context.Context, name string) ([]string, error) {
				return []string{"10.0.0.2", "10.0.0.3"}, nil
			}
			s := settings{refresh: time.Hour, wait: 200 * time.Millisecond, keep: 2 * time.Second, timeout: time.Second}
			ctx := context.Background()
			for i := range names {
				name := fmt.Sprintf("app-%03d-80", i)
				t.mu.Lock()
				e := &entry{addrs: []string{"10.0.0.2", "10.0.0.3"}, answered: now, asked: now}
				t.entries[name] = e
				t.mu.Unlock()
				if _, err := t.addresses(ctx, name, s); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := t.addresses(ctx, "app-005-80", s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGetUpstreams is the module's whole answer to the proxy for one
// request, allocations included.
func BenchmarkGetUpstreams(b *testing.B) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names.mu.Lock()
	names.entries["bench-app-80"] = &entry{addrs: []string{"10.0.0.2", "10.0.0.3"}, answered: now, asked: now}
	names.mu.Unlock()
	saved := names.now
	names.now = func() time.Time { return now }
	defer func() { names.now = saved }()
	r := &Replicas{Name: "bench-app-80", Port: "8080", Refresh: 1e9, Wait: 2e8, Keep: 2e9}
	req := httptest.NewRequest(http.MethodGet, "http://bench-app.example.com/", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.GetUpstreams(req); err != nil {
			b.Fatal(err)
		}
	}
}
