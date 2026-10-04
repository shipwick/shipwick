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

// What the supervisor relies on after a reboot or a kill: a container that
// was ended from outside is still there, says how it ended, and starts again
// as the same container — with what it had written to its own filesystem.
func TestIntegrationAContainerKilledFromOutsideIsStartedAgainAsItWas(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	app := fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage,
		Entrypoint: []string{"sh", "-c", "date >> /starts; sleep 600"},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(context.Background(), id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}

	if _, err := rt.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if _, err := rt.WaitContainer(ctx, id); err != nil {
		t.Fatalf("WaitContainer: %v", err)
	}
	c, err := rt.InspectContainer(ctx, id)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if c.Running || c.State != "exited" || c.ExitCode != 137 || c.FinishedAt == nil {
		t.Fatalf("after the kill: running=%v state=%s exit=%d finished=%v; want an exited container that says so", c.Running, c.State, c.ExitCode, c.FinishedAt)
	}
	listed, err := rt.ListContainers(ctx, app)
	if err != nil || len(listed) != 1 || listed[0].ID != id || listed[0].Running {
		t.Fatalf("ListContainers = %+v, %v; want the killed container, not running", listed, err)
	}

	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer after the kill: %v", err)
	}
	code, out, err := rt.Exec(ctx, id, []string{"wc", "-l", "/starts"}, 10*time.Second)
	if err != nil || code != 0 {
		t.Fatalf("exec: %d %q %v", code, out, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "2 ") {
		t.Errorf("the file the container keeps has %q lines, want 2: one per start, in the same filesystem", out)
	}
}

// A pull that its caller gives up on ends at once and leaves the daemon able
// to pull the same image again: what a deployment's timeout and an agent's
// restart both rely on.
func TestIntegrationAPullThatIsAbandonedCanBeDoneAgain(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	const image = "busybox:1.36.1"
	// An image that was here before stays here afterwards.
	if had, err := rt.ImageExists(ctx, image); err != nil {
		t.Fatalf("ImageExists: %v", err)
	} else if !had {
		t.Cleanup(func() { rt.cli.ImageRemove(context.Background(), image, client.ImageRemoveOptions{}) })
	}

	abandoned, cancel := context.WithCancel(ctx)
	cancel()
	started := time.Now()
	err := rt.PullImage(abandoned, image, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("an abandoned pull: err = %v, want the cancellation", err)
	}
	if IsUnavailable(err) {
		t.Errorf("an abandoned pull is taken for a daemon that does not answer: %v", err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("an abandoned pull took %s to return", took)
	}

	if err := rt.PullImage(ctx, image, nil); err != nil {
		t.Fatalf("the pull, done again: %v", err)
	}
	if ok, err := rt.ImageExists(ctx, image); err != nil || !ok {
		t.Errorf("ImageExists after the second pull = %v, %v", ok, err)
	}
}
