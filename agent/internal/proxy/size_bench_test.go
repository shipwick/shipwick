package proxy

import (
	"fmt"
	"testing"
)

// BenchmarkBuildTwoHundredHostnames renders the proxy's configuration for a
// server of 200 applications, each with a hostname and a redirect from its
// www name, and reports its size: what the proxy is sent at every change.
func BenchmarkBuildTwoHundredHostnames(b *testing.B) {
	routes := make([]Route, 0, 202)
	routes = append(routes,
		Route{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true},
		Route{Domain: "dash.example.com", Upstreams: []string{"dashboard:3000"}, Streaming: true})
	for i := range 200 {
		name := fmt.Sprintf("app-%03d", i)
		routes = append(routes, Route{
			Domain:    name + ".example.com",
			Redirects: []string{"www." + name + ".example.com"},
			Backends:  []Backend{{Name: name + "_8080", Port: 8080}},
		})
	}
	config, _, err := Build("unix//run/caddy/admin.sock", routes)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Build("unix//run/caddy/admin.sock", routes); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(config)), "bytes")
}
