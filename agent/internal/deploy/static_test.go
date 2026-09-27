package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// newStaticHarness is a routed harness whose engine keeps uploads.
func newStaticHarness(t *testing.T) (*supervised, *fakeProxy) {
	t.Helper()
	s, p := newRouted(t)
	s.engine.opts.UploadDir = t.TempDir()
	return s, p
}

func site(name string) spec.App {
	return spec.App{
		Name:      name,
		Domain:    name + ".example.com",
		Redirects: []string{"www." + name + ".example.com"},
		Replicas:  1,
		Static:    &spec.Static{Dir: "dist"},
		Restart:   spec.Restart{Policy: spec.RestartAlways},
		Deploy:    spec.Deploy{Strategy: spec.StrategyRolling},
	}
}

// upload stores an archive for the application and returns its digest.
func (s *supervised) upload(name string, files map[string]string) api.StaticUpload {
	s.t.Helper()
	up, err := s.engine.StoreStatic(context.Background(), name, bytes.NewReader(tarOf(s.t, files)))
	if err != nil {
		s.t.Fatalf("StoreStatic: %v", err)
	}
	return up
}

// deployStatic uploads the files and deploys them, to completion.
func (s *supervised) deployStatic(a spec.App, files map[string]string) store.Deployment {
	s.t.Helper()
	up := s.upload(a.Name, files)
	return s.finish(s.engine.DeployStatic(context.Background(), a, up.Digest))
}

