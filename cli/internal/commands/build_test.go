package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// fakeImages answers POST /applications/{name}/images the way the agent does:
// it reads the archive and reports the reference it carried. Like the agent's
// test runtime, it takes the body to be the list of references.
type fakeImages struct {
	mu          sync.Mutex
	contentType string
	body        string
	status      int // non-zero: the upload fails with this status
}

func (i *fakeImages) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/applications/{name}/images", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		i.mu.Lock()
		i.contentType, i.body = r.Header.Get("Content-Type"), string(body)
		status := i.status
		i.mu.Unlock()
		if status != 0 {
			respondError(w, status, api.Error{Code: api.CodeInvalidRequest, Message: "the archive holds no tagged image"})
			return
		}
		respond(w, 201, api.LoadedImage{Image: strings.TrimSpace(string(body)), SizeBytes: int64(len(body))})
	})
}

const buildConfig = "name: my-api\nbuild: .\nport: 8080\ndomain: api.example.com\n"

// fakeDocker stands in for `docker build` and `docker save`: it records the
// argv and answers with an archive naming the image the build was asked for.
type fakeDocker struct {
	argv   []string
	dir    string // where the build ran
	output string // what the build prints
	err    error  // how the build fails, if it does
	saved  string // the image `save` was asked for
}

func (d *fakeDocker) tools() buildTools {
	return buildTools{
		run: func(_ context.Context, dir string, argv []string, out io.Writer) error {
			d.dir, d.argv = dir, argv
			io.WriteString(out, d.output)
			return d.err
		},
		save: func(_ context.Context, image string) (io.ReadCloser, error) {
			d.saved = image
			return io.NopCloser(strings.NewReader(image + "\n")), nil
		},
	}
}

var localTag = regexp.MustCompile(`^shipwick\.local/my-api:20260301-120000-[0-9a-f]{4}$`)

func TestDeployWithBuildBuildsHereAndSendsTheImage(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "aarch64"}
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &done}}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	docker := &fakeDocker{output: "#1 [internal] load build definition\n#2 DONE 0.1s\n"}
	f.build = docker.tools()

	out, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	// docker build --platform <server> -f <dockerfile> -t <ref> <context>, as an argv.
	if len(docker.argv) != 9 || strings.Join(docker.argv[:7], " ") != "docker build --platform linux/arm64 -f Dockerfile -t" || docker.argv[8] != "." {
		t.Fatalf("argv = %q", docker.argv)
	}
	image := docker.argv[7]
	if !localTag.MatchString(image) {
		t.Errorf("image = %q, want shipwick.local/<app>:<UTC stamp>-<4 hex>", image)
	}
	if docker.saved != image {
		t.Errorf("saved %q, built %q", docker.saved, image)
	}
	if f.images.contentType != "application/x-tar" || f.images.body != image+"\n" {
		t.Errorf("upload: Content-Type %q, body %q", f.images.contentType, f.images.body)
	}

	sent, err := spec.Parse([]byte(f.deployBodies[0]))
	if err != nil {
		t.Fatalf("the agent must receive a valid deploy.yaml: %v\n%s", err, f.deployBodies[0])
	}
	if sent.Image != image || sent.Build == nil || sent.Domain != "api.example.com" {
		t.Errorf("the document sent should name the image and keep the rest: %+v", sent)
	}
	assertInOrder(t, out, []string{
		"✓ Validated deploy.yaml",
		"#1 [internal] load build definition",
		"✓ Built " + image + " for linux/arm64",
		"✓ Sent image to the server",
		"deployed in",
	})
}

func TestDeployWithBuildExplainsAMissingDocker(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	docker := &fakeDocker{err: &exec.Error{Name: "docker", Err: exec.ErrNotFound}}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	want := "docker is not installed on this machine, and build: needs it here (the server never builds); install Docker Desktop or set image: instead"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v", err)
	}
	if f.images.body != "" || len(f.deployBodies) != 0 {
		t.Error("nothing may be sent when the build did not happen")
	}
}

func TestDeployWithBuildReportsTheBuildsLastLines(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	var lines []string
	for i := 1; i <= 15; i++ {
		lines = append(lines, fmt.Sprintf("#%d step", i))
	}
	docker := &fakeDocker{output: strings.Join(lines, "\n") + "\nERROR: failed to solve: process did not complete successfully: exit code: 1\n",
		err: &exec.ExitError{}}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if err == nil {
		t.Fatal("a failed build must fail the command")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "docker build failed") || !strings.Contains(msg, "ERROR: failed to solve") || !strings.Contains(msg, "#15 step") {
		t.Errorf("the error should carry the build's last lines:\n%s", msg)
	}
	if strings.Contains(msg, "#1 step\n") {
		t.Errorf("only the last lines, not the whole build:\n%s", msg)
	}
	if len(f.deployBodies) != 0 {
		t.Error("nothing may be deployed after a failed build")
	}
}

