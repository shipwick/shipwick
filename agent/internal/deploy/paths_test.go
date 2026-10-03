package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// on gives an application a domain and a path of it.
func on(a spec.App, domain, path string) spec.App {
	a.Domain, a.Path = domain, path
	return a
}

// routeAt is the last route the proxy was given for a path of a domain.
func (p *fakeProxy) routeAt(t *testing.T, domain, path string) proxy.Route {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.syncs) == 0 {
		t.Fatal("the proxy was never synced")
	}
	for _, r := range p.syncs[len(p.syncs)-1] {
		if r.Domain == domain && r.Path == path {
			return r
		}
	}
	t.Fatalf("no route for %s%s", domain, path)
	return proxy.Route{}
}

func TestTwoApplicationsShareAHostnameUnderDifferentPaths(t *testing.T) {
	s, p := newRouted(t)
	site := on(app("web", "web:1.0", 1), "example.com", "")
	site.Aliases = []string{"www.example.com"}
	backend := on(app("api", "api:1.0", 2), "example.com", "/api")
	deeper := on(app("admin", "admin:1.0", 1), "example.com", "/api/admin")
	for _, a := range []spec.App{site, backend, deeper} {
		if d := s.deploy(a); d.Status != api.StatusActive {
			t.Fatalf("%s: %s (%s)", a.Name, d.Status, d.Error)
		}
	}

	if r := p.routeAt(t, "example.com", ""); len(r.Backends) != 1 || r.Backends[0].Name != "web_8080" || strings.Join(r.Aliases, ",") != "www.example.com" {
		t.Errorf("the route without a path = %+v, want web behind it", r)
	}
	if r := p.routeAt(t, "example.com", "/api"); len(r.Backends) != 1 || r.Backends[0].Name != "api_8080" {
		t.Errorf("the route of /api = %+v, want api behind it", r)
	}
	if r := p.routeAt(t, "example.com", "/api/admin"); len(r.Backends) != 1 || r.Backends[0].Name != "admin_8080" {
		t.Errorf("the route of /api/admin = %+v, want admin behind it", r)
	}

	// Each keeps its own rollout: replacing api leaves the others' routes alone.
	if d := s.deploy(on(app("api", "api:1.1", 2), "example.com", "/api")); d.Status != api.StatusActive {
		t.Fatalf("redeploying api: %s (%s)", d.Status, d.Error)
	}
	if r := p.routeAt(t, "example.com", ""); len(r.Backends) != 1 || r.Backends[0].Name != "web_8080" {
		t.Errorf("after api's rollout the route without a path = %+v", r)
	}
}

