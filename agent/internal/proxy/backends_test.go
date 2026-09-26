package proxy

import (
	"encoding/json"
	"testing"
)

// resolver mirrors the part of a reverse_proxy handler that says where
// upstreams come from.
type resolver struct {
	Source  string
	Name    string
	Port    string
	Refresh string
	Sources []resolver
}

func handlerOf(t *testing.T, raw []byte, domain string) (dynamic *resolver, static []string) {
	t.Helper()
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []struct{ Host []string }
						Handle []struct {
							Handler   string
							Dynamic   *resolver `json:"dynamic_upstreams"`
							Upstreams []struct{ Dial string }
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Apps.HTTP.Servers[serverName].Routes {
		if len(r.Match) == 1 && len(r.Match[0].Host) == 1 && r.Match[0].Host[0] == domain {
			h := r.Handle[0]
			for _, u := range h.Upstreams {
				static = append(static, u.Dial)
			}
			return h.Dynamic, static
		}
	}
	t.Fatalf("no route for %s", domain)
	return nil, nil
}

func TestBackendsAreResolvedByName(t *testing.T) {
	_, _, raw := build(t, []Route{{Domain: "api.example.com", Backends: []Backend{{Name: "api.8080", Port: 8080}}}})

	dynamic, static := handlerOf(t, raw, "api.example.com")
	if dynamic == nil || len(static) != 0 {
		t.Fatalf("an application is reached by name, never through a list of replicas: dynamic=%+v static=%v", dynamic, static)
	}
	if dynamic.Source != "a" || dynamic.Name != "api.8080" || dynamic.Port != "8080" || dynamic.Refresh != resolveEvery {
		t.Errorf("resolver = %+v", dynamic)
	}
}

func TestTwoPortsAreTwoSourcesOfOneRoute(t *testing.T) {
	// A rollout whose new version listens on another port: both serve.
	_, _, raw := build(t, []Route{{Domain: "api.example.com", Backends: []Backend{{Name: "api.9090", Port: 9090}, {Name: "api.8080", Port: 8080}}}})

	dynamic, _ := handlerOf(t, raw, "api.example.com")
	if dynamic == nil || dynamic.Source != "multi" || len(dynamic.Sources) != 2 {
		t.Fatalf("resolver = %+v", dynamic)
	}
	if dynamic.Sources[0].Name != "api.8080" || dynamic.Sources[1].Name != "api.9090" {
		t.Errorf("sources must be ordered, or the same routing gives two configs: %+v", dynamic.Sources)
	}
}

func TestWhoStandsBehindANameIsNotPartOfTheConfig(t *testing.T) {
	// The whole point: replicas come and go behind a name without the config
	// changing, because every changed config is a reload.
	route := []Route{{Domain: "api.example.com", Backends: []Backend{{Name: "api.8080", Port: 8080}}}}
	_, before, _ := build(t, route)
	_, after, _ := build(t, route)
	if before != after {
		t.Errorf("fingerprints differ: %s, %s", before, after)
	}

	_, other, _ := build(t, []Route{{Domain: "api.example.com", Backends: []Backend{{Name: "api.9090", Port: 9090}}}})
	if other == before {
		t.Error("another port is another config")
	}
}

func TestFixedUpstreamsRemainForWhatIsNotAnApplication(t *testing.T) {
	_, _, raw := build(t, []Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true}})
	dynamic, static := handlerOf(t, raw, "agent.example.com")
	if dynamic != nil || len(static) != 1 || static[0] != "agent:9000" {
		t.Errorf("dynamic=%+v static=%v", dynamic, static)
	}
}

func TestNobodyBehindANameAnswers503(t *testing.T) {
	// Between the death of an application's last replica and the supervisor
	// noticing, the proxy finds nobody behind the name. It must say what the
	// static route says afterwards, not a bare 502.
	_, _, raw := build(t, []Route{{Domain: "api.example.com", Backends: []Backend{{Name: "api.8080", Port: 8080}}}})
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Errors struct {
						Routes []struct {
							Match  []struct{ Expression string }
							Handle []struct {
								Handler    string
								StatusCode int `json:"status_code"`
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	routes := cfg.Apps.HTTP.Servers[serverName].Errors.Routes
	if len(routes) != 1 || len(routes[0].Handle) != 1 || routes[0].Handle[0].StatusCode != 503 {
		t.Fatalf("error routes = %+v", routes)
	}
}
