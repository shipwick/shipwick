//go:build integration

package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// A replica the runtime stops itself rests only until the proxy has forgotten
// it, although Docker reports it as exited from then on; without the proxy's
// word it rests in full.
func TestIntegrationAnAddressTheProxyForgotDoesNotRestInFull(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	const rest = 4 * time.Second
	rt.rest = newAddressRest(rest)
	rt.rest.since = time.Now().Add(-time.Hour)
	var mu sync.Mutex
	var asked [][]string
	confirms := true
	rt.ForgetWith(func(_ context.Context, names []string) error {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, names)
		if !confirms {
			return errors.New("the proxy did not forget (HTTP 404)")
		}
		return nil
	})
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	suffix := time.Now().UnixNano() % 1_000_000
	alpha, beta := fmt.Sprintf("it-forget-a-%d", suffix), fmt.Sprintf("it-forget-b-%d", suffix)
	names := []string{alpha, alpha + "_80"}

	started := func(app string, replica int) string {
		t.Helper()
		id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: replica, Image: testImage})
		if err != nil {
			t.Fatalf("CreateContainer: %v", err)
		}
		t.Cleanup(func() { rt.RemoveContainer(ctx, id) })
		if err := rt.StartContainer(ctx, id); err != nil {
			t.Fatalf("StartContainer: %v", err)
		}
		return id
	}
	// stop retires a replica of alpha that carries its names, and returns how
	// long a container of beta then takes to start.
	stop := func(replica int) time.Duration {
		t.Helper()
		id := started(alpha, replica)
		if err := rt.SetServiceNames(ctx, id, names); err != nil {
			t.Fatalf("SetServiceNames: %v", err)
		}
		// Whatever the calls above left resting is over before the stop.
		if err := rt.rest.wait(ctx, "", rt.exitedByApp); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		asked = nil
		mu.Unlock()
		if err := rt.StopContainer(ctx, id, time.Second); err != nil {
			t.Fatalf("StopContainer: %v", err)
		}
		mu.Lock()
		if len(asked) != 1 || !slices.Equal(asked[0], names) {
			t.Errorf("the proxy was asked to forget %v, want %v once", asked, names)
		}
		mu.Unlock()
		stopped := time.Now()
		started(beta, replica)
		return time.Since(stopped)
	}

	if took := stop(1); took >= rest {
		t.Errorf("%s started %v after %s was stopped and forgotten: its address rested in full", beta, took, alpha)
	} else if took < ForgottenRest {
		t.Errorf("%s started %v after %s was stopped, before a request on its way there had given up (%v)", beta, took, alpha, ForgottenRest)
	}

	mu.Lock()
	confirms = false
	mu.Unlock()
	if took := stop(2); took < rest {
		t.Errorf("%s started %v after %s was stopped and not forgotten, before its address had rested %v", beta, took, alpha, rest)
	}
}

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
