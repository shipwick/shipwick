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

const statefulConfig = `
name: db
image: postgres:17
port: 5432
replicas: 1
volumes:
  - name: data
    path: /var/lib/data
deploy:
  strategy: recreate
`

// deployStateful deploys the config above and seeds its volume; it returns
// the container the volume is read through.
func (f *fixture) deployStateful(files map[string]string) string {
	f.t.Helper()
	if status, body := f.do("POST", "/api/v1/applications/db/deploy", statefulConfig); status != http.StatusAccepted {
		f.t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	id := f.rt.Containers()[0].ID
	for name, content := range files {
		if err := f.rt.PutFile(id, "/var/lib/data/"+name, []byte(content)); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

// put sends a restore upload with the given content type.
func (f *fixture) put(path, contentType string, body []byte) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPut, f.srv.URL+path, bytes.NewReader(body))
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

func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, content := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, content)
	}
	w.Close()
	return buf.Bytes()
}

func TestListVolumes(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(nil)
	status, body := f.do("GET", "/api/v1/applications/db/volumes", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	volumes := decode[[]api.Volume](t, body)
	if len(volumes) != 1 || volumes[0] != (api.Volume{Name: "data", Path: "/var/lib/data"}) {
		t.Errorf("volumes = %+v", volumes)
	}
}

func TestBackupStreamsATarArchive(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(map[string]string{"a.txt": "alpha"})

	req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/applications/db/volumes/data/archive", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="db-data-`) || !strings.HasSuffix(cd, `.tar"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}

	tr := tar.NewReader(resp.Body)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("the body is not a tar archive: %v", err)
	}
	content, _ := io.ReadAll(tr)
	if hdr.Name != "a.txt" || string(content) != "alpha" {
		t.Errorf("first entry = %s %q", hdr.Name, content)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("want one entry, next = %v", err)
	}
}

func TestBackupOfAnUnknownVolumeIs404(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(nil)
	status, body := f.do("GET", "/api/v1/applications/db/volumes/logs/archive", "")
	if status != http.StatusNotFound || decodeError(t, body).Code != api.CodeNotFound {
		t.Errorf("status = %d, body = %s", status, body)
	}
	status, body = f.do("GET", "/api/v1/applications/db/volumes/Not_Valid/archive", "")
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidRequest {
		t.Errorf("status = %d, body = %s", status, body)
	}
}

func TestRestoreNeedsAStoppedApplication(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(map[string]string{"a.txt": "alpha"})
	archive := tarOf(t, map[string]string{"c.txt": "gamma"})

	status, body := f.put("/api/v1/applications/db/volumes/data/archive", "application/x-tar", archive)
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeApplicationRunning {
		t.Fatalf("restore while running: status = %d, body = %s", status, body)
	}

	if status, body := f.do("POST", "/api/v1/applications/db/stop", ""); status != http.StatusOK {
		t.Fatalf("stop: %d %s", status, body)
	}
	status, body = f.put("/api/v1/applications/db/volumes/data/archive", "application/x-tar", archive)
	if status != http.StatusNoContent {
		t.Fatalf("restore: status = %d, body = %s", status, body)
	}
	files := f.rt.Files(f.rt.Containers()[0].ID)
	if len(files) != 1 || string(files["/var/lib/data/c.txt"]) != "gamma" {
		t.Errorf("files after restore = %v", files)
	}
	if status, body := f.do("POST", "/api/v1/applications/db/start", ""); status != http.StatusOK {
		t.Errorf("start after restore: %d %s", status, body)
	}
}

func TestRestoreRejectsWhatIsNotATar(t *testing.T) {
	f := newFixture(t)
	f.deployStateful(map[string]string{"a.txt": "alpha"})
	f.do("POST", "/api/v1/applications/db/stop", "")

	status, body := f.put("/api/v1/applications/db/volumes/data/archive", "application/gzip", tarOf(t, nil))
	if status != http.StatusBadRequest {
		t.Errorf("wrong content type: status = %d, body = %s", status, body)
	}
	status, body = f.put("/api/v1/applications/db/volumes/data/archive", "application/x-tar", []byte("not a tar at all"))
	if status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidRequest {
		t.Errorf("garbage body: status = %d, body = %s", status, body)
	}
	if len(f.rt.RemovedVolumes()) != 0 {
		t.Errorf("a rejected upload must not touch the volume")
	}
}
