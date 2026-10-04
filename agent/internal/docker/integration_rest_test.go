//go:build integration

package docker

import (
	"fmt"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// What the rest of addresses rests on: Docker gives the address of a
// container that stopped to the next one that starts, and a container of
// another application is made to wait for it.
func TestIntegrationAnAddressRestsBetweenApplications(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	const rest = 3 * time.Second
	rt.rest = newAddressRest(rest)
	rt.rest.since = time.Now().Add(-time.Hour)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	suffix := time.Now().UnixNano() % 1_000_000
	alpha, beta := fmt.Sprintf("it-rest-a-%d", suffix), fmt.Sprintf("it-rest-b-%d", suffix)

	create := func(app string, replica int) string {
		t.Helper()
		id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: replica, Image: testImage})
		if err != nil {
			t.Fatalf("CreateContainer: %v", err)
		}
		t.Cleanup(func() { rt.RemoveContainer(ctx, id) })
		return id
	}
	servicesAddress := func(id string) string {
		t.Helper()
		res, err := rt.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		ep := res.Container.NetworkSettings.Networks[rt.services]
		if ep == nil || !ep.IPAddress.IsValid() {
			t.Fatalf("%s has no address on %s", id, rt.services)
		}
		return ep.IPAddress.String()
	}
	// die stops a container behind the runtime's back, as a crash would.
	die := func(id string) time.Time {
		t.Helper()
		now := 0
		if _, err := rt.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &now}); err != nil {
			t.Fatalf("stop: %v", err)
		}
		c, err := rt.InspectContainer(ctx, id)
		if err != nil || c.Running || c.FinishedAt == nil {
			t.Fatalf("a stopped container says when it stopped: %+v, %v", c, err)
		}
		return *c.FinishedAt
	}

	first := create(alpha, 1)
	if err := rt.StartContainer(ctx, first); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	address := servicesAddress(first)
	stopped := die(first)

	// Another application's container waits for the address to have rested.
	other := create(beta, 1)
	if err := rt.StartContainer(ctx, other); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	if started := time.Now(); started.Before(stopped.Add(rest)) {
		t.Errorf("%s started %v after %s stopped, before its address had rested %v", beta, started.Sub(stopped), alpha, rest)
	}
	// Why it had to: this is the address the proxy held for alpha.
	if got := servicesAddress(other); got != address {
		t.Logf("%s got %s, not the %s that %s gave up: this daemon does not hand out the lowest free address", beta, got, address, alpha)
	}

	// The application's own next container does not wait for it.
	stopped = die(other)
	own := create(beta, 2)
	if err := rt.StartContainer(ctx, own); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	if started := time.Now(); !started.Before(stopped.Add(rest)) {
		t.Errorf("%s waited %v for the address of its own container", beta, started.Sub(stopped))
	}
}
