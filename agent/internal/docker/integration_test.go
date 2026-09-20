//go:build integration

// These tests talk to a real Docker daemon:
//
//	go test -tags integration ./agent/internal/docker/
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

const testImage = "nginx:alpine"

func newIntegrationRuntime(t *testing.T) (*Runtime, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	network := fmt.Sprintf("shipwick-test-%d", time.Now().UnixNano())
	rt, err := New(network)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.Ping(ctx); err != nil {
		t.Skipf("docker is not available: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rt.cli.NetworkRemove(cleanup, network, client.NetworkRemoveOptions{})
		rt.cli.NetworkRemove(cleanup, rt.services, client.NetworkRemoveOptions{})
		rt.Close()
	})
	return rt, ctx
}

func TestIntegrationContainerLifecycle(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)

	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork must be idempotent: %v", err)
	}
	if err := rt.PullImage(ctx, testImage); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if ok, err := rt.ImageExists(ctx, testImage); err != nil || !ok {
		t.Fatalf("ImageExists = %v, %v", ok, err)
	}
	if ok, err := rt.ImageExists(ctx, "shipwick/does-not-exist:nope"); err != nil || ok {
		t.Fatalf("ImageExists(missing) = %v, %v", ok, err)
	}

	id, name, err := rt.CreateContainer(ctx, ContainerSpec{
		App:          app,
		DeploymentID: 42,
		Sequence:     7,
		Replica:      1,
		Image:        testImage,
		Env:          map[string]string{"SHIPWICK_TEST": "yes"},
		NanoCPUs:     500_000_000,
		MemoryBytes:  64 << 20,
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(context.Background(), id) })
	if name != "shipwick_"+app+"_7_1" {
		t.Errorf("name = %q", name)
	}

	// The security-relevant settings must reach Docker as intended.
	raw, err := rt.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("raw inspect: %v", err)
	}
	hc := raw.Container.HostConfig
	if hc.NanoCPUs != 500_000_000 || hc.Memory != 64<<20 || hc.MemorySwap != 64<<20 {
		t.Errorf("limits not applied: cpus=%d memory=%d swap=%d", hc.NanoCPUs, hc.Memory, hc.MemorySwap)
	}
	if hc.Privileged || !hc.RestartPolicy.IsNone() || len(hc.SecurityOpt) == 0 || len(hc.PortBindings) != 0 {
		t.Errorf("unexpected host config: privileged=%v restart=%q securityOpt=%v ports=%v",
			hc.Privileged, hc.RestartPolicy.Name, hc.SecurityOpt, hc.PortBindings)
	}

	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	c, err := rt.InspectContainer(ctx, id)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if !c.Running || c.IP == "" || c.App != app || c.DeploymentID != 42 || c.Replica != 1 || c.StartedAt == nil {
		t.Errorf("unexpected container: %+v", c)
	}

	listed, err := rt.ListContainers(ctx, app)
	if err != nil || len(listed) != 1 || listed[0].ID != id || !listed[0].Running || listed[0].IP != c.IP {
		t.Errorf("ListContainers = %+v, %v", listed, err)
	}

	// nginx logs its startup to stdout/stderr; give it a moment.
	var logs []LogEntry
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if logs, err = rt.Logs(ctx, id, 50); err != nil {
			t.Fatalf("Logs: %v", err)
		}
		if len(logs) > 0 {
			break
		}
	}
	if len(logs) == 0 {
		t.Error("expected some log output")
	}
	for _, entry := range logs {
		if entry.Time.IsZero() || (entry.Stream != "stdout" && entry.Stream != "stderr") {
			t.Errorf("malformed log entry: %+v", entry)
		}
		if strings.HasPrefix(entry.Message, "20") && strings.Contains(entry.Message[:min(len(entry.Message), 31)], "T") {
			t.Errorf("timestamp was not stripped from the message: %q", entry.Message)
		}
	}

	if err := rt.StopContainer(ctx, id, 5*time.Second); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	if c, _ = rt.InspectContainer(ctx, id); c.Running || c.State != "exited" {
		t.Errorf("after stop: %+v", c)
	}
	if err := rt.StopContainer(ctx, id, time.Second); err != nil {
		t.Errorf("StopContainer must be idempotent: %v", err)
	}

	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	if err := rt.RemoveContainer(ctx, id); err != nil {
		t.Fatalf("RemoveContainer (running, forced): %v", err)
	}
	if err := rt.RemoveContainer(ctx, id); err != nil {
		t.Errorf("RemoveContainer must be idempotent: %v", err)
	}
	if _, err := rt.InspectContainer(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("inspect after remove: err = %v, want ErrNotFound", err)
	}
	if err := rt.StartContainer(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("start after remove: err = %v, want ErrNotFound", err)
	}
}

func TestIntegrationFollowLogs(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-follow-%d", time.Now().UnixNano()%1_000_000)

	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(context.Background(), id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}

	followCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan LogEntry, 100)
	done := make(chan error, 1)
	go func() {
		done <- rt.FollowLogs(followCtx, id, 100, func(e LogEntry) {
			select {
			case lines <- e:
			default:
			}
		})
	}()

	select {
	case entry := <-lines:
		if entry.Time.IsZero() || entry.Message == "" {
			t.Errorf("malformed streamed entry: %+v", entry)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no log line streamed")
	}

	// Removing the container, as a new deployment does, must end the stream
	// on its own, without an error and without cancellation.
	if err := rt.RemoveContainer(ctx, id); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("FollowLogs after container removal: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("FollowLogs did not return after its container was removed")
	}
}

