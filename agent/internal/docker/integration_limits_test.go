//go:build integration

package docker

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// What the daemon says about its limits is held against what a container
// finds in its own cgroup. On a daemon with the controllers the limit is
// there; on one without — rootless Docker that was delegated none — the
// container is created all the same and nothing is: which is the one case
// Info.Unenforced exists for, and the test passes on both.
func TestIntegrationTheDaemonSaysWhichLimitsItEnforces(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	info, err := rt.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	const memory = 32 << 20
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage,
		Entrypoint: []string{"sleep"}, Command: []string{"300"},
		MemoryBytes: memory, NanoCPUs: 500_000_000,
	})
	if err != nil {
		t.Fatalf("CreateContainer with limits (unenforced: %v): %v", info.Unenforced, err)
	}
	t.Cleanup(func() { rt.RemoveContainer(ctx, id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}

	// cgroup v2; a daemon on v1 keeps the limit elsewhere and is not judged.
	exit, out, err := rt.Exec(ctx, id, []string{"cat", "/sys/fs/cgroup/memory.max"}, 10*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := strings.TrimSpace(out)
	enforced := !slices.Contains(info.Unenforced, LimitMemory)
	switch {
	case exit != 0 && enforced:
		t.Skipf("no memory.max in the container (%s): not cgroup v2", got)
	case enforced && got != fmt.Sprint(memory):
		t.Errorf("the daemon says it enforces memory limits, and memory.max is %q, not %d", got, memory)
	case !enforced && got == fmt.Sprint(memory):
		t.Errorf("the daemon says it does not enforce memory limits (rootless: %v), and memory.max is %s", info.Rootless, got)
	}
	t.Logf("rootless %v, unenforced %v, memory.max %s", info.Rootless, info.Unenforced, got)
}
