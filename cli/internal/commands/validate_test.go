package commands

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// fakeTimer stands in for time.AfterFunc in the CLI under test.
type fakeTimer = func(time.Duration, func()) func()

// fakeValidate answers POST /applications/{name}/validate: the agent's dry
// run of a deploy.yaml.
type fakeValidate struct {
	mu     sync.Mutex
	bodies []string
	// older makes the agent one that predates the operation.
	older  bool
	status int // non-zero: the document is refused with this status and refusal
	refuse api.Error
}

func (v *fakeValidate) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/applications/{name}/validate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.bodies = append(v.bodies, string(body))
		older, status, refuse := v.older, v.status, v.refuse
		v.mu.Unlock()
		switch {
		case older:
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: POST " + r.URL.Path})
		case status != 0:
			respondError(w, status, refuse)
		default:
			respond(w, 200, map[string]bool{"valid": true})
		}
	})
}

func invalidPublish() api.Error {
	return api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{
		"fields": []map[string]string{{"field": "publish[0].host", "message": "port 5432 is already published by application \"postgres\"", "expected": "a free port"}},
	}}
}

func requested(f *fakeAgent, fragment string) bool {
	for _, r := range f.requests {
		if strings.Contains(r, fragment) {
			return true
		}
	}
	return false
}

func TestDeployWithBuildAsksTheAgentBeforeBuilding(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	f.validate.status, f.validate.refuse = 400, invalidPublish()
	docker := &fakeDocker{}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	want := "invalid deploy.yaml\n\npublish[0].host:\n  port 5432 is already published by application \"postgres\"\n  expected: a free port\n"
	if got := Render(err); got != want {
		t.Errorf("a refusal must read as a refused deploy does, got:\n%s", got)
	}
	if docker.argv != nil || f.images.body != "" || len(f.deployBodies) != 0 {
		t.Errorf("nothing may be built, sent or deployed after a refusal: ran %q", docker.argv)
	}
	// The document goes as deploy would send it: with build, without an image.
	if len(f.validate.bodies) != 1 || f.validate.bodies[0] != buildConfig {
		t.Errorf("validated %q", f.validate.bodies)
	}
}

func TestDeployWithBuildStopsWhenTheApplicationIsBusy(t *testing.T) {
	f := newFakeAgent(t)
	f.validate.status = 409
	f.validate.refuse = api.Error{Code: api.CodeDeploymentInProgress, Message: "another operation is in progress"}
	docker := &fakeDocker{}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if got := Render(err); !strings.Contains(got, "Another operation is already in progress") {
		t.Errorf("rendering = %q", got)
	}
	if docker.argv != nil {
		t.Errorf("nothing may be built for a busy application, ran %q", docker.argv)
	}
}

func TestDeployWithBuildAsksAnOlderAgentAboutTheDomainOnly(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	f.validate.older = true
	f.app = api.ApplicationDetail{Application: api.Application{Name: "legacy", Domain: "api.example.com"}}
	docker := &fakeDocker{}
	f.build = docker.tools()
	dir := writeConfig(t, buildConfig)

	_, _, err := f.run(dir, "deploy")
	if err == nil || !strings.Contains(err.Error(), `api.example.com is already served by application "legacy"; nothing was built`) || !strings.Contains(err.Error(), "shipwick delete legacy") {
		t.Errorf("err = %v", err)
	}
	if docker.argv != nil {
		t.Errorf("nothing may be built for a taken domain, ran %q", docker.argv)
	}

	// With the domain free, an older agent is no obstacle.
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &done}}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Domain: "api.example.com"}}
	if out, _, err := f.run(dir, "deploy"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if docker.argv == nil || len(f.deployBodies) != 1 {
		t.Errorf("the deployment should have gone through: %q, %d documents", docker.argv, len(f.deployBodies))
	}
}

func TestDeployStaticAsksTheAgentBeforeUploading(t *testing.T) {
	f := newFakeAgent(t)
	f.validate.status, f.validate.refuse = 400, invalidPublish()
	dir := staticSite(t, map[string]string{"index.html": "<h1>hi</h1>"})

	_, _, err := f.run(dir, "deploy")
	if got := Render(err); !strings.Contains(got, "publish[0].host:") {
		t.Errorf("rendering = %q", got)
	}
	if requested(f, "/static") || len(f.deployBodies) != 0 {
		t.Errorf("nothing may be uploaded or deployed after a refusal: %v", f.requests)
	}
	// A static document is validated as it is written: there is no digest yet.
	if len(f.validate.bodies) != 1 || f.validate.bodies[0] != staticConfig {
		t.Errorf("validated %q", f.validate.bodies)
	}

	f.validate = fakeValidate{older: true}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "legacy", Aliases: []string{"example.com"}}}
	_, _, err = f.run(dir, "deploy")
	if err == nil || !strings.Contains(err.Error(), `example.com is already served by application "legacy"; nothing was uploaded`) {
		t.Errorf("err = %v", err)
	}
	if requested(f, "/static") {
		t.Errorf("nothing may be uploaded for a taken domain: %v", f.requests)
	}
}