func TestDeployWithBuildStopsWhenTheServerRefusesTheImage(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	f.images.status = 400
	docker := &fakeDocker{}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if err == nil || !strings.Contains(Render(err), "the archive holds no tagged image") {
		t.Errorf("err = %v", err)
	}
	if len(f.deployBodies) != 0 {
		t.Error("a refused image must not be deployed")
	}
}

func TestDeployWithBuildNeedsAKnownServerArchitecture(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "mips"}
	docker := &fakeDocker{}
	f.build = docker.tools()

	_, _, err := f.run(writeConfig(t, buildConfig), "deploy")
	if err == nil || !strings.Contains(err.Error(), `"mips"`) || !strings.Contains(err.Error(), "set image: instead") {
		t.Errorf("err = %v", err)
	}
	if docker.argv != nil {
		t.Errorf("nothing should be built for an unknown platform, ran %q", docker.argv)
	}
}

func TestDeployImageFlagDoesNotApplyToABuiltApplication(t *testing.T) {
	f := newFakeAgent(t)
	_, _, err := f.run(writeConfig(t, buildConfig), "deploy", "--image", "ghcr.io/company/my-api:1.0")
	if err == nil || !strings.Contains(err.Error(), "--image does not apply") || !strings.Contains(err.Error(), "remove build:") {
		t.Errorf("err = %v", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("refused before the agent is asked anything, saw %v", f.requests)
	}
}

func TestValidateDescribesTheBuildAndDoesNotBuild(t *testing.T) {
	f := newFakeAgent(t)
	docker := &fakeDocker{}
	f.build = docker.tools()

	out, _, err := f.run(writeConfig(t, "name: my-api\nbuild:\n  context: services/api\n  dockerfile: docker/Dockerfile.prod\nport: 8080\n"), "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	assertInOrder(t, out, []string{"deploy.yaml is valid", "Build", "services/api/ (docker/Dockerfile.prod)", "builds the image on this machine and sends it to the server"})
	if strings.Contains(out, "Image") || strings.Contains(out, "Version") {
		t.Errorf("there is no image to describe before the build:\n%s", out)
	}
	if docker.argv != nil || len(f.requests) != 0 {
		t.Errorf("validate neither builds nor talks to the agent: %q %v", docker.argv, f.requests)
	}

	out, _, _ = f.run(writeConfig(t, buildConfig), "validate")
	if !strings.Contains(out, "./ (Dockerfile)") {
		t.Errorf("a dot context reads as ./:\n%s", out)
	}
}

func TestDockerPlatform(t *testing.T) {
	for arch, want := range map[string]string{"x86_64": "linux/amd64", "aarch64": "linux/arm64", "armv7l": "linux/arm/v7"} {
		if got, err := dockerPlatform(arch); got != want || err != nil {
			t.Errorf("dockerPlatform(%q) = %q, %v", arch, got, err)
		}
	}
	if _, err := dockerPlatform(""); err == nil {
		t.Error("an agent that reports no architecture cannot be built for")
	}
}

func TestTailWriterKeepsTheLastLines(t *testing.T) {
	w := &tailWriter{}
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	io.WriteString(w, "no newline yet")
	got := w.String()
	if strings.HasPrefix(got, "line 1\n") || !strings.HasPrefix(got, "line 3\n") || !strings.HasSuffix(got, "line 12\nno newline yet") {
		t.Errorf("tail = %q", got)
	}
}

func TestProgressReaderReportsOncePerMegabyte(t *testing.T) {
	var reports []int64
	p := &progressReader{r: strings.NewReader(strings.Repeat("x", 3<<20+5)), report: func(n int64) { reports = append(reports, n) }}
	if _, err := io.Copy(io.Discard, p); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Errorf("reports = %v, want one per megabyte crossed", reports)
	}
}

// The build tools' defaults run a real docker; they are exercised by hand, not
// here. What can be checked is that a missing program is recognized as such.
func TestAMissingProgramIsExecErrNotFound(t *testing.T) {
	tools := buildTools{}.withDefaults()
	err := tools.run(context.Background(), "", []string{"shipwick-no-such-program-0123456789"}, io.Discard)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", err)
	}
}

func TestDeployWithBuildRunsDockerWhereTheFileIs(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{Architecture: "x86_64"}
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &done}}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	docker := &fakeDocker{}
	f.build = docker.tools()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "name: my-api\nbuild:\n  context: api\n  dockerfile: docker/Dockerfile.prod\nport: 8080\n"
	if err := os.WriteFile(filepath.Join(dir, "services", "deploy.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, err := f.run(dir, "deploy", "-f", filepath.Join("services", "deploy.yaml")); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	// Paths in deploy.yaml are relative to the file: docker runs in its
	// directory, and -f names the Dockerfile from there, under the context.
	if docker.dir != "services" {
		t.Errorf("docker ran in %q, want the file's directory", docker.dir)
	}
	if docker.argv[5] != "api/docker/Dockerfile.prod" || docker.argv[8] != "api" {
		t.Errorf("argv = %q", docker.argv)
	}
}