// served lists the files under the proxy's directory for the application,
// relative to it: "<digest hex>/index.html".
func (s *supervised) served(name string) []string {
	var out []string
	for path := range s.rt.Files(dockertest.ProxyID) {
		if rel, ok := strings.CutPrefix(path, staticRoot+"/"+name+"/"); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func hexOf(up api.StaticUpload) string { return strings.TrimPrefix(up.Digest, "sha256:") }

func TestStaticDeploymentServesTheUploadedFolder(t *testing.T) {
	s, p := newStaticHarness(t)
	files := map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "console.log(1)"}
	up := s.upload("web", files)
	if !ValidStaticDigest(up.Digest) || up.Files != 2 || up.SizeBytes != int64(len(files["index.html"])+len(files["assets/app.js"])) {
		t.Fatalf("upload = %+v", up)
	}

	d := s.finish(s.engine.DeployStatic(context.Background(), site("web"), up.Digest))
	if d.Status != api.StatusActive || d.Error != "" {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}
	if d.Version != hexOf(up)[:12] || d.Image != "" || d.StaticDigest != up.Digest || d.StaticFiles != 2 {
		t.Errorf("record = version %q image %q digest %q files %d; want the digest's first 12 characters and no image", d.Version, d.Image, d.StaticDigest, d.StaticFiles)
	}

	want := []string{hexOf(up) + "/assets/app.js", hexOf(up) + "/index.html"}
	if got := s.served("web"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("files in the proxy = %v, want %v", got, want)
	}
	if len(s.rt.Containers()) != 0 {
		t.Errorf("a static application must create no container, got %d", len(s.rt.Containers()))
	}
	route := p.route(t, "web.example.com")
	if route.StaticRoot != staticDir("web", up.Digest) || len(route.Redirects) != 1 {
		t.Errorf("route = %+v, want the folder as its root and the redirect kept", route)
	}

	var states, steps []string
	for _, e := range s.events(d.ID) {
		switch e.Type {
		case api.EventState:
			states = append(states, e.Message)
		case api.EventStep:
			steps = append(steps, e.Message)
		}
	}
	if got := strings.Join(states, " "); got != "BUILDING STARTING HEALTH_CHECKING HEALTHY ACTIVE" {
		t.Errorf("states = %q", got)
	}
	if joined := strings.Join(steps, "\n"); !strings.Contains(joined, "Received 2 files") || !strings.Contains(joined, "Routed https://web.example.com to the uploaded files") {
		t.Errorf("steps = %q", joined)
	}

	apps, err := s.engine.Applications(context.Background())
	if err != nil || len(apps) != 1 {
		t.Fatalf("Applications = %v, %v", apps, err)
	}
	if !apps[0].Static || apps[0].Status != api.AppHealthy || apps[0].Replicas != (api.ReplicaCount{}) {
		t.Errorf("view = %+v; want static, HEALTHY, no replicas", apps[0])
	}
	view := DeploymentView(d)
	if view.Static == nil || view.Static.Files != 2 || view.Static.Digest != up.Digest {
		t.Errorf("deployment view = %+v", view.Static)
	}
}

func TestStaticDeploymentKeepsThePreviousFolderAndRemovesOlderOnes(t *testing.T) {
	s, p := newStaticHarness(t)
	v1 := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	v2 := s.deployStatic(site("web"), map[string]string{"index.html": "v2"})
	if v1.Status != api.StatusActive || v2.Status != api.StatusActive {
		t.Fatalf("v1 %s (%s), v2 %s (%s)", v1.Status, v1.Error, v2.Status, v2.Error)
	}
	if got := s.served("web"); len(got) != 2 {
		t.Fatalf("after two deployments both folders stay (the rollback target too), got %v", got)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != staticDir("web", v2.StaticDigest) {
		t.Errorf("route = %+v, want v2's folder", route)
	}

	v3 := s.deployStatic(site("web"), map[string]string{"index.html": "v3"})
	got := s.served("web")
	if len(got) != 2 || got[0] == strings.TrimPrefix(v1.StaticDigest, "sha256:")+"/index.html" || got[1] == strings.TrimPrefix(v1.StaticDigest, "sha256:")+"/index.html" {
		t.Errorf("after a third deployment v1's folder goes, v2's stays: %v", got)
	}
	removed := s.rt.ProxyRemoved()
	if len(removed) == 0 || !strings.HasSuffix(removed[len(removed)-1], strings.TrimPrefix(v1.StaticDigest, "sha256:")) {
		t.Errorf("removed = %v, want v1's folder last", removed)
	}
	if v3.Sequence != 3 {
		t.Errorf("sequence = %d", v3.Sequence)
	}
}

func TestStaticRollbackReRoutesToTheKeptFolderWithoutAnUpload(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	v1 := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v2"})
	// v2's upload replaced v1's: only the proxy has v1 now.
	if _, err := os.Stat(s.engine.uploadPath("web", v1.StaticDigest)); err == nil {
		t.Fatal("one upload is kept per application, the latest")
	}

	rb := s.finish(s.engine.Rollback(ctx, "web", 0))
	if rb.Status != api.StatusActive || rb.Kind != api.KindRollback || rb.StaticDigest != v1.StaticDigest || rb.Version != v1.Version {
		t.Fatalf("rollback = %s (%s) kind %s digest %s", rb.Status, rb.Error, rb.Kind, rb.StaticDigest)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != staticDir("web", v1.StaticDigest) {
		t.Errorf("route = %+v, want v1's folder", route)
	}
	if files := s.rt.Files(dockertest.ProxyID); string(files[staticDir("web", v1.StaticDigest)+"/index.html"]) != "v1" {
		t.Errorf("v1's files must be the ones served: %q", files)
	}
	var steps []string
	for _, e := range s.events(rb.ID) {
		if e.Type == api.EventStep {
			steps = append(steps, e.Message)
		}
	}
	if joined := strings.Join(steps, "\n"); !strings.Contains(joined, "already has") {
		t.Errorf("a rollback copies nothing: %q", joined)
	}

	// A redeploy of the same folder copies nothing either.
	rd := s.finish(s.engine.Redeploy(ctx, "web", ""))
	if rd.Status != api.StatusActive || rd.StaticDigest != v1.StaticDigest {
		t.Errorf("redeploy = %s (%s) digest %s", rd.Status, rd.Error, rd.StaticDigest)
	}
	if _, err := s.engine.Redeploy(ctx, "web", "nginx:1.27"); err == nil {
		t.Error("a static application has no image to redeploy with")
	}
}

func TestStaticRollbackFailsWhenTheFolderIsGone(t *testing.T) {
	ctx := context.Background()
	s, _ := newStaticHarness(t)
	v1 := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v2"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v3"}) // v1's folder is removed here

	rb := s.finish(s.engine.Rollback(ctx, "web", v1.ID))
	if rb.Status != api.StatusFailed || !strings.Contains(rb.Error, "no longer on the server") {
		t.Fatalf("rollback = %s (%s)", rb.Status, rb.Error)
	}
}

func TestStaticDeploymentFailsWithoutIndexHTML(t *testing.T) {
	s, p := newStaticHarness(t)
	v1 := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	d := s.deployStatic(site("web"), map[string]string{"readme.txt": "no site here"})
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "no index.html") {
		t.Fatalf("deployment = %s (%s)", d.Status, d.Error)
	}
	if got := s.served("web"); len(got) != 1 || !strings.HasPrefix(got[0], strings.TrimPrefix(v1.StaticDigest, "sha256:")) {
		t.Errorf("the half-extracted folder must be removed, leaving v1's: %v", got)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != staticDir("web", v1.StaticDigest) {
		t.Errorf("routing must stay with v1: %+v", route)
	}
	still, _ := s.store.GetDeployment(context.Background(), v1.ID)
	if still.Status != api.StatusActive {
		t.Errorf("v1 must stay active, got %s", still.Status)
	}
}

func TestStaticDeploymentNeedsItsUpload(t *testing.T) {
	ctx := context.Background()
	s, _ := newStaticHarness(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	if _, err := s.engine.DeployStatic(ctx, site("web"), digest); !errors.Is(err, ErrNoUpload) {
		t.Errorf("err = %v, want ErrNoUpload: nothing is recorded for an upload the agent never got", err)
	}
	if _, err := s.engine.DeployStatic(ctx, site("web"), "sha256:short"); err == nil {
		t.Error("a malformed digest must be refused")
	}
	if _, err := s.engine.DeployStatic(ctx, web("web:1.0", 1), digest); err == nil {
		t.Error("a container application is not deployed with a digest")
	}
	if deployments, _ := s.store.ListDeployments(ctx, store.DeploymentFilter{}); len(deployments) != 0 {
		t.Errorf("nothing may be recorded, got %d deployments", len(deployments))
	}

	// The archive goes missing between the request and the rollout: the
	// deployment fails, and says what to do.
	up := s.upload("web", map[string]string{"index.html": "v1"})
	os.Remove(s.engine.uploadPath("web", up.Digest))
	d := s.finish(s.engine.DeployStatic(ctx, site("web"), up.Digest))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "no files were uploaded") {
		t.Errorf("deployment = %s (%s)", d.Status, d.Error)
	}
}

func TestStaticDeploymentNeedsTheProxyInDocker(t *testing.T) {
	s, _ := newStaticHarness(t)
	s.rt.ProxyErr = errors.New("the proxy runs outside Docker; static applications need the compose setup")
	d := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "outside Docker") {
		t.Errorf("deployment = %s (%s)", d.Status, d.Error)
	}
}

