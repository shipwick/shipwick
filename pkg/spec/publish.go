package spec

import (
	"fmt"
	"net"
	"slices"
	"strings"
)

// MaxPublish bounds the ports one application publishes on the server.
const MaxPublish = 20

// Protocols a published port can use.
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// proxyPorts are the server ports the reverse proxy listens on. The agent
// refuses its own ports as well; only it knows them.
var proxyPorts = []int{80, 443}

// validatePublish checks the host ports the application publishes. It runs
// after the strategy and the replica count are settled: a host port cannot be
// shared, so publishing requires recreate with one replica.
func (r raw) validatePublish(verr *ValidationError, app App) []Publish {
	if len(r.Publish) == 0 {
		return nil
	}
	if len(r.Publish) > MaxPublish {
		verr.add("publish", fmt.Sprintf("too many (%d)", len(r.Publish)), fmt.Sprintf("at most %d", MaxPublish))
		return nil
	}
	// A server port has one holder: not two replicas, and not the old and the
	// new version side by side during a rolling deployment. Volumes ask for
	// the same thing; one complaint per field is enough.
	if len(app.Volumes) == 0 {
		if app.Deploy.Strategy != StrategyRecreate {
			verr.add("deploy.strategy", "must be \"recreate\" for an application that publishes ports: two versions cannot listen on the same server port",
				"deploy:\n    strategy: recreate")
		}
		if app.Replicas != 1 {
			verr.add("replicas", fmt.Sprintf("must be 1 for an application that publishes ports, got %d: replicas cannot share a server port", app.Replicas), "1")
		}
	}

	out := make([]Publish, 0, len(r.Publish))
	seen := map[Publish]bool{} // by host port, protocol and address
	for i, p := range r.Publish {
		field := fmt.Sprintf("publish[%d]", i)
		pub := Publish{Protocol: ProtocolTCP}
		switch {
		case p.Port == nil:
			verr.add(field+".port", "is required", "the port your application listens on, e.g. 5432")
		case *p.Port < 1 || *p.Port > 65535:
			verr.add(field+".port", fmt.Sprintf("invalid value %d", *p.Port), "a number between 1 and 65535")
		default:
			pub.Port = *p.Port
		}

		pub.Host = pub.Port
		if p.Host != nil {
			pub.Host = *p.Host
		}
		switch {
		case p.Host != nil && (pub.Host < 1 || pub.Host > 65535):
			verr.add(field+".host", fmt.Sprintf("invalid value %d", pub.Host), "a number between 1 and 65535")
			pub.Host = 0
		case slices.Contains(proxyPorts, pub.Host):
			verr.add(field+".host", fmt.Sprintf("invalid value %d: the proxy listens there", pub.Host), "another server port, e.g. 15432")
		}

		if proto := strings.ToLower(strings.TrimSpace(p.Protocol)); proto != "" {
			switch proto {
			case ProtocolTCP, ProtocolUDP:
				pub.Protocol = proto
			default:
				verr.add(field+".protocol", fmt.Sprintf("invalid value %q", p.Protocol), "tcp, udp")
			}
		}

		if addr := strings.TrimSpace(p.Address); addr != "" {
			ip := net.ParseIP(addr)
			if ip == nil {
				verr.add(field+".address", fmt.Sprintf("invalid value %q", p.Address), "an address of the server, e.g. 10.0.0.5")
			} else {
				pub.Address = ip.String()
			}
		}

		key := Publish{Host: pub.Host, Address: pub.Address, Protocol: pub.Protocol}
		if pub.Host != 0 && seen[key] {
			verr.add(field+".host", fmt.Sprintf("server port %d/%s is published twice", pub.Host, pub.Protocol), "a different server port for each entry")
		}
		seen[key] = true
		out = append(out, pub)
	}
	return out
}
