package api

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const staticConfig = "name: web\nstatic: dist/\ndomain: web.example.com\n"

func siteArchive(files map[string]string) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, content := range files {
		w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(content))})
		w.Write([]byte(content))
	}
	w.Close()
	return buf.Bytes()
}

// upload sends a folder and returns the status and decoded body.
func (f *fixture) upload(name, contentType string, archive []byte) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPut, f.srv.URL+"/api/v1/applications/"+name+"/static", bytes.NewReader(archive))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestStaticUploadAndDeploy(t *testing.T) {
	f := newFixture(t)
	f.engine.Wait()

	status, body := f.upload("web", "application/x-tar", siteArchive(map[string]string{"index.html": "<h1>hi</h1>", "app.js": "1"}))
	if status != http.StatusOK {
		t.Fatalf("upload: status = %d: %s", status, body)
	}
	up := decode[api.StaticUpload](t, body)
	if !strings.HasPrefix(up.Digest, "sha256:") || len(up.Digest) != 71 || up.Files != 2 || up.SizeBytes != 12 {
		t.Errorf("upload = %+v", up)
	}

	status, body = f.do("POST", "/api/v1/applications/web/deploy?static="+up.Digest, staticConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d: %s", status, body)
	}
	d := decode[api.Deployment](t, body)
	if d.Version != strings.TrimPrefix(up.Digest, "sha256:")[:12] || d.Image != "" || d.Static == nil || d.Static.Digest != up.Digest || d.Static.Files != 2 {
		t.Errorf("deployment = %+v static %+v", d, d.Static)
	}
	f.engine.Wait()

	status, body = f.do("GET", "/api/v1/applications/web", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	app := decode[api.ApplicationDetail](t, body)
	if !app.Static || app.Status != api.AppHealthy || app.Replicas.Desired != 0 || len(app.Containers) != 0 {
		t.Errorf("application = %+v", app.Application)
	}
	if app.Spec == nil || app.Spec.Static == nil || app.Spec.Static.Dir != "dist" {
		t.Errorf("spec = %+v", app.Spec)
	}

	status, body = f.do("GET", "/api/v1/applications/web/logs", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeStaticApplication {
		t.Errorf("logs of a static application: status %d %+v, want 409 STATIC_APPLICATION", status, e)
	}
}

func TestStaticUploadIsRefusedWhenItIsNotATar(t *testing.T) {
	f := newFixture(t)
	status, body := f.upload("web", "application/json", []byte(`{}`))
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "application/x-tar") {
		t.Errorf("wrong content type: %d %+v", status, e)
	}
	status, body = f.upload("web", "application/x-tar", []byte("<html>not a tar</html>"))
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
		t.Errorf("not a tar: %d %+v", status, e)
	}
}

func TestStaticDeployNeedsAnUploadedDigest(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/applications/web/deploy", staticConfig)
	if e := decodeError(t, body); status != http.StatusBadRequest || !strings.Contains(e.Message, "static=") {
		t.Errorf("no digest: %d %+v", status, e)
	}
	status, body = f.do("POST", "/api/v1/applications/web/deploy?static=sha256:"+strings.Repeat("0", 64), staticConfig)
	if e := decodeError(t, body); status != http.StatusNotFound || !strings.Contains(e.Message, "no files were uploaded") {
		t.Errorf("unknown digest: %d %+v", status, e)
	}
	status, body = f.do("POST", "/api/v1/applications/my-api/deploy?static=sha256:"+strings.Repeat("0", 64), validConfig)
	if e := decodeError(t, body); status != http.StatusBadRequest {
		t.Errorf("a container application with a digest: %d %+v", status, e)
	}
	// Nothing was recorded by any of those.
	status, body = f.do("GET", "/api/v1/deployments", "")
	if ds := decode[[]api.Deployment](t, body); status != http.StatusOK || len(ds) != 0 {
		t.Errorf("deployments = %v", ds)
	}
}