func TestStaticUploadIsInspected(t *testing.T) {
	s, _ := newStaticHarness(t)
	ctx := context.Background()
	link := func(name, target string) []byte {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "index.html", Mode: 0o644})
		w.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777})
		w.Close()
		return buf.Bytes()
	}
	tests := map[string][]byte{
		"not a tar":          []byte("<html>"),
		"empty":              tarOf(t, map[string]string{}),
		"outside the folder": tarOf(t, map[string]string{"index.html": "", "../etc/passwd": "root"}),
		"absolute path":      tarOf(t, map[string]string{"index.html": "", "/etc/passwd": "root"}),
		"symbolic link":      link("certs", "/data/caddy"),
	}
	for name, archive := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := s.engine.StoreStatic(ctx, "web", bytes.NewReader(archive))
			var bad *InvalidUploadError
			if !errors.As(err, &bad) {
				t.Errorf("err = %v, want an InvalidUploadError", err)
			}
		})
	}
	entries, _ := os.ReadDir(filepath.Join(s.engine.opts.UploadDir, "web"))
	if len(entries) != 0 {
		t.Errorf("a refused upload leaves nothing behind, got %v", entries)
	}

	// Directory entries and "./" prefixes, as other tar writers make them, are fine.
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o755})
	w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./css/", Mode: 0o755})
	w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "./index.html", Mode: 0o644, Size: 2})
	w.Write([]byte("hi"))
	w.Close()
	up, err := s.engine.StoreStatic(ctx, "web", &buf)
	if err != nil || up.Files != 1 || up.SizeBytes != 2 {
		t.Errorf("upload = %+v, %v", up, err)
	}
	entries, _ = os.ReadDir(filepath.Join(s.engine.opts.UploadDir, "web"))
	if len(entries) != 2 {
		t.Errorf("the archive and its note are kept, nothing else: %v", entries)
	}
}

