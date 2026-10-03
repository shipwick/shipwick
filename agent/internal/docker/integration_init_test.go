//go:build integration

package docker

import (
	"fmt"
	"testing"
	"time"
)

// `sleep` has no handler for SIGTERM: as PID 1 it never sees the signal and
// is killed when the grace period ends; behind an init process the signal
// ends it. Which signal that is belongs to the image (nginx asks for SIGQUIT),
// so the exit code is only held to not being the kill's.
func TestIntegrationInitPassesSIGTERMOn(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	const grace = 3 * time.Second
	const killed = 137
	for i, tc := range []struct{ init bool }{{false}, {true}} {
		id, _, err := rt.CreateContainer(ctx, ContainerSpec{
			App: app, DeploymentID: 1, Sequence: 1, Replica: i + 1, Image: testImage,
			Entrypoint: []string{"sleep"}, Command: []string{"300"}, Init: tc.init,
		})
		if err != nil {
			t.Fatalf("CreateContainer: %v", err)
		}
		t.Cleanup(func() { rt.RemoveContainer(ctx, id) })
		if err := rt.StartContainer(ctx, id); err != nil {
			t.Fatalf("StartContainer: %v", err)
		}
		if top := execIn(t, ctx, rt, id, "cat", "/proc/1/cmdline"); (top[:5] == "sleep") == tc.init {
			t.Errorf("init = %v, but PID 1 is %q", tc.init, top)
		}

		began := time.Now()
		if err := rt.StopContainer(ctx, id, grace); err != nil {
			t.Fatalf("StopContainer: %v", err)
		}
		took := time.Since(began)
		c, err := rt.InspectContainer(ctx, id)
		if err != nil {
			t.Fatalf("InspectContainer: %v", err)
		}
		if (c.ExitCode == killed) == tc.init {
			t.Errorf("init = %v: exit code %d", tc.init, c.ExitCode)
		}
		if tc.init && took >= grace {
			t.Errorf("with an init process the stop took %s, the whole grace period", took)
		}
		if !tc.init && took < grace {
			t.Errorf("without one the stop took %s, less than the grace period: the process was not PID 1 after all", took)
		}
	}
}
