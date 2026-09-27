package commands

import (
	"archive/tar"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const staticDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeStatic is the static-folder side of fakeAgent: what PUT …/static
// received, entry by entry.
type fakeStatic struct {
	mu      sync.Mutex
	entries []*tar.Header
	content map[string]string
	refuse  *api.Error // PUT answers this instead
}

func (s *fakeStatic) register(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/applications/{name}/static", func(w http.ResponseWriter, r *http.Request) {
		if s.refuse != nil {
			respondError(w, 413, *s.refuse)
			return
		}
		if r.Header.Get("Content-Type") != "application/x-tar" {
			respondError(w, 400, api.Error{Code: api.CodeInvalidRequest, Message: "not a tar"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.content = map[string]string{}
		var size int64
		files := 0
		tr := tar.NewReader(r.Body)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			s.entries = append(s.entries, hdr)
			if hdr.Typeflag == tar.TypeReg {
				data, _ := io.ReadAll(tr)
				s.content[hdr.Name] = string(data)
				files++
				size += hdr.Size
			}
		}
		respond(w, 200, api.StaticUpload{Digest: staticDigest, SizeBytes: size, Files: files})
	})
}

const staticConfig = "name: web\nstatic: dist/\ndomain: example.com\n"

// staticSite writes a deploy.yaml and a built folder next to it.
func staticSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := writeConfig(t, staticConfig)
	for name, content := range files {
		path := filepath.Join(dir, "dist", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDeployStaticUploadsTheFolderFirst(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Status: api.StatusActive, Version: "0123456789ab", CompletedAt: &done,
			Static: &api.StaticFiles{Digest: staticDigest, Files: 2, SizeBytes: 25}},
		Events: []api.Event{
			event(1, api.EventState, api.LevelInfo, "BUILDING"),
			event(2, api.EventStep, api.LevelInfo, "Received 2 files (25 B)"),
			event(3, api.EventStep, api.LevelInfo, "Routed https://example.com to the uploaded files"),
		},
		Spec: spec.App{Domain: "example.com", Static: &spec.Static{Dir: "dist"}},
	}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "web", Static: true}}
	dir := staticSite(t, map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "console.log(1)"})
	if err := os.MkdirAll(filepath.Join(dir, "dist", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := f.run(dir, "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s%s", err, out, errOut)
	}
	assertInOrder(t, out, []string{
		"✓ Validated deploy.yaml",
		"✓ Uploaded dist/: 2 files, 25 B",
		"✓ Received 2 files",
		"✓ Routed https://example.com to the uploaded files",
		"web 0123456789ab",
		"served by the proxy",
		"https://example.com",
	})
	if strings.Contains(out, "Uploading") || strings.Contains(out, "replicas healthy") {
		t.Errorf("piped output carries no progress line, and no replica count for a folder:\n%s", out)
	}

	// The folder goes first; the deployment names what the agent kept.
	var order []string
	for _, r := range f.requests {
		if strings.Contains(r, "/applications/web/") {
			order = append(order, r)
		}
	}
	want := []string{"PUT /api/v1/applications/web/static", "POST /api/v1/applications/web/deploy?static=" + url.QueryEscape(staticDigest)}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests = %v, want %v", order, want)
	}
	if f.deployBodies[0] != staticConfig {
		t.Errorf("the agent should receive deploy.yaml verbatim, got:\n%s", f.deployBodies[0])
	}

	var names []string
	for _, h := range f.static.entries {
		names = append(names, h.Name)
		if !h.ModTime.Equal(time.Unix(0, 0)) || h.Uid != 0 || h.Uname != "" {
			t.Errorf("entry %s carries the machine's metadata (%v, uid %d %q); the same folder must make the same archive everywhere", h.Name, h.ModTime, h.Uid, h.Uname)
		}
		if h.Typeflag == tar.TypeReg && h.Mode != 0o644 || h.Typeflag == tar.TypeDir && h.Mode != 0o755 {
			t.Errorf("entry %s has mode %o", h.Name, h.Mode)
		}
	}
	if got := strings.Join(names, " "); got != "assets/ assets/app.js empty/ index.html" {
		t.Errorf("entries = %q; want every file and directory, relative to the folder, sorted, forward slashes", got)
	}
	if f.static.content["index.html"] != "<h1>hi</h1>" || f.static.content["assets/app.js"] != "console.log(1)" {
		t.Errorf("contents = %v", f.static.content)
	}
}