func TestStaticApplicationHasNoContainersToAsk(t *testing.T) {
	ctx := context.Background()
	s, _ := newStaticHarness(t)
	s.deployStatic(site("web"), map[string]string{"index.html": "v1"})

	if _, err := s.engine.Logs(ctx, "web", 10); !errors.Is(err, ErrStaticApplication) {
		t.Errorf("Logs: %v", err)
	}
	if _, err := s.engine.LogStream(ctx, "web", 10); !errors.Is(err, ErrStaticApplication) {
		t.Errorf("LogStream: %v", err)
	}
	if _, err := s.engine.Metrics(ctx, "web"); !errors.Is(err, ErrStaticApplication) {
		t.Errorf("Metrics: %v", err)
	}
	if _, err := s.engine.RunCommand(ctx, "web", []string{"ls"}); !errors.Is(err, ErrStaticApplication) {
		t.Errorf("RunCommand: %v", err)
	}
	if _, err := s.engine.Jobs(ctx, "web"); !errors.Is(err, ErrStaticApplication) {
		t.Errorf("Jobs: %v", err)
	}
	if volumes, err := s.engine.Volumes(ctx, "web"); err != nil || len(volumes) != 0 {
		t.Errorf("Volumes = %v, %v; a static application has none, which is not an error", volumes, err)
	}

	// The supervisor has nothing to keep alive, and must not try.
	for i := 0; i < 5; i++ {
		s.advance(time.Second)
	}
	if n := len(s.rt.Containers()); n != 0 {
		t.Errorf("the supervisor created %d containers for a static application", n)
	}
	events, _ := s.engine.Events(ctx, "web", 50)
	if len(events) != 0 {
		t.Errorf("the supervisor must have nothing to say: %+v", events)
	}
}

func TestStaticApplicationStopsAndStarts(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	d := s.deployStatic(site("web"), map[string]string{"index.html": "v1"})

	if err := s.engine.Stop(ctx, "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != "" {
		t.Errorf("a stopped static application keeps its route and answers 503: %+v", route)
	}
	apps, _ := s.engine.Applications(ctx)
	if apps[0].Status != api.AppStopped {
		t.Errorf("status = %s, want STOPPED", apps[0].Status)
	}
	if err := s.engine.Start(ctx, "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != staticDir("web", d.StaticDigest) {
		t.Errorf("after start the files serve again: %+v", route)
	}
}

func TestDeleteRemovesTheFoldersAndTheUpload(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	s.deployStatic(site("web"), map[string]string{"index.html": "v1"})
	s.deployStatic(site("web"), map[string]string{"index.html": "v2"})

	if err := s.engine.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := s.served("web"); len(got) != 0 {
		t.Errorf("files left in the proxy: %v", got)
	}
	if _, err := os.Stat(filepath.Join(s.engine.opts.UploadDir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the upload directory must go with the application: %v", err)
	}
	if p.hasRoute("web.example.com") {
		t.Error("the route must be gone")
	}
}

func TestContainerApplicationBecomesStaticAndBack(t *testing.T) {
	ctx := context.Background()
	s, p := newStaticHarness(t)
	s.deploy(web("web:1.0", 2))
	a := site("web")
	d := s.deployStatic(a, map[string]string{"index.html": "v2"})
	if d.Status != api.StatusActive {
		t.Fatalf("static deployment: %s (%s)", d.Status, d.Error)
	}
	if n := len(s.rt.Containers()); n != 0 {
		t.Errorf("the container version must be retired, %d containers left", n)
	}
	if route := p.route(t, "web.example.com"); route.StaticRoot != staticDir("web", d.StaticDigest) {
		t.Errorf("route = %+v", route)
	}

	back := s.deploy(web("web:1.1", 1))
	if back.Status != api.StatusActive {
		t.Fatalf("container deployment: %s (%s)", back.Status, back.Error)
	}
	route := p.route(t, "web.example.com")
	if route.StaticRoot != "" || len(p.upstreams("web.example.com")) != 1 {
		t.Errorf("route = %+v upstreams %v; want the replica, no folder", route, p.upstreams("web.example.com"))
	}
	if _, err := s.engine.Logs(ctx, "web", 10); err != nil {
		t.Errorf("a container application has logs again: %v", err)
	}
}
