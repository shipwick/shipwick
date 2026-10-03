package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const proxyConfig = `name: api
image: ghcr.io/company/api:1.0
port: 8080
domain: example.com
path: /api
proxy:
  strip_prefix: true
  headers:
    X-Frame-Options: DENY
    X-Build: ${BUILD}
  basic_auth:
    - path: /api/admin
      username: admin
      password: ${ADMIN_PASSWORD}
    - username: everyone
      password: $${LITERAL}-and-more
  redirects:
    - from: /api/old
      to: /api/${TARGET}
`

func TestExpandLeavesABasicAuthPasswordToTheServer(t *testing.T) {
	values := map[string]string{"BUILD": "42", "TARGET": "new"}
	out, used, err := expand([]byte(proxyConfig), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(used.substituted, ",") != "BUILD,TARGET" || strings.Join(used.deferred, ",") != "ADMIN_PASSWORD" {
		t.Errorf("used = %+v, want the password left to the server and the rest filled in", used)
	}
	// The escaped form in a password travels as it is, like in an env value:
	// the agent turns it into ${LITERAL}.
	for _, want := range []string{"X-Build: \"42\"", "password: ${ADMIN_PASSWORD}", "password: $${LITERAL}-and-more", "to: /api/new"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	app, err := spec.Parse(out)
	if err != nil || app.Proxy.BasicAuth[0].Password != "${ADMIN_PASSWORD}" {
		t.Errorf("what is sent must validate with the placeholder in it: %v", err)
	}

	// Set here, it is filled in here, and nothing is left to the server.
	values["ADMIN_PASSWORD"] = "correct horse"
	out, used, err = expand([]byte(proxyConfig), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil || len(used.deferred) != 0 || !strings.Contains(string(out), "password: correct horse") {
		t.Errorf("used = %+v, err = %v:\n%s", used, err, out)
	}
}

func TestExpandLeavesNothingElseOfTheProxyBlockToTheServer(t *testing.T) {
	_, _, err := expand([]byte(proxyConfig), func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "${BUILD}, ${TARGET}") || strings.Contains(err.Error(), "ADMIN_PASSWORD") {
		t.Errorf("err = %v, want the header and the redirect reported and the password not", err)
	}
	if !strings.Contains(err.Error(), "passwords of proxy.basic_auth") {
		t.Errorf("err = %v, want it to say what can be left to the server", err)
	}

	// A username is not a secret.
	_, _, err = expand([]byte("name: api\nproxy:\n  basic_auth:\n    - username: ${WHO}\n      password: ${PW}\n"), func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "${WHO}") || strings.Contains(err.Error(), "${PW}") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateDescribesPathAndProxyWithoutThePassword(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"BUILD": "42", "TARGET": "new", "ADMIN_PASSWORD": "correct horse"}
	out, _, err := f.run(writeConfig(t, proxyConfig), "validate")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"Domain", "example.com", "Path", "/api", "Proxy", "strips /api; 2 response headers; a password for /api/admin, everything; 1 redirect"})
	if strings.Contains(out, "correct horse") {
		t.Errorf("the summary shows a password:\n%s", out)
	}
}

func TestValidateSaysWhichPasswordTheServerFillsIn(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"BUILD": "42", "TARGET": "new"}
	out, _, err := f.run(writeConfig(t, proxyConfig), "validate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "${ADMIN_PASSWORD} is not set here; the server fills it in from its secrets") {
		t.Errorf("out = %s", out)
	}
}

func TestTakenHostname(t *testing.T) {
	others := []api.Application{
		{Name: "web", Domain: "example.com", Aliases: []string{"www.example.com"}, Redirects: []string{"example.net"}},
		{Name: "api", Domain: "example.com", Path: "/api"},
		{Name: "me", Domain: "example.com", Path: "/shop"},
	}
	app := func(domain, path string, aliases, redirects []string) spec.App {
		return spec.App{Name: "me", Domain: domain, Path: path, Aliases: aliases, Redirects: redirects}
	}
	for _, tt := range []struct {
		name      string
		app       spec.App
		taken, by string
	}{
		{"a path of its own", app("example.com", "/shop", nil, nil), "", ""},
		{"a path of its own on the alias too", app("example.com", "/docs", []string{"www.example.com"}, nil), "", ""},
		{"the same path", app("example.com", "/api", nil, nil), "example.com/api", "api"},
		{"the same path in another case", app("example.com", "/API", nil, nil), "example.com/API", "api"},
		{"no path, like the application that has the rest", app("example.com", "", nil, nil), "example.com", "web"},
		{"a path of a redirected hostname", app("example.net", "/x", nil, nil), "example.net", "web"},
		{"a redirect of a hostname that is served", app("other.example.com", "", nil, []string{"www.example.com"}), "www.example.com", "web"},
		{"a redirect that is redirected elsewhere", app("other.example.com", "", nil, []string{"example.net"}), "example.net", "web"},
	} {
		if taken, by := takenHostname(tt.app, others); taken != tt.taken || by != tt.by {
			t.Errorf("%s: taken = %q by %q, want %q by %q", tt.name, taken, by, tt.taken, tt.by)
		}
	}
}

func TestDeployStaticChecksTheFallbackPageBeforeUploading(t *testing.T) {
	f := newFakeAgent(t)
	dir := staticSite(t, map[string]string{"index.html": "<h1>hi</h1>"})
	config := "name: web\nstatic: {dir: dist/, fallback: 200.html}\ndomain: example.com\n"
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.run(dir, "deploy")
	if err == nil || !strings.Contains(err.Error(), "names 200.html as static.fallback") || !strings.Contains(err.Error(), "has no such file") {
		t.Errorf("err = %v", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/static") || strings.Contains(r, "/deploy") {
			t.Errorf("nothing may be sent for a fallback page that is not in the folder, saw %s", r)
		}
	}

	out, _, err := f.run(dir, "validate")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"Folder", "dist/", "Fallback", "200.html for paths that name no file"})
}

func TestInitWritesAFallbackForASinglePageApplication(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package-lock.json", "{}")
	write("package.json", `{"devDependencies":{"vite":"^6"},"scripts":{"build":"vite build"}}`)
	p, err := detectProject(dir)
	if err != nil || p.Kind != kindStatic || p.Static.Fallback != "index.html" {
		t.Fatalf("a Vite project: %+v, %v; want a static site with index.html as its fallback", p.Static, err)
	}
	content := renderConfig(initAnswers{Name: "my-app", Project: &p, Domain: "app.example.com"})
	app, err := spec.Parse([]byte(content))
	if err != nil {
		t.Fatalf("deploy.yaml does not validate: %v\n%s", err, content)
	}
	if app.Static.Dir != "dist" || app.Static.Fallback != "index.html" || !strings.Contains(content, "static: {dir: dist/, fallback: index.html}\n") {
		t.Errorf("static = %+v:\n%s", app.Static, content)
	}

	// Astro writes a file for every page: an unknown path is a 404 there.
	write("package.json", `{"dependencies":{"astro":"^5"},"scripts":{"build":"astro build"}}`)
	p, err = detectProject(dir)
	if err != nil || p.Kind != kindStatic || p.Static.Fallback != "" {
		t.Fatalf("an Astro project: %+v, %v; want a static site without a fallback", p.Static, err)
	}
	if content := renderConfig(initAnswers{Name: "my-site", Project: &p, Domain: "example.com"}); !strings.Contains(content, "static: dist/\n") || strings.Contains(content, "fallback") {
		t.Errorf("unexpected config:\n%s", content)
	}
}
