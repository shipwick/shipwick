//go:build integration

package docker

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func removeControl(t *testing.T, rt *Runtime) {
	t.Helper()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rt.cli.NetworkRemove(cleanup, ControlNetwork(rt.network), client.NetworkRemoveOptions{})
	})
}

func TestIntegrationTheControlNetworkAndWhereApplicationsCallFrom(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}

	// Before the network exists there is nothing to say, and no error.
	if reach, err := rt.Reach(ctx, 9000); err != nil || reach.Control.IsValid() || reach.InContainer {
		t.Fatalf("Reach without a control network = %+v, %v", reach, err)
	}
	removeControl(t, rt)
	if err := rt.EnsureControl(ctx); err != nil {
		t.Fatalf("EnsureControl: %v", err)
	}
	if err := rt.EnsureControl(ctx); err != nil {
		t.Fatalf("EnsureControl must be idempotent: %v", err)
	}
	// The tests are a process of the host: the address is the bridge's own.
	reach, err := rt.Reach(ctx, 9000)
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if reach.InContainer || !reach.Control.IsValid() || len(reach.ControlSubnets) == 0 || !reach.ControlSubnets[0].Contains(reach.Control) {
		t.Fatalf("Reach = %+v; want the control network's gateway and its subnet", reach)
	}

	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	app := fmt.Sprintf("it-reach-%d", time.Now().UnixNano()%1_000_000)
	id, _, err := rt.CreateContainer(ctx, ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(ctx, id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	res, err := rt.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	var mine []netip.Addr
	for _, name := range []string{rt.network, rt.services} {
		ep := res.Container.NetworkSettings.Networks[name]
		if ep == nil || !ep.IPAddress.IsValid() {
			t.Fatalf("the replica has no address on %s", name)
		}
		mine = append(mine, ep.IPAddress)
	}

	origins, err := rt.Origins(ctx)
	if err != nil {
		t.Fatalf("Origins: %v", err)
	}
	if len(origins.Subnets) != 2 {
		t.Fatalf("Subnets = %v, want those of the two application networks", origins.Subnets)
	}
	for i, a := range mine {
		if !slices.Contains(origins.Containers, a) {
			t.Errorf("Containers = %v, without the replica's address %s", origins.Containers, a)
		}
		if !origins.Subnets[i].Contains(a) {
			t.Errorf("Subnets[%d] = %s does not contain the replica's address %s", i, origins.Subnets[i], a)
		}
	}
	for _, s := range origins.Subnets {
		if s.Contains(reach.Control) {
			t.Errorf("the application subnet %s contains the control network's address %s", s, reach.Control)
		}
	}

	if err := rt.StopContainer(ctx, id, time.Second); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	origins, err = rt.Origins(ctx)
	if err != nil {
		t.Fatalf("Origins: %v", err)
	}
	for _, a := range mine {
		if slices.Contains(origins.Containers, a) {
			t.Errorf("Containers = %v, still with the address %s of a stopped replica: the next holder would be refused", origins.Containers, a)
		}
	}
}

// What listening on the control network alone rests on when the API's port
// is published: Docker forwards a published port to the network the default
// gateway comes from, and gatewayNetwork has to name the same one.
func TestIntegrationTheGatewayNetworkIsTheOneDockerChooses(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	removeControl(t, rt)
	if err := rt.EnsureControl(ctx); err != nil {
		t.Fatalf("EnsureControl: %v", err)
	}
	if err := rt.PullImage(ctx, testImage, nil); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	control := ControlNetwork(rt.network)
	name := fmt.Sprintf("shipwick-it-reach-%d", time.Now().UnixNano()%1_000_000)
	created, err := rt.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name,
		Config:           &container.Config{Image: testImage},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{control: {}}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(ctx, created.ID) })
	// As the agent joins the application network itself.
	if _, err := rt.cli.NetworkConnect(ctx, rt.network, client.NetworkConnectOptions{Container: created.ID,
		EndpointConfig: &network.EndpointSettings{GwPriority: selfGwPriority}}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := rt.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := rt.cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	networks := res.Container.NetworkSettings.Networks

	route := execIn(t, ctx, rt, created.ID, "ip", "route", "show", "default")
	chosen := ""
	for n, ep := range networks {
		if ep.Gateway.IsValid() && strings.Contains(route, "via "+ep.Gateway.String()+" ") {
			chosen = n
		}
	}
	if chosen == "" {
		t.Fatalf("no network of the container has the gateway of its default route %q", route)
	}
	if got := gatewayNetwork(networks); got != chosen {
		t.Errorf("gatewayNetwork = %s, but Docker took the default gateway from %s (%q)", got, chosen, strings.TrimSpace(route))
	}
	// Daemons before 28 do not know the priority and go by name.
	if networks[rt.network].GwPriority == selfGwPriority && chosen != control {
		t.Errorf("the daemon kept the priority and still took the gateway from %s, not from the control network", chosen)
	}
}
