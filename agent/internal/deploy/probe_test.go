package deploy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

var pgIsReady = []string{"pg_isready", "-U", "postgres"}

func withCommandHealth(a spec.App) spec.App {
	a.Health = &spec.Health{Command: pgIsReady, Interval: spec.Duration(10 * time.Second), Timeout: spec.Duration(time.Second), Retries: 3}
	return a
}

func withTCPHealth(a spec.App, port int) spec.App {
	a.Health = &spec.Health{TCP: port, Interval: spec.Duration(10 * time.Second), Timeout: spec.Duration(time.Second), Retries: 3}
	return a
}

// noHTTPProbe fails the test if an HTTP probe is attempted: the other kinds
// must never fall back to it.
func noHTTPProbe(t *testing.T) ProbeFunc {
	return func(context.Context, string, int, string, time.Duration) error {
		t.Error("an HTTP probe was made for a check that is not HTTP")
		return errors.New("wrong probe")
	}
}

func TestDeployWithCommandCheckBecomesHealthy(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.Probe = noHTTPProbe(t)

	d := s.deploy(withCommandHealth(app("db", "postgres:16", 1)))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	calls := s.rt.ExecCalls()
	if len(calls) == 0 {
		t.Fatal("the command was never run in the replica")
	}
	c := s.container(t, 1)
	if got := calls[0]; got.Container != c.Name || strings.Join(got.Cmd, " ") != "pg_isready -U postgres" || got.Timeout != time.Second {
		t.Errorf("exec = %+v; want the spec's argv, in the replica, bounded by health.timeout", got)
	}
}

func TestDeployFailsWhenTheCommandKeepsFailing(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.Probe = noHTTPProbe(t)
	s.engine.opts.StartupPollInterval = 5 * time.Millisecond
	s.rt.SetExecResult("pg_isready -U postgres", dockertest.ExecResult{
		ExitCode: 2,
		Output:   strings.Repeat("waiting for the server to start\n", 300) + "pg_isready: no response\n",
	})

	bad := withCommandHealth(app("db", "postgres:16", 1))
	bad.Health.Interval, bad.Health.Retries = spec.Duration(20*time.Millisecond), 3 // budget: 60ms
	d := s.deploy(bad)
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	for _, want := range []string{"did not become healthy within 60ms", "command exited 2: pg_isready: no response"} {
		if !strings.Contains(d.Error, want) {
			t.Errorf("error %q should mention %q", d.Error, want)
		}
	}
	if strings.Contains(d.Error, "waiting for") || len(d.Error) > 300 {
		t.Errorf("only the last line of the output belongs in the error: %q", d.Error)
	}
}

func TestDeployWithTCPCheckUsesTheTCPProbe(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.Probe = noHTTPProbe(t)
	var mu sync.Mutex
	var probed []string
	s.engine.opts.ProbeTCP = func(_ context.Context, ip string, port int, timeout time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		probed = append(probed, ip+":"+strconv.Itoa(port))
		return nil
	}

	d := s.deploy(withTCPHealth(app("db", "postgres:16", 1), 5432))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := s.container(t, 1).IP + ":5432"; len(probed) == 0 || probed[0] != want {
		t.Errorf("probed %v, want a connect to %s: the replica's address and health.tcp, not port", probed, want)
	}
	if len(s.rt.ExecCalls()) != 0 {
		t.Error("a TCP check must not run anything in the replica")
	}
}

func TestDeployFailsWhenTheTCPPortRefuses(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.Probe = noHTTPProbe(t)
	s.engine.opts.StartupPollInterval = 5 * time.Millisecond
	s.engine.opts.ProbeTCP = func(context.Context, string, int, time.Duration) error {
		return errors.New("connection refused")
	}

	bad := withTCPHealth(app("db", "postgres:16", 1), 5432)
	bad.Health.Interval, bad.Health.Retries = spec.Duration(20*time.Millisecond), 3
	d := s.deploy(bad)
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	if want := "did not become healthy within 60ms: TCP connect to port 5432: connection refused"; !strings.Contains(d.Error, want) {
		t.Errorf("error = %q, want it to contain %q", d.Error, want)
	}
}

func TestSupervisorMarksAndRestartsCommandUnhealthyReplica(t *testing.T) {
	s := newSupervised(t)
	s.engine.opts.Probe = noHTTPProbe(t)
	s.deploy(withCommandHealth(app("db", "postgres:16", 2)))
	sick := s.container(t, 1)

	s.advance(time.Second)
	if h, _ := s.engine.sup.snapshot(sick.ID); h != api.HealthHealthy {
		t.Fatalf("health = %q after a passing command, want healthy", h)
	}

	// The process runs on, but the check says it stopped answering.
	s.rt.SetExecResult(sick.Name, dockertest.ExecResult{ExitCode: 1, Output: "pg_isready: accepting connections\npg_isready: no response"})
	for range 3 {
		s.advance(10 * time.Second)
	}
	detail, _ := s.engine.Application(context.Background(), "db")
	if detail.Status != api.AppDegraded || detail.Replicas != (api.ReplicaCount{Desired: 2, Running: 2, Healthy: 1}) {
		t.Errorf("after 3 failures: status=%s replicas=%+v", detail.Status, detail.Replicas)
	}

	s.rt.SetExecResult(sick.Name, dockertest.ExecResult{}) // the restart cures it
	s.advance(time.Second)
	if got := s.rt.Starts(sick.ID); got != 2 {
		t.Fatalf("starts = %d, want 2: an unhealthy replica should be restarted", got)
	}
	s.advance(time.Second)
	detail, _ = s.engine.Application(context.Background(), "db")
	if detail.Status != api.AppHealthy || detail.Containers[0].Health != api.HealthHealthy {
		t.Errorf("after the restart: status=%s container=%+v", detail.Status, detail.Containers[0])
	}

	events := strings.Join(s.appEvents(t, "db"), "\n")
	if want := "Replica 1 failed 3 health checks in a row: command exited 1: pg_isready: no response"; !strings.Contains(events, want) {
		t.Errorf("missing event %q in:\n%s", want, events)
	}
}

func TestLastLine(t *testing.T) {
	tests := []struct {
		output, want string
	}{
		{"", ""},
		{"\n\n", ""},
		{"ready\n", "ready"},
		{"first\nsecond\r\n", "second"},
		{"   padded   ", "padded"},
		{"a\n" + strings.Repeat("x", 250), strings.Repeat("x", 200) + "…"},
	}
	for _, tt := range tests {
		if got := lastLine(tt.output); got != tt.want {
			t.Errorf("lastLine(%q) = %q, want %q", tt.output, got, tt.want)
		}
	}
}