func TestDeployWithBuildShowsOneProgressLineInATerminal(t *testing.T) {
	f := newFakeAgent(t)
	f.terminal = true
	f.server = api.Server{Architecture: "x86_64"}
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &done}}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	docker := &fakeDocker{output: "#1 [internal] load build definition\n#2 [build 4/6] RUN npm ci\n\n"}
	f.build = docker.tools()
	dir := writeConfig(t, buildConfig)

	out, _, err := f.run(dir, "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"… Building the image (0s)",
		"… Building the image (0s) — #2 [build 4/6] RUN npm ci\r\x1b[K",
		"✓ Built shipwick.local/my-api:",
	})
	if strings.Contains(out, "load build definition") || strings.Contains(out, "$ docker build") {
		t.Errorf("a build that works shows its progress line and nothing else:\n%q", out)
	}

	// --verbose is the build as docker prints it.
	out, _, err = f.run(dir, "deploy", "--verbose")
	if err != nil {
		t.Fatalf("deploy --verbose: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"$ docker build --platform linux/amd64", "#1 [internal] load build definition", "#2 [build 4/6] RUN npm ci", "✓ Built"})
	if strings.Contains(out, "Building the image") {
		t.Errorf("--verbose needs no progress line:\n%q", out)
	}
}

func TestDeployWithBuildShowsTheWholeOutputWhenTheBuildFails(t *testing.T) {
	f := newFakeAgent(t)
	f.terminal = true
	f.server = api.Server{Architecture: "x86_64"}
	var lines []string
	for i := 1; i <= 15; i++ {
		lines = append(lines, fmt.Sprintf("#%d step", i))
	}
	docker := &fakeDocker{output: strings.Join(lines, "\n") + "\nERROR: failed to solve", err: &exec.ExitError{}}
	f.build = docker.tools()

	out, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if err == nil || !strings.HasPrefix(err.Error(), "docker build failed") {
		t.Fatalf("err = %v", err)
	}
	// Everything docker printed, once: on screen, not again in the error.
	assertInOrder(t, out, []string{"$ docker build", "#1 step\n", "#15 step\n", "ERROR: failed to solve\n"})
	if strings.Contains(err.Error(), "#15 step") {
		t.Errorf("the output is on screen; the error should not repeat it:\n%s", err)
	}
}

func TestBuildOutputPassesOnTheLastLineDockerPrinted(t *testing.T) {
	var shown []string
	w := &buildOutput{show: func(last string) { shown = append(shown, last) }}
	for _, chunk := range []string{"#1 load\n", "#2 RUN npm", " ci\n", "\x1b[1m#3 exporting\x1b[0m  \r\n", "#4 10%\r#4 100%\n", "\n"} {
		io.WriteString(w, chunk)
	}
	want := []string{"#1 load", "#1 load", "#2 RUN npm ci", "[1m#3 exporting[0m", "#4 100%", "#4 100%"}
	if strings.Join(shown, "|") != strings.Join(want, "|") {
		t.Errorf("shown = %q\n want = %q", shown, want)
	}
	if got := w.String(); !strings.HasPrefix(got, "#1 load\n#2 RUN npm ci\n") {
		t.Errorf("the whole output is kept as it was printed, got %q", got)
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0s", 12400 * time.Millisecond: "12s", 65 * time.Second: "1m05s"} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLogsFollowSaysThatItIsWaiting(t *testing.T) {
	f := newFakeAgent(t)
	f.terminal = true
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	var waited time.Duration
	// The timer fires at once: two seconds have passed and nothing arrived.
	f.after = func(d time.Duration, fn func()) func() {
		waited = d
		fn()
		return func() {}
	}

	_, errOut, err := f.run(t.TempDir(), "logs", "-f", "my-api")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if waited != 2*time.Second {
		t.Errorf("waited %v, want 2s", waited)
	}
	if !strings.HasPrefix(errOut, "Following my-api; nothing printed yet. Ctrl-C stops.\n") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogsFollowOffATerminalSetsNoTimer(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	f.after = func(time.Duration, func()) func() {
		t.Error("a pipe is not told that it is waiting")
		return func() {}
	}
	if _, _, err := f.run(t.TempDir(), "logs", "-f", "my-api"); err != nil {
		t.Fatalf("logs: %v", err)
	}
}

func TestNoteSilenceIsQuietOnceALineHasArrived(t *testing.T) {
	var out, errOut bytes.Buffer
	c, _ := newRoot(Options{In: strings.NewReader(""), Out: &out, Err: &errOut, Getenv: func(string) string { return "" }})
	c.ui = ui.Terminal(&out, &errOut)
	var fire func()
	stopped := false
	c.after = func(_ time.Duration, fn func()) func() {
		fire = fn
		return func() { stopped = true }
	}

	print, stop := c.noteSilence("my-api", func(line api.LogLine) { c.ui.Println(line.Message) })
	print(api.LogLine{Message: "listening on :8080"})
	fire()
	stop()
	if out.String() != "listening on :8080\n" || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errOut.String())
	}
	if !stopped {
		t.Error("stop must call the timer off")
	}

	// Nothing arrived by the time the timer fires.
	_, _ = c.noteSilence("my-api", func(api.LogLine) {})
	fire()
	if got := errOut.String(); got != "Following my-api; nothing printed yet. Ctrl-C stops.\n" {
		t.Errorf("stderr = %q", got)
	}
}