func TestDeployStaticRefusesAFolderWithoutIndexHTML(t *testing.T) {
	f := newFakeAgent(t)
	_, _, err := f.run(staticSite(t, map[string]string{"readme.txt": "build me"}), "deploy")
	if err == nil || !strings.Contains(err.Error(), "no index.html") || !strings.Contains(err.Error(), "Build the site first") {
		t.Errorf("err = %v", err)
	}
	_, _, err = f.run(writeConfig(t, staticConfig), "deploy")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing folder: err = %v", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/static") || strings.Contains(r, "/deploy") {
			t.Errorf("nothing may be sent for a folder that cannot be served, saw %s", r)
		}
	}
}

func TestDeployStaticRefusesAFolderOverTheSizeCap(t *testing.T) {
	f := newFakeAgent(t)
	dir := staticSite(t, map[string]string{"index.html": "<h1>hi</h1>"})
	// Extended without being written: the size is all the check reads.
	big, err := os.Create(filepath.Join(dir, "dist", "video.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(spec.MaxStaticBytes + 1); err != nil {
		big.Close()
		t.Skipf("cannot make a sparse file here: %v", err)
	}
	big.Close()

	_, _, err = f.run(dir, "deploy")
	if err == nil || !strings.Contains(err.Error(), "larger than 512 MB") {
		t.Errorf("err = %v", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/static") {
			t.Error("a folder over the cap must not be uploaded")
		}
	}
}

func TestDeployStaticReportsARefusedUpload(t *testing.T) {
	f := newFakeAgent(t)
	f.static.refuse = &api.Error{Code: api.CodeInvalidRequest, Message: "the folder exceeds 512 MB"}
	_, _, err := f.run(staticSite(t, map[string]string{"index.html": "<h1>hi</h1>"}), "deploy")
	if got := Render(err); !strings.Contains(got, "the folder exceeds 512 MB") {
		t.Errorf("rendering = %q", got)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/deploy") {
			t.Error("a refused upload must not be followed by a deployment")
		}
	}
}

func TestValidateDescribesAStaticApplication(t *testing.T) {
	f := newFakeAgent(t)
	out, _, err := f.run(writeConfig(t, staticConfig), "validate")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"deploy.yaml is valid", "Folder", "dist/ — served by the proxy, no container", "Domain", "example.com"})
	if strings.Contains(out, "Image") || strings.Contains(out, "Replicas") || strings.Contains(out, "Restart") {
		t.Errorf("a folder has no image, replicas or restart policy:\n%s", out)
	}
}

func TestStatusAndPsOfAStaticApplication(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{
		Application: api.Application{Name: "web", Status: api.AppHealthy, Domain: "example.com", Version: "0123456789ab", Static: true,
			UpdatedAt: fixedNow.Add(-5 * time.Minute)},
		Spec:             &spec.App{Domain: "example.com", Static: &spec.Static{Dir: "dist"}},
		ActiveDeployment: &api.Deployment{Sequence: 2, Version: "0123456789ab", StartedAt: fixedNow, CompletedAt: &fixedNow, Static: &api.StaticFiles{Digest: staticDigest, Files: 42, SizeBytes: 3_250_000}},
	}

	out, _, err := f.run(t.TempDir(), "status", "web")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"web", "● HEALTHY", "0123456789ab", "https://example.com", "Files", "42 files, 3.1 MB, served by the proxy"})
	if strings.Contains(out, "healthy\n") || strings.Contains(out, "Limits") || strings.Contains(out, "REPLICA") {
		t.Errorf("no replica count, limits or container table for a folder:\n%s", out)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/metrics") {
			t.Error("a static application has no metrics to ask for")
		}
	}

	out, _, err = f.run(t.TempDir(), "ps")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"NAME", "REPLICAS", "web", "HEALTHY", "0123456789ab", "static", "example.com"})
}

func TestStaticApplicationErrorIsExplained(t *testing.T) {
	err := &client.APIError{Status: 409, Code: api.CodeStaticApplication, Message: "this application is a folder served by the proxy; it has no containers"}
	got := Render(err)
	if !strings.Contains(got, "folder served by the proxy") || !strings.Contains(got, "shipwick status") {
		t.Errorf("rendering = %q", got)
	}
}
