package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// withHostnames gives the web application an alias and two redirects.
func withHostnames(a spec.App) spec.App {
	a.Aliases = []string{"api.example.com"}
	a.Redirects = []string{"www.example.com", "example.net"}
	return a
}

// route is the last route the proxy was given for domain.
func (p *fakeProxy) route(t *testing.T, domain string) proxy.Route {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.syncs) == 0 {
		t.Fatal("the proxy was never synced")
	}
	for _, r := range p.syncs[len(p.syncs)-1] {
		if r.Domain == domain {
			return r
		}
	}
	t.Fatalf("no route for %s", domain)
	return proxy.Route{}
}

func TestAliasesAndRedirectsReachTheProxy(t *testing.T) {
	s, p := newRouted(t)
	if d := s.deploy(withHostnames(web("web:1.0", 1))); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	r := p.route(t, "web.example.com")
	if strings.Join(r.Aliases, ",") != "api.example.com" || strings.Join(r.Redirects, ",") != "www.example.com,example.net" {
		t.Errorf("route = %+v, want the spec's aliases and redirects on the domain's route", r)
	}
	if len(r.Backends) != 1 {
		t.Errorf("route = %+v, want the replica behind every hostname", r)
	}
}

func TestRedirectsStayWhileNothingServes(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(withHostnames(web("web:1.0", 1)))
	if err := s.engine.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	r := p.route(t, "web.example.com")
	if len(r.Backends) != 0 || len(r.Redirects) != 2 {
		t.Errorf("route = %+v; a redirect needs no backend and must survive a stop", r)
	}
}

func TestHostnamesAreKeptThroughARollout(t *testing.T) {
	s, p := newRouted(t)
	s.deploy(withHostnames(web("web:1.0", 2)))
	before := len(p.syncs)
	if d := s.deploy(withHostnames(web("web:1.1", 2))); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	// The rollout dictates routing while it runs, and must say the whole
	// truth: dropping the aliases for its duration would be a reload, and
	// a 404 on them.
	for _, routes := range p.syncs[before:] {
		for _, r := range routes {
			if r.Domain == "web.example.com" && (len(r.Aliases) != 1 || len(r.Redirects) != 2) {
				t.Fatalf("mid-rollout the route lost hostnames: %+v", r)
			}
		}
	}
}

func TestAHostnameIsTakenWhateverItsRole(t *testing.T) {
	s, _ := newRouted(t)
	s.engine.opts.ExtraRoutes = []proxy.Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}}}
	// web.example.com, alias api.example.com, redirects www.example.com and example.net.
	s.deploy(withHostnames(web("web:1.0", 1)))

	tests := []struct {
		name      string
		domain    string
		aliases   []string
		redirects []string
		wantField string
		wantHost  string
		wantOwner string
	}{
		{"domain that is an alias elsewhere", "api.example.com", nil, nil, "domain", "api.example.com", `application "web"`},
		{"domain that is redirected elsewhere", "www.example.com", nil, nil, "domain", "www.example.com", `application "web"`},
		{"alias that is a domain elsewhere", "other.example.com", []string{"web.example.com"}, nil, "aliases[0]", "web.example.com", `application "web"`},
		{"redirect that is redirected elsewhere", "other.example.com", nil, []string{"x.example.com", "example.net"}, "redirects[1]", "example.net", `application "web"`},
		{"redirect of another application's domain", "other.example.com", nil, []string{"web.example.com"}, "redirects[0]", "web.example.com", `application "web"`},
		{"alias that is the agent's", "other.example.com", []string{"agent.example.com"}, nil, "aliases[0]", "agent.example.com", "Shipwick itself (the agent or the dashboard)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := app("other", "other:1.0", 1)
			other.Domain, other.Aliases, other.Redirects = tt.domain, tt.aliases, tt.redirects
			_, err := s.engine.Deploy(context.Background(), other)
			var conflict *DomainConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want a DomainConflictError", err)
			}
			if conflict.Field != tt.wantField || conflict.Domain != tt.wantHost || conflict.Owner != tt.wantOwner {
				t.Errorf("conflict = %+v, want field %s, host %s, owner %s", conflict, tt.wantField, tt.wantHost, tt.wantOwner)
			}
			if _, err := s.store.GetApplication(context.Background(), "other"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a refused deployment must leave no trace, got %v", err)
			}
		})
	}

	fine := app("other", "other:1.0", 1)
	fine.Domain, fine.Aliases, fine.Redirects = "other.example.com", []string{"api.example.net"}, []string{"www.example.org"}
	if d := s.deploy(fine); d.Status != api.StatusActive {
		t.Errorf("hostnames nobody has must be accepted: %s %q", d.Status, d.Error)
	}
}
