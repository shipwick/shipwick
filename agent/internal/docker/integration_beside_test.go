//go:build integration

package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// A command started inside a running container outlives whoever waits for
// it; the same command in a container beside it is stopped like any other
// container, sees the first one as localhost and writes the same volume.
func TestIntegrationACommandBesideAContainerCanBeEndedAndOneInsideItCannot(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	mounts := []Mount{{Volume: "data", Path: "/data"}}
	replica, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage, Mounts: mounts})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		rt.RemoveContainer(cleanup, replica)
		rt.RemoveVolume(cleanup, app, "data")
	})
	if err := rt.StartContainer(ctx, replica); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	fetch := []string{"wget", "-q", "-O", "/data/page.html", "http://localhost:80/"}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if code, _, err := rt.Exec(ctx, replica, fetch, 5*time.Second); err == nil && code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nginx did not start listening")
		}
	}
	execIn(t, ctx, rt, replica, "rm", "/data/page.html")

	beside := func(run int64, cmd ...string) string {
		t.Helper()
		id, name, err := rt.CreateContainer(ctx, ContainerSpec{
			App: app, DeploymentID: 1, Sequence: 1, Image: testImage, Mounts: mounts, Command: cmd, Init: true,
			Job: &JobSpec{Name: "backup_before", RunID: run}, Beside: &BesideSpec{Container: replica},
		})
		if err != nil {
			t.Fatalf("CreateContainer beside: %v", err)
		}
		t.Cleanup(func() { rt.RemoveContainer(context.WithoutCancel(ctx), id) })
		if want := fmt.Sprintf("shipwick_%s_job_backup_before_%d", app, run); name != want {
			t.Errorf("name = %q, want %q", name, want)
		}
		if err := rt.StartContainer(ctx, id); err != nil {
			t.Fatalf("StartContainer beside: %v", err)
		}
		return id
	}

	// localhost is the replica, and the volume is the replica's.
	id := beside(1, fetch...)
	if code, err := rt.WaitContainer(ctx, id); err != nil || code != 0 {
		logs, _ := rt.Logs(ctx, id, 20)
		t.Fatalf("the command beside the replica: exit %d, err = %v, output = %+v", code, err, logs)
	}
	if page := execIn(t, ctx, rt, replica, "cat", "/data/page.html"); !strings.Contains(page, "nginx") {
		t.Errorf("the replica does not see what was written beside it: %q", page)
	}
	// The command ran as it is, not behind the image's entrypoint.
	in, err := rt.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Container.Path != "wget" || len(in.Container.NetworkSettings.Networks) != 0 {
		t.Errorf("path = %q, networks = %v; want the bare command and no network of its own", in.Container.Path, in.Container.NetworkSettings.Networks)
	}
	if c, err := rt.InspectContainer(ctx, id); err != nil || c.Job != "backup_before" || c.App != app {
		t.Errorf("container = %+v, err = %v", c, err)
	}

	// One that does not finish is stopped, well within the grace period.
	const grace = 5 * time.Second
	id = beside(2, "sleep", "300")
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	_, err = rt.WaitContainer(waitCtx, id)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContainer = %v, want the deadline", err)
	}
	began := time.Now()
	if err := rt.StopContainer(ctx, id, grace); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	if took := time.Since(began); took >= grace {
		t.Errorf("the stop took %s, the whole grace period", took)
	}
	if c, err := rt.InspectContainer(ctx, id); err != nil || c.Running {
		t.Errorf("after the stop: %+v, err = %v", c, err)
	}
	if c, err := rt.InspectContainer(ctx, replica); err != nil || !c.Running {
		t.Errorf("the replica after the stop: %+v, err = %v", c, err)
	}

	// The same command inside the replica is still there when Exec has given
	// up on it: the daemon offers nothing to end it with.
	if _, _, err := rt.Exec(ctx, replica, []string{"sleep", "301"}, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec = %v, want the deadline", err)
	}
	if ps := execIn(t, ctx, rt, replica, "ps", "-o", "args"); !strings.Contains(ps, "sleep 301") {
		t.Errorf("the command inside the replica is gone; if the daemon ends it now, before_in: replica can be ended as well:\n%s", ps)
	}
}
