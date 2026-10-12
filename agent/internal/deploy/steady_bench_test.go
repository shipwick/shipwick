package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/spec"
)

// The benchmarks here measure the agent's own share of its steady state —
// what a supervision pass, a sampling round and the API's views cost with n
// applications of two replicas, each with a hostname and a health check, on
// the in-memory fakes. Docker's share is measured by scripts/measure-limits.sh.

// steadyState is an engine with n such applications deployed and idle.
type steadyState struct {
	e    *Engine
	st   *store.Store
	rt   *dockertest.Fake
	opts Options
	now  time.Time
}

func newSteadyState(b *testing.B, n int) *steadyState {
	b.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:", store.Options{EncryptionKey: []byte("an-encryption-key-of-32-bytes!!!")})
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	rt.CPUBusy, rt.MemoryUsed = 0.1, 64<<20
	s := &steadyState{rt: rt, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rt.Clock = func() time.Time { return s.now }
	dns := newFakeResolver()
	s.st = st
	s.opts = Options{
		StabilizeWindow: time.Millisecond,
		NameSettle:      time.Millisecond,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		LookupHost:      dns.LookupHost,
		ServerAddresses: []string{serverAddress},
		Proxy:           &fakeProxy{rt: rt},
		Probe:           func(context.Context, string, int, string, time.Duration) error { return nil },
		RedirectProbe:   answerWith(200, ""),
	}
	e := New(st, rt, s.opts)
	e.sup.inlineProbes = true
	b.Cleanup(func() {
		e.Shutdown(ctx)
		st.Close()
	})
	for i := range n {
		a := app(fmt.Sprintf("app-%03d", i), fmt.Sprintf("registry.example.com/app-%03d:1.0", i), 2)
		a.Domain = fmt.Sprintf("app-%03d.example.com", i)
		a.Health = &spec.Health{Path: "/health", Interval: spec.Duration(10 * time.Second), Timeout: spec.Duration(2 * time.Second), Retries: 3}
		if _, err := e.Deploy(ctx, a); err != nil {
			b.Fatalf("deploy %s: %v", a.Name, err)
		}
	}
	e.Wait()
	s.e = e
	// One pass so that every replica is known to the supervisor, as on a
	// server that has been running for a while.
	s.tick()
	return s
}

func (s *steadyState) tick() {
	s.now = s.now.Add(time.Second)
	s.e.sup.tick(context.Background(), s.now)
}

var steadySizes = []int{10, 50, 100, 200}

func BenchmarkSupervisorTick(b *testing.B) {
	for _, n := range steadySizes {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			s := newSteadyState(b, n)
			b.ResetTimer()
			for b.Loop() {
				s.tick()
			}
		})
	}
}

func BenchmarkSamplerPass(b *testing.B) {
	for _, n := range steadySizes {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			s := newSteadyState(b, n)
			ctx := context.Background()
			b.ResetTimer()
			for b.Loop() {
				s.now = s.now.Add(30 * time.Second)
				s.e.sample(ctx, s.now)
				s.e.checkAlerts(ctx, s.now)
			}
		})
	}
}

func BenchmarkApplicationsView(b *testing.B) {
	for _, n := range steadySizes {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			s := newSteadyState(b, n)
			ctx := context.Background()
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.e.Applications(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkServerView(b *testing.B) {
	for _, n := range steadySizes {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			s := newSteadyState(b, n)
			ctx := context.Background()
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.e.Server(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAgentStart is what an agent does with its database and Docker
// before it answers, on a server of n applications: recover what was left
// and tell the proxy the routes. Opening the database and its migrations are
// not in it; neither is Docker's share.
func BenchmarkAgentStart(b *testing.B) {
	for _, n := range []int{50, 100} {
		b.Run(fmt.Sprintf("apps=%d", n), func(b *testing.B) {
			s := newSteadyState(b, n)
			ctx := context.Background()
			b.ResetTimer()
			for b.Loop() {
				e := New(s.st, s.rt, s.opts)
				if err := e.Recover(ctx); err != nil {
					b.Fatal(err)
				}
				if err := e.SyncProxy(ctx); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				e.Shutdown(ctx)
				b.StartTimer()
			}
		})
	}
}

// BenchmarkRecordAccess is one line of the proxy's access log, parsed and
// counted: what every request through the proxy costs the agent.
func BenchmarkRecordAccess(b *testing.B) {
	s := newSteadyState(b, 50)
	ctx := context.Background()
	line := []byte(accessLine(s.now, "app-010.example.com", "GET", "/api/items/42", 200, 3*time.Millisecond, 512))
	s.e.recordAccess(ctx, line, s.now)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.e.recordAccess(ctx, line, s.now)
	}
}
