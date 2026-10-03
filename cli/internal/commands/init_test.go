package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

func readOrEmpty(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitRecognisesAProject(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{
		"package.json":      `{"dependencies":{"nuxt":"^4"},"scripts":{"build":"nuxt build"}}`,
		"package-lock.json": "{}",
	})

	out, _, err := f.run(dir, "init", "--name", "site", "--domain", "www.example.com")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, want := range []string{"Recognised a Nuxt application", "Wrote Dockerfile, .dockerignore and deploy.yaml", "Review them, then run: shipwick deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	app, err := spec.Parse([]byte(readOrEmpty(t, dir, DefaultFile)))
	if err != nil {
		t.Fatalf("init must produce a valid config: %v", err)
	}
	if app.Name != "site" || app.Image != "" || app.Port != 3000 || app.Domain != "www.example.com" || app.Health == nil || app.Health.Path != "/" {
		t.Errorf("unexpected config: %+v", app)
	}
	dockerfile := readOrEmpty(t, dir, "Dockerfile")
	if !strings.Contains(dockerfile, "RUN npm run build") || !strings.Contains(dockerfile, "EXPOSE 3000") {
		t.Errorf("unexpected Dockerfile:\n%s", dockerfile)
	}
	if ignore := readOrEmpty(t, dir, ".dockerignore"); !strings.Contains(ignore, "node_modules\n") {
		t.Errorf("unexpected .dockerignore:\n%s", ignore)
	}
	if len(f.requests) != 0 {
		t.Errorf("init is offline, saw %v", f.requests)
	}
}

func TestInitKeepsAnExistingDockerfile(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{
		"go.mod":     "module example.com/api\n\ngo 1.27\n",
		"main.go":    "package main\n",
		"Dockerfile": "FROM scratch\n",
	})

	out, _, err := f.run(dir, "init", "--name", "api", "--port", "9090")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "Recognised a Go program") || !strings.Contains(out, "Kept the existing Dockerfile; build: . will use it") || !strings.Contains(out, "Wrote .dockerignore and deploy.yaml") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if got := readOrEmpty(t, dir, "Dockerfile"); got != "FROM scratch\n" {
		t.Errorf("the developer's Dockerfile was replaced:\n%s", got)
	}
	app, err := spec.Parse([]byte(readOrEmpty(t, dir, DefaultFile)))
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "api" || app.Port != 9090 || app.Health != nil {
		t.Errorf("unexpected config: %+v", app)
	}

	// --force is about deploy.yaml; the Dockerfile stays the developer's.
	if _, _, err := f.run(dir, "init", "--force"); err != nil {
		t.Fatalf("init --force: %v", err)
	}
	if got := readOrEmpty(t, dir, "Dockerfile"); got != "FROM scratch\n" {
		t.Error("--force must not overwrite the Dockerfile")
	}
}

func TestInitWithSeveralGoProgramsNeedsATerminal(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{
		"go.mod":             "module example.com/svc\n\ngo 1.27\n",
		"cmd/api/main.go":    "package main\n",
		"cmd/worker/main.go": "package main\n",
	})
	_, _, err := f.run(dir, "init")
	if err == nil || !strings.Contains(err.Error(), "./cmd/api, ./cmd/worker") || !strings.Contains(err.Error(), "in a terminal") {
		t.Errorf("err = %v", err)
	}
	if readOrEmpty(t, dir, DefaultFile) != "" || readOrEmpty(t, dir, "Dockerfile") != "" {
		t.Error("nothing must be written when the program is unknown")
	}
}

func TestInitStatic(t *testing.T) {
	f := newFakeAgent(t)

	dir := writeTree(t, map[string]string{"dist/index.html": "<html>"})
	// The proxy serves the folder at a domain; off a terminal it must be given.
	if _, _, err := f.run(dir, "init", "--name", "site"); err == nil || !strings.Contains(err.Error(), "needs a domain") {
		t.Errorf("err = %v", err)
	}
	out, _, err := f.run(dir, "init", "--name", "site", "--domain", "site.example.com")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "Recognised a static site (dist/)") || !strings.Contains(out, "Wrote deploy.yaml\n") || !strings.Contains(out, "Review it, then run") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if readOrEmpty(t, dir, "Dockerfile") != "" || readOrEmpty(t, dir, ".dockerignore") != "" {
		t.Error("a static site needs no Dockerfile")
	}
	config := readOrEmpty(t, dir, DefaultFile)
	if _, err := spec.Parse([]byte(config)); err != nil {
		t.Fatalf("invalid config: %v\n%s", err, config)
	}
	if !strings.Contains(config, "static: dist/\n") || !strings.Contains(config, "\ndomain: site.example.com\n") {
		t.Errorf("unexpected config:\n%s", config)
	}

	// --static forces the kind, whatever the directory holds.
	dir = writeTree(t, map[string]string{"package.json": `{"dependencies":{"nuxt":"^4"}}`})
	if _, _, err := f.run(dir, "init", "--static", "public/", "--domain", "example.com"); err != nil {
		t.Fatalf("init --static: %v", err)
	}
	if config := readOrEmpty(t, dir, DefaultFile); !strings.Contains(config, "static: public\n") || strings.Contains(config, "build:") {
		t.Errorf("unexpected config:\n%s", config)
	}
	if readOrEmpty(t, dir, "Dockerfile") != "" {
		t.Error("--static writes no Dockerfile")
	}

	if _, _, err := f.run(t.TempDir(), "init", "--static", "dist", "--image", "nginx:1"); err == nil || !strings.Contains(err.Error(), "exclude each other") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := f.run(t.TempDir(), "init", "--static", "dist", "--port", "80"); err == nil || !strings.Contains(err.Error(), "--port does not apply") {
		t.Errorf("err = %v", err)
	}
}

func TestInitImageSkipsDetection(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{"package.json": `{"dependencies":{"nuxt":"^4"}}`})
	out, _, err := f.run(dir, "init", "--image", "ghcr.io/company/site:1.0.0", "--port", "3000")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if strings.Contains(out, "Recognised") || readOrEmpty(t, dir, "Dockerfile") != "" {
		t.Errorf("--image keeps init to deploy.yaml:\n%s", out)
	}
	if config := readOrEmpty(t, dir, DefaultFile); !strings.Contains(config, "image: ghcr.io/company/site:1.0.0\n") {
		t.Errorf("unexpected config:\n%s", config)
	}
}
func TestInitAsksWhatDetectionCannotKnow(t *testing.T) {
	// The prompts run on a terminal only; the flow behind them is exercised
	// directly, with the answers on the reader.
	dir := writeTree(t, map[string]string{
		"go.mod":             "module example.com/svc\n\ngo 1.27\n",
		"cmd/api/main.go":    "package main\n",
		"cmd/worker/main.go": "package main\n",
	})
	t.Chdir(dir)
	p, err := detectProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c, _ := newRoot(Options{In: strings.NewReader("svc\n2\napi.example.com\n"), Out: &out, Err: &out, Getenv: func(string) string { return "" }})
	answers := initAnswers{Name: defaultAppName("."), Project: &p}
	if err := c.completeProject(&answers, true); err != nil {
		t.Fatal(err)
	}
	if answers.Name != "svc" || p.Go.Package != "./cmd/worker" || answers.Port != 8080 || answers.Domain != "api.example.com" || p.Label != "a Go program (cmd/worker)" {
		t.Errorf("answers = %+v, project = %+v", answers, p.Go)
	}
	if !strings.Contains(out.String(), "1. ./cmd/api") || !strings.Contains(out.String(), "Public domain") {
		t.Errorf("unexpected prompts:\n%s", out.String())
	}
}