func TestAHostnameAndPathIsServedByOneApplication(t *testing.T) {
	s, _ := newRouted(t)
	s.engine.opts.ExtraRoutes = []proxy.Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}}}
	whole := on(app("web", "web:1.0", 1), "example.com", "")
	whole.Aliases, whole.Redirects = []string{"www.example.com"}, []string{"example.net"}
	s.deploy(whole)
	s.deploy(on(app("api", "api:1.0", 1), "example.com", "/api"))

	tests := []struct {
		name      string
		app       spec.App
		wantField string
		wantHost  string
		wantPath  string
		wantOwner string
		wantSays  string
	}{
		{"the same path", on(app("other", "o:1", 1), "example.com", "/api"), "path", "example.com", "/api", `application "api"`, "example.com/api is already served by application \"api\"; applications share a domain under different paths"},
		{"the same path in another case", on(app("other", "o:1", 1), "example.com", "/API"), "path", "example.com", "/API", `application "api"`, ""},
		{"no path, like the one that has the rest", on(app("other", "o:1", 1), "example.com", ""), "domain", "example.com", "", `application "web"`, `already served by application "web"`},
		{"an alias's path", on(app("other", "o:1", 1), "www.example.com", ""), "domain", "www.example.com", "", `application "web"`, ""},
		{"a path of a redirected hostname", on(app("other", "o:1", 1), "example.net", "/shop"), "domain", "example.net", "", `application "web"`, `already served by application "web"`},
		{"a path of the agent's hostname", on(app("other", "o:1", 1), "agent.example.com", "/x"), "domain", "agent.example.com", "", "Shipwick itself (the agent or the dashboard)", ""},
		{"a redirect of a hostname that is served", func() spec.App {
			a := on(app("other", "o:1", 1), "other.example.com", "/x")
			a.Redirects = []string{"www.example.com"}
			return a
		}(), "redirects[0]", "www.example.com", "", `application "web"`, ""},
		{"an alias with the same path", func() spec.App {
			a := on(app("other", "o:1", 1), "other.example.com", "/api")
			a.Aliases = []string{"example.com"}
			return a
		}(), "aliases[0]", "example.com", "/api", `application "api"`, `example.com/api is already served by application "api"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.engine.Deploy(context.Background(), tt.app)
			var conflict *DomainConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want a DomainConflictError", err)
			}
			if conflict.Field != tt.wantField || conflict.Domain != tt.wantHost || conflict.Path != tt.wantPath || conflict.Owner != tt.wantOwner {
				t.Errorf("conflict = %+v, want field %s, %s%s, owner %s", conflict, tt.wantField, tt.wantHost, tt.wantPath, tt.wantOwner)
			}
			if tt.wantSays != "" && conflict.Message() != tt.wantSays {
				t.Errorf("message = %q, want %q", conflict.Message(), tt.wantSays)
			}
			if _, err := s.store.GetApplication(context.Background(), "other"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("a refused deployment must leave no trace, got %v", err)
			}
		})
	}

	// A path nobody has, on the domain and on its alias, is free.
	fine := on(app("other", "o:1", 1), "example.com", "/shop")
	fine.Aliases = []string{"www.example.com"}
	if d := s.deploy(fine); d.Status != api.StatusActive {
		t.Errorf("a free path must be accepted: %s %q", d.Status, d.Error)
	}
}

func withProxyBlock(a spec.App) spec.App {
	a.Proxy = &spec.Proxy{
		StripPrefix: true,
		Headers:     map[string]string{"X-Frame-Options": "DENY"},
		BasicAuth:   []spec.BasicAuth{{Path: "/api/admin", Username: "admin", Password: "correct horse"}},
		Redirects:   []spec.PathRedirect{{From: "/api/old", To: "/api/new", Status: 308}},
	}
	return a
}

func TestTheProxyBlockReachesTheRoute(t *testing.T) {
	s, p := newRouted(t)
	a := withProxyBlock(on(app("api", "api:1.0", 1), "example.com", "/api"))
	if d := s.deploy(a); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	r := p.routeAt(t, "example.com", "/api")
	if !r.StripPrefix || r.Headers["X-Frame-Options"] != "DENY" {
		t.Errorf("route = %+v, want the prefix stripped and the header set", r)
	}
	if len(r.PathRedirects) != 1 || r.PathRedirects[0] != (proxy.PathRedirect{From: "/api/old", To: "/api/new", Status: 308}) {
		t.Errorf("redirects = %+v", r.PathRedirects)
	}
	if len(r.BasicAuth) != 1 || r.BasicAuth[0].Path != "/api/admin" || r.BasicAuth[0].Username != "admin" {
		t.Fatalf("accounts = %+v", r.BasicAuth)
	}
	hash := r.BasicAuth[0].Hash
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 10 {
		t.Errorf("hash cost = %d, %v; want bcrypt at cost 10", cost, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse")) != nil {
		t.Error("the hash does not verify the password")
	}

	// Nothing of the password is in what the proxy is told.
	told, _ := json.Marshal(p.syncs[len(p.syncs)-1])
	if strings.Contains(string(told), "correct horse") {
		t.Errorf("the proxy was told the password: %s", told)
	}
	for _, e := range s.events(1) {
		if strings.Contains(e.Message, "correct horse") {
			t.Errorf("event leaks the password: %q", e.Message)
		}
	}
}

func TestAPasswordIsHashedOnceForAsLongAsItIsRouted(t *testing.T) {
	s, p := newRouted(t)
	a := withProxyBlock(on(app("api", "api:1.0", 1), "example.com", "/api"))
	s.deploy(a)
	first := p.routeAt(t, "example.com", "/api").BasicAuth[0].Hash
	configs := p.configs()

	// Every tick computes the routes again; a new hash each time would be a
	// reload of the proxy each time.
	for i := 0; i < 3; i++ {
		s.advance(time.Second)
	}
	if got := p.routeAt(t, "example.com", "/api").BasicAuth[0].Hash; got != first {
		t.Error("the supervisor's sync hashed the password again")
	}
	if p.configs() != configs {
		t.Errorf("the proxy was given %d configurations after the deployment, want none new", p.configs()-configs)
	}

	// The next version has the same account: the same hash, through the
	// rollout and after it.
	b := withProxyBlock(on(app("api", "api:1.1", 1), "example.com", "/api"))
	before := len(p.syncs)
	if d := s.deploy(b); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	for _, routes := range p.syncs[before:] {
		for _, r := range routes {
			if r.Path == "/api" && (len(r.BasicAuth) != 1 || r.BasicAuth[0].Hash != first) {
				t.Fatalf("mid-rollout the route's accounts changed: %+v", r.BasicAuth)
			}
		}
	}

	// Another password is another hash, and the old one is forgotten.
	c := withProxyBlock(on(app("api", "api:1.2", 1), "example.com", "/api"))
	c.Proxy.BasicAuth[0].Password = "battery staple"
	s.deploy(c)
	changed := p.routeAt(t, "example.com", "/api").BasicAuth[0].Hash
	if changed == first || bcrypt.CompareHashAndPassword([]byte(changed), []byte("battery staple")) != nil {
		t.Error("a changed password must reach the proxy as a new hash")
	}
	if n := len(s.engine.hashes.hashes); n != 1 {
		t.Errorf("the cache holds %d hashes, want only the one in use", n)
	}
}

func TestTheProxyBlockSwitchesWhenTheNewVersionTakesTraffic(t *testing.T) {
	s, p := newRouted(t)
	v1 := on(app("api", "api:1.0", 1), "example.com", "/api")
	v1.Proxy = &spec.Proxy{Headers: map[string]string{"X-Version": "1"}}
	s.deploy(v1)

	v2 := on(app("api", "api:2.0", 1), "example.com", "/v2")
	v2.Proxy = &spec.Proxy{Headers: map[string]string{"X-Version": "2"}}
	// The path and the headers are the new version's from the sync that puts
	// its first replica behind the name, and the old version's until then.
	p.onSync = func(routes []proxy.Route) {
		for _, r := range routes {
			if r.Domain != "example.com" || len(r.Backends) == 0 {
				continue
			}
			fresh := strings.Contains(strings.Join(p.resolve(r), ","), "shipwick_api_2_")
			if fresh != (r.Path == "/v2") || fresh != (r.Headers["X-Version"] == "2") {
				t.Errorf("%v are served under %s with X-Version %s", p.resolve(r), r.Path, r.Headers["X-Version"])
			}
		}
	}
	if d := s.deploy(v2); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if r := p.routeAt(t, "example.com", "/v2"); r.Headers["X-Version"] != "2" {
		t.Errorf("route = %+v", r)
	}
}

func TestDeployFillsABasicAuthPasswordFromTheStoredSecrets(t *testing.T) {
	s, p := newRouted(t)
	ctx := context.Background()
	s.store.SetSecret(ctx, "ADMIN_PASSWORD", "correct horse", time.Now())

	a := on(app("api", "api:1.0", 1), "example.com", "")
	a.Proxy = &spec.Proxy{BasicAuth: []spec.BasicAuth{
		{Username: "admin", Password: "${ADMIN_PASSWORD}"},
		{Path: "/literal", Username: "literal", Password: "$${ADMIN_PASSWORD}"},
	}}
	d := s.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if a.Proxy.BasicAuth[0].Password != "${ADMIN_PASSWORD}" {
		t.Error("the caller's spec must not be modified")
	}

	// The record holds the value the proxy was given a hash of: a rollback
	// to it needs no secret that may have changed since.
	stored, _ := s.store.GetDeployment(ctx, d.ID)
	if got := stored.Spec.Proxy.BasicAuth; got[0].Password != "correct horse" || got[1].Password != "${ADMIN_PASSWORD}" {
		t.Errorf("stored passwords = %q, %q", got[0].Password, got[1].Password)
	}
	for _, account := range stored.Spec.Redacted().Proxy.BasicAuth {
		if account.Password != "********" {
			t.Errorf("redacted password of %s = %q", account.Username, account.Password)
		}
	}
	r := p.routeAt(t, "example.com", "")
	for _, account := range r.BasicAuth {
		want := map[string]string{"admin": "correct horse", "literal": "${ADMIN_PASSWORD}"}[account.Username]
		if bcrypt.CompareHashAndPassword([]byte(account.Hash), []byte(want)) != nil {
			t.Errorf("the hash of %s does not verify the resolved password", account.Username)
		}
	}
}

func TestDeployRefusesABasicAuthPasswordNoSecretAnswers(t *testing.T) {
	s, _ := newRouted(t)
	ctx := context.Background()
	a := on(app("api", "api:1.0", 1), "example.com", "")
	a.Env = map[string]string{"KEY": "${API_KEY}"}
	a.Proxy = &spec.Proxy{BasicAuth: []spec.BasicAuth{{Username: "admin", Password: "${ADMIN_PASSWORD}"}}}

	_, err := s.engine.Deploy(ctx, a)
	var missing *MissingSecretsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want MissingSecretsError", err)
	}
	fields := missing.Fields()
	if len(fields) != 2 || fields[0].Field != "env.KEY" || fields[1].Field != "proxy.basic_auth[0].password" || fields[1].Expected != "shipwick secret set ADMIN_PASSWORD" {
		t.Errorf("fields = %+v, want the env variable and the password, each under its field", fields)
	}
	if !strings.HasPrefix(err.Error(), "deploy.yaml refers to ${API_KEY}, ${ADMIN_PASSWORD}") {
		t.Errorf("error = %q", err)
	}

	// A stored value that cannot be a password is refused too, without
	// being repeated.
	s.store.SetSecret(ctx, "API_KEY", "k-1", time.Now())
	s.store.SetSecret(ctx, "ADMIN_PASSWORD", "tiny-pw", time.Now())
	_, err = s.engine.Deploy(ctx, a)
	var unusable *UnusableSecretError
	if !errors.As(err, &unusable) {
		t.Fatalf("err = %v, want UnusableSecretError", err)
	}
	if f := unusable.Fields(); len(f) != 1 || f[0].Field != "proxy.basic_auth[0].password" || !strings.Contains(f[0].Message, "${ADMIN_PASSWORD}") || !strings.Contains(f[0].Message, "too short") {
		t.Errorf("fields = %+v", f)
	}
	if strings.Contains(err.Error(), "tiny-pw") || strings.Contains(err.Error(), "k-1") {
		t.Errorf("error repeats a secret: %q", err)
	}
	if list, _ := s.store.ListDeployments(ctx, store.DeploymentFilter{Application: "api"}); len(list) != 0 {
		t.Errorf("a refused deployment must leave no record, got %d", len(list))
	}

	s.store.SetSecret(ctx, "ADMIN_PASSWORD", "long enough", time.Now())
	if d := s.deploy(a); d.Status != api.StatusActive {
		t.Errorf("after storing a usable password: %s (%s)", d.Status, d.Error)
	}
}

func TestAStaticApplicationsPasswordIsFilledInToo(t *testing.T) {
	s, p := newStaticHarness(t)
	s.store.SetSecret(context.Background(), "SITE_PASSWORD", "correct horse", time.Now())
	a := site("web")
	a.Proxy = &spec.Proxy{BasicAuth: []spec.BasicAuth{{Username: "preview", Password: "${SITE_PASSWORD}"}}}
	if d := s.deployStatic(a, map[string]string{"index.html": "v1"}); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	r := p.route(t, "web.example.com")
	if len(r.BasicAuth) != 1 || bcrypt.CompareHashAndPassword([]byte(r.BasicAuth[0].Hash), []byte("correct horse")) != nil {
		t.Errorf("accounts = %+v, want the stored secret's hash in front of the files", r.BasicAuth)
	}
}

func TestStaticFallbackIsCheckedAndRouted(t *testing.T) {
	s, p := newStaticHarness(t)
	a := site("web")
	a.Path = "/app"
	a.Static.Fallback = "200.html"

	// The page is not in the folder: the deployment fails before anything
	// is routed to it.
	d := s.deployStatic(a, map[string]string{"index.html": "v1"})
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "no 200.html") || !strings.Contains(d.Error, "static.fallback") {
		t.Fatalf("deployment = %s (%s), want it to fail for the missing fallback page", d.Status, d.Error)
	}
	if got := s.served("web"); len(got) != 0 {
		t.Errorf("a failed deployment must leave no files in the proxy, got %v", got)
	}

	files := map[string]string{"index.html": "v2", "200.html": "app"}
	d = s.deployStatic(a, files)
	if d.Status != api.StatusActive {
		t.Fatalf("deployment = %s (%s)", d.Status, d.Error)
	}
	r := p.routeAt(t, "web.example.com", "/app")
	if r.StaticFallback != "200.html" || r.StaticRoot != staticDir("web", d.StaticDigest) {
		t.Errorf("route = %+v, want the fallback page next to the folder", r)
	}
	var steps []string
	for _, e := range s.events(d.ID) {
		steps = append(steps, e.Message)
	}
	if joined := strings.Join(steps, "\n"); !strings.Contains(joined, "Found 200.html, the fallback page") || !strings.Contains(joined, "Routed https://web.example.com/app to the uploaded files") {
		t.Errorf("steps = %q", joined)
	}

	// Without a fallback the route has none: unknown paths are a 404.
	plain := site("web")
	s.deployStatic(plain, files)
	if r := p.route(t, "web.example.com"); r.StaticFallback != "" {
		t.Errorf("route = %+v, want no fallback", r)
	}
}

func TestAnApplicationThatBecomesAContainerLeavesNoFolders(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	second := s.deployStatic(site("web"), map[string]string{"index.html": "v2"})
	if got := s.served("web"); len(got) != 2 {
		t.Fatalf("two folders before the switch, got %v", got)
	}

	// The first container version: the folder it replaced is what a rollback
	// would return to, and stays. The one before that goes.
	c := web("web:1.0", 1)
	if d := s.deploy(c); d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	if got := s.served("web"); len(got) != 1 || got[0] != strings.TrimPrefix(second.StaticDigest, "sha256:")+"/index.html" {
		t.Errorf("folders after the switch = %v, want only the rollback target's", got)
	}
	var steps []string
	for _, e := range s.events(3) {
		steps = append(steps, e.Message)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "Removed 1 folder of older versions") {
		t.Errorf("steps = %q", steps)
	}

	// And the rollback works: the folder is served again without an upload.
	if d := s.finish(s.engine.Rollback(ctx, "web", 0)); d.Status != api.StatusActive {
		t.Fatalf("rollback = %s (%s)", d.Status, d.Error)
	}
	if r := p.route(t, "web.example.com"); r.StaticRoot != staticDir("web", second.StaticDigest) {
		t.Errorf("route after the rollback = %+v", r)
	}

	// Two container versions in a row: no rollback leads to a folder any
	// more, and nothing of the application is left in the proxy.
	s.deploy(web("web:1.1", 1))
	s.deploy(web("web:1.2", 1))
	if got := s.served("web"); len(got) != 0 {
		t.Errorf("folders after two container versions = %v, want none", got)
	}
	removed := s.rt.ProxyRemoved()
	if len(removed) == 0 || removed[len(removed)-1] != staticRoot+"/web" {
		t.Errorf("removed = %v, want the application's directory to go last", removed)
	}

	// From then on a deployment has nothing to look for in the proxy.
	before := len(s.rt.ProxyRemoved())
	s.deploy(web("web:1.3", 1))
	if len(s.rt.ProxyRemoved()) != before {
		t.Errorf("a later deployment removed %v", s.rt.ProxyRemoved()[before:])
	}
}

func TestAContainerApplicationNeverAsksAProxyThatIsNotAContainer(t *testing.T) {
	s, _ := newRouted(t)
	s.rt.ProxyErr = errors.New("the proxy runs outside Docker")
	d := s.deploy(web("web:1.0", 1))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	for _, e := range s.events(d.ID) {
		if e.Level == api.LevelWarn {
			t.Errorf("unexpected warning: %q", e.Message)
		}
	}
}
