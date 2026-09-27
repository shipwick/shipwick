package deploy

import (
	"context"
	"fmt"
	"slices"

	"github.com/shipwick/shipwick/pkg/spec"
)

// PortConflictError is returned when a deployment publishes a server port
// that something else already holds.
type PortConflictError struct {
	Index    int // the publish entry in deploy.yaml
	Port     int // on the server
	Protocol string
	Owner    string // an application, or Shipwick itself
}

func (e *PortConflictError) Error() string {
	return fmt.Sprintf("server port %d/%s is already published by %s", e.Port, e.Protocol, e.Owner)
}

// Field is the deploy.yaml field to change.
func (e *PortConflictError) Field() string {
	return fmt.Sprintf("publish[%d].host", e.Index)
}

// checkPublish refuses a server port that is already taken: by the agent or
// the proxy, or by another application's active configuration. Docker would
// refuse the second bind too, but only when the container starts — for a
// recreate deployment that is after the old version has been stopped, so a
// mistake that was visible up front would cost an outage and a rollback.
func (e *Engine) checkPublish(ctx context.Context, appName string, publish []spec.Publish) error {
	if len(publish) == 0 {
		return nil
	}
	for i, p := range publish {
		if slices.Contains(e.opts.ReservedHostPorts, p.Host) {
			return &PortConflictError{Index: i, Port: p.Host, Protocol: p.Protocol, Owner: "Shipwick itself (the agent or the proxy)"}
		}
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return err
	}
	for _, other := range apps {
		if other.Name == appName || other.ActiveDeploymentID == nil {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *other.ActiveDeploymentID)
		if err != nil {
			return err
		}
		for i, p := range publish {
			for _, q := range d.Spec.Publish {
				if bindingsCollide(p, q) {
					return &PortConflictError{Index: i, Port: p.Host, Protocol: p.Protocol, Owner: fmt.Sprintf("application %q", other.Name)}
				}
			}
		}
	}
	return nil
}

// bindingsCollide reports whether two published ports cannot both be bound:
// the same server port and protocol, on the same address or with one of them
// on every address.
func bindingsCollide(a, b spec.Publish) bool {
	return a.Host == b.Host && a.Protocol == b.Protocol &&
		(a.Address == "" || b.Address == "" || a.Address == b.Address)
}