func TestIntegrationStats(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-stats-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage, MemoryBytes: 64 << 20})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(context.Background(), id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}

	start := time.Now()
	one, err := rt.Stats(ctx, id, false)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("a single sample took %s; it must not wait for a second one", elapsed)
	}
	if one.Read.IsZero() || one.SystemCPU == 0 || one.OnlineCPUs == 0 || one.MemoryBytes <= 0 || one.MemoryBytes > 64<<20 || one.Previous != nil {
		t.Errorf("unexpected single sample: %+v", one)
	}

	both, err := rt.Stats(ctx, id, true)
	if err != nil {
		t.Fatalf("Stats with previous: %v", err)
	}
	if both.Previous == nil || !both.Read.After(both.Previous.Read) {
		t.Fatalf("expected two samples in order: %+v", both)
	}
	if cpu := CPUPercent(*both.Previous, both); cpu < 0 || cpu > 100*float64(both.OnlineCPUs) {
		t.Errorf("cpu = %v%%, out of range for %d CPUs", cpu, both.OnlineCPUs)
	}
	// And our own delta between two single samples agrees in kind.
	if cpu := CPUPercent(one, both); cpu < 0 || cpu > 100*float64(both.OnlineCPUs) {
		t.Errorf("cpu across separate samples = %v%%", cpu)
	}

	rt.StopContainer(ctx, id, 2*time.Second)
	stopped, err := rt.Stats(ctx, id, false)
	if err != nil {
		t.Fatalf("Stats of a stopped container: %v", err)
	}
	if !stopped.Read.IsZero() && stopped.MemoryBytes != 0 {
		t.Errorf("a stopped container should report no usage: %+v", stopped)
	}
}

func TestIntegrationPullMissingImageFails(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	if err := rt.PullImage(ctx, "shipwick/does-not-exist:nope"); err == nil {
		t.Error("expected an error pulling a missing image")
	}
}

// execIn runs a command inside a container and returns what it printed. Test
// code only: the agent itself never executes anything in a container.
func execIn(t *testing.T, ctx context.Context, rt *Runtime, id string, cmd ...string) string {
	t.Helper()
	created, err := rt.cli.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true, TTY: true})
	if err != nil {
		t.Fatalf("exec create %v: %v", cmd, err)
	}
	attached, err := rt.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		t.Fatalf("exec attach %v: %v", cmd, err)
	}
	defer attached.Close()
	out, _ := io.ReadAll(attached.Reader)
	return string(out)
}

func TestIntegrationServiceNames(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	suffix := time.Now().UnixNano() % 1_000_000
	start := func(app string) string {
		t.Helper()
		id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage})
		if err != nil {
			t.Fatalf("CreateContainer %s: %v", app, err)
		}
		t.Cleanup(func() { rt.RemoveContainer(context.Background(), id) })
		if err := rt.StartContainer(ctx, id); err != nil {
			t.Fatalf("StartContainer %s: %v", app, err)
		}
		return id
	}
	orders := start(fmt.Sprintf("orders-%d", suffix))
	caller := start(fmt.Sprintf("caller-%d", suffix))
	name := fmt.Sprintf("orders-%d", suffix)
	resolves := func() bool {
		return strings.Contains(execIn(t, ctx, rt, caller, "getent", "hosts", name), name)
	}

	born, err := rt.InspectContainer(ctx, orders)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if !born.OnServicesNetwork || len(born.ServiceNames) != 0 || born.IP == "" {
		t.Fatalf("a replica is born on both networks, nameless: %+v", born)
	}
	if resolves() {
		t.Fatal("a replica must not be findable under its application's name before it is given that name")
	}

	if err := rt.SetServiceNames(ctx, orders, []string{name, name + "-8080"}); err != nil {
		t.Fatalf("SetServiceNames: %v", err)
	}
	named, err := rt.InspectContainer(ctx, orders)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if len(named.ServiceNames) != 2 {
		t.Errorf("ServiceNames = %v, want both names", named.ServiceNames)
	}
	// Its address on the main network — the one its database connections and
	// the agent's health probes use — must not move.
	if named.IP != born.IP {
		t.Errorf("address on the main network changed: %s → %s", born.IP, named.IP)
	}
	if !resolves() {
		t.Error("the application's name does not resolve after SetServiceNames")
	}

	// Asking again for what it already has must work: it is how the agent
	// repairs a replica after a half-finished attempt.
	if err := rt.SetServiceNames(ctx, orders, []string{name}); err != nil {
		t.Fatalf("SetServiceNames again: %v", err)
	}

	if err := rt.SetServiceNames(ctx, orders, nil); err != nil {
		t.Fatalf("SetServiceNames(none): %v", err)
	}
	if resolves() {
		t.Error("the name still resolves after it was taken away")
	}
	if c, _ := rt.InspectContainer(ctx, orders); !c.OnServicesNetwork || len(c.ServiceNames) != 0 {
		t.Errorf("without names the replica stays on the services network: %+v", c)
	}

	// A stopped container leaves DNS by itself, names or not; that is what
	// makes retiring a replica a plain stop.
	if err := rt.SetServiceNames(ctx, orders, []string{name}); err != nil {
		t.Fatal(err)
	}
	if err := rt.StopContainer(ctx, orders, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if resolves() {
		t.Error("a stopped replica is still resolvable")
	}
	// ...and names can be changed while it is stopped, so that a restarted
	// replica does not come back findable before it is ready.
	if err := rt.SetServiceNames(ctx, orders, nil); err != nil {
		t.Fatalf("SetServiceNames on a stopped container: %v", err)
	}
	if err := rt.StartContainer(ctx, orders); err != nil {
		t.Fatal(err)
	}
	if resolves() {
		t.Error("a restarted replica came back under its name although the name was taken away while it was stopped")
	}
}
