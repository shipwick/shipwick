package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// published is a single-replica recreate application that publishes ports.
func published(name, image string, ports ...spec.Publish) spec.App {
	a := app(name, image, 1)
	a.Deploy.Strategy = spec.StrategyRecreate
	a.Publish = ports
	return a
}

func tcp(container, host int) spec.Publish {
	return spec.Publish{Port: container, Host: host, Protocol: spec.ProtocolTCP}
}

func TestPublishedPortsReachTheContainer(t *testing.T) {
	h := newHarness(t)
	bound := spec.Publish{Port: 5432, Host: 15432, Address: "10.0.0.5", Protocol: spec.ProtocolTCP}
	d := h.deploy(published("db", "postgres:17", tcp(5432, 5432), bound))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}

	containers := h.rt.Containers()
	if len(containers) != 1 {
		t.Fatalf("got %d containers, want 1", len(containers))
	}
	want := []docker.PortBinding{
		{Port: 5432, HostPort: 5432, Protocol: "tcp"},
		{Port: 5432, HostPort: 15432, Address: "10.0.0.5", Protocol: "tcp"},
	}
	got := h.rt.Spec(containers[0].ID).Publish
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("bindings = %+v, want %+v", got, want)
	}

	// The next version binds the same ports: it comes to exist the same way.
	v2 := h.deploy(published("db", "postgres:18", tcp(5432, 5432), bound))
	if v2.Status != api.StatusActive {
		t.Fatalf("v2: %s (%s)", v2.Status, v2.Error)
	}
	if got := h.rt.Spec(h.rt.Containers()[0].ID).Publish; len(got) != 2 {
		t.Errorf("v2 bindings = %+v", got)
	}
}

func TestPublishedPortConflict(t *testing.T) {
	h := newHarness(t)
	h.deploy(published("db", "postgres:17", tcp(5432, 5432)))

	_, err := h.engine.Deploy(context.Background(), published("other", "other:1.0", tcp(8080, 8080), tcp(9000, 5432)))
	var conflict *PortConflictError
	if !errors.As(err, &conflict) || conflict.Owner != `application "db"` || conflict.Field() != "publish[1].host" {
		t.Fatalf("err = %v, want a PortConflictError naming the owner and the entry", err)
	}
	if _, err := h.store.GetApplication(context.Background(), "other"); err == nil {
		t.Error("a refused deployment must leave no trace")
	}

	// Another protocol is another port.
	udp := spec.Publish{Port: 5432, Host: 5432, Protocol: spec.ProtocolUDP}
	if d := h.deploy(published("other", "other:1.0", udp)); d.Status != api.StatusActive {
		t.Errorf("5432/udp next to 5432/tcp: %s %q", d.Status, d.Error)
	}

	// The owner may deploy again, and may give the port up.
	if d := h.deploy(published("db", "postgres:18", tcp(5432, 5432))); d.Status != api.StatusActive {
		t.Errorf("redeploying the owner: %s %q", d.Status, d.Error)
	}
	h.deploy(published("db", "postgres:18", tcp(5432, 15432)))
	if d := h.deploy(published("third", "third:1.0", tcp(5432, 5432))); d.Status != api.StatusActive {
		t.Errorf("the port was given up, so it should be free now: %s %q", d.Status, d.Error)
	}
}

func TestPublishedPortOnEveryAddressCollidesWithOneAddress(t *testing.T) {
	on := func(host int, address string) spec.Publish {
		return spec.Publish{Port: 6379, Host: host, Address: address, Protocol: spec.ProtocolTCP}
	}
	for _, tt := range []struct {
		name         string
		first, then  spec.Publish
		wantConflict bool
	}{
		{"all, then one address", on(6379, ""), on(6379, "10.0.0.5"), true},
		{"one address, then all", on(6379, "10.0.0.5"), on(6379, ""), true},
		{"same address twice", on(6379, "10.0.0.5"), on(6379, "10.0.0.5"), true},
		{"two addresses", on(6379, "10.0.0.5"), on(6379, "10.0.0.6"), false},
		{"two ports", on(6379, ""), on(6380, ""), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.deploy(published("cache", "redis:7", tt.first))
			_, err := h.engine.Deploy(context.Background(), published("other", "other:1.0", tt.then))
			var conflict *PortConflictError
			if errors.As(err, &conflict) != tt.wantConflict {
				t.Errorf("Deploy = %v, want conflict %v", err, tt.wantConflict)
			}
			h.engine.Wait()
		})
	}
}

func TestShipwicksOwnPortsCannotBePublished(t *testing.T) {
	h := newHarness(t)
	h.engine.opts.ReservedHostPorts = []int{80, 443, 9000}

	_, err := h.engine.Deploy(context.Background(), published("api", "api:1.0", tcp(8080, 9000)))
	var conflict *PortConflictError
	if !errors.As(err, &conflict) || conflict.Owner != "Shipwick itself (the agent or the proxy)" || conflict.Port != 9000 {
		t.Fatalf("err = %v; an application must not be able to take the agent's port", err)
	}
	if d := h.deploy(published("api", "api:1.0", tcp(8080, 9001))); d.Status != api.StatusActive {
		t.Errorf("a free port: %s %q", d.Status, d.Error)
	}
}
