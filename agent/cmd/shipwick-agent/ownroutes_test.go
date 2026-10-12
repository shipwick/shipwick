package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/proxy"
)

func TestTheAgentsOwnHostnamesCarryStrictTransportSecurity(t *testing.T) {
	raw, _, err := proxy.Build("unix//run/caddy/admin.sock", []proxy.Route{
		ownRoute("agent.example.com", "172.18.0.2:9000"),
		ownRoute("dash.example.com", "dashboard:3000"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var p struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []struct{ Host []string }
						Handle json.RawMessage
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	const header = `"set":{"Strict-Transport-Security":["max-age=31536000"]}`
	seen := map[string]bool{}
	for _, srv := range p.Apps.HTTP.Servers {
		for _, r := range srv.Routes {
			for _, m := range r.Match {
				for _, host := range m.Host {
					seen[host] = true
					if !strings.Contains(string(r.Handle), header) {
						t.Errorf("%s: the route sets no Strict-Transport-Security:\n%s", host, r.Handle)
					}
					if !strings.Contains(string(r.Handle), `"flush_interval":-1`) {
						t.Errorf("%s: the route no longer streams:\n%s", host, r.Handle)
					}
				}
			}
		}
	}
	if !seen["agent.example.com"] || !seen["dash.example.com"] {
		t.Errorf("routes for %v, want both hostnames", seen)
	}
}
