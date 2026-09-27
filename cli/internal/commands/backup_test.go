package commands

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

// volumeAgent answers the volume endpoints in front of the fake agent, which
// keeps serving everything else.
type volumeAgent struct {
	*fakeAgent
	volumes  []api.Volume
	archives map[string][]byte // by volume: what a backup streams

	mu       sync.Mutex
	restored map[string][]byte // by volume: what a restore uploaded
	types    []string          // Content-Type of each upload
}

func newVolumeAgent(t *testing.T, volumes ...api.Volume) *volumeAgent {
	t.Helper()
	v := &volumeAgent{fakeAgent: newFakeAgent(t), volumes: volumes, archives: map[string][]byte{}, restored: map[string][]byte{}}
	for _, vol := range volumes {
		v.archives[vol.Name] = tarOf(t, map[string]string{vol.Name + ".txt": "contents of " + vol.Name})
	}
	upstream, _ := url.Parse(v.srv.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/applications/{name}/volumes", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, v.volumes)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/volumes/{volume}/archive", func(w http.ResponseWriter, r *http.Request) {
		archive, ok := v.archives[r.PathValue("volume")]
		if !ok {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "the application has no volume by that name"})
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", `attachment; filename="x.tar"`)
		w.Write(archive)
	})
	mux.HandleFunc("PUT /api/v1/applications/{name}/volumes/{volume}/archive", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.restored[r.PathValue("volume")] = body
		v.types = append(v.types, r.Header.Get("Content-Type"))
		v.mu.Unlock()
		if v.app.Replicas.Running > 0 {
			respondError(w, 409, api.Error{Code: api.CodeApplicationRunning, Message: "the application is running; stop it first with: shipwick stop"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", proxy)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	v.srv = srv
	return v
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

func TestBackupWritesOneArchivePerVolume(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"}, api.Volume{Name: "uploads", Path: "/srv/uploads"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppHealthy, Replicas: api.ReplicaCount{Desired: 1, Running: 1, Healthy: 1}}}
	dir := t.TempDir()

	out, errOut, err := f.run(t.TempDir(), "backup", "my-api", "-o", dir)
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	for _, volume := range []string{"data", "uploads"} {
		name := "my-api-" + volume + "-20260301-120000.tar"
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s not written: %v\n%s", name, err, out)
		}
		if !bytes.Equal(data, f.archives[volume]) {
			t.Errorf("%s does not hold what the agent streamed", name)
		}
		if !strings.Contains(out, name) {
			t.Errorf("output does not name %s:\n%s", name, out)
		}
	}
	if !strings.Contains(errOut, "my-api is running") || !strings.Contains(errOut, "shipwick run my-api") {
		t.Errorf("a running application deserves the consistency warning, got:\n%s", errOut)
	}
}

func TestBackupOfAStoppedApplicationDoesNotWarn(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppStopped, DesiredState: api.DesiredStopped}}

	out, errOut, err := f.run(t.TempDir(), "backup", "my-api", "--volume", "data", "-o", t.TempDir())
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if errOut != "" {
		t.Errorf("unexpected warning:\n%s", errOut)
	}
}

func TestBackupOfAnUnknownVolumeNamesTheOthers(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}

	_, _, err := f.run(t.TempDir(), "backup", "my-api", "--volume", "logs")
	if err == nil || !strings.Contains(err.Error(), `no volume "logs"`) || !strings.Contains(err.Error(), "data") {
		t.Errorf("err = %v", err)
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Errorf("nothing should have been written: %v", entries)
	}
}

func TestRestoreAsksForConfirmationAndUploads(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppStopped, DesiredState: api.DesiredStopped}}
	archive := tarOf(t, map[string]string{"restored.txt": "from the backup"})
	path := filepath.Join(t.TempDir(), "my-api-data-20260301-120000.tar")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}

	// Standard input is not a terminal here: no --yes, no restore.
	_, _, err := f.run(t.TempDir(), "restore", "my-api", path)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want a refusal that names --yes", err)
	}
	if len(f.restored) != 0 {
		t.Fatal("nothing may be uploaded without confirmation")
	}

	out, _, err := f.run(t.TempDir(), "restore", "my-api", path, "--yes")
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if !bytes.Equal(f.restored["data"], archive) || f.types[0] != "application/x-tar" {
		t.Errorf("uploaded %d bytes as %q, want the archive as application/x-tar", len(f.restored["data"]), f.types)
	}
	assertInOrder(t, out, []string{"Restored volume data of my-api", "shipwick start my-api"})
}

func TestRestoreRefusesWhatIsNotATar(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	path := filepath.Join(t.TempDir(), "dump.sql")
	os.WriteFile(path, []byte(strings.Repeat("INSERT INTO t VALUES (1);\n", 40)), 0o600)

	_, _, err := f.run(t.TempDir(), "restore", "my-api", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "not a tar archive") {
		t.Errorf("err = %v", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("the agent must not be asked at all, got %v", f.requests)
	}
}

func TestRestoreNeedsTheVolumeNamedWhenThereAreSeveral(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"}, api.Volume{Name: "uploads", Path: "/srv/uploads"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	path := filepath.Join(t.TempDir(), "backup.tar")
	os.WriteFile(path, tarOf(t, map[string]string{"x": "y"}), 0o600)

	_, _, err := f.run(t.TempDir(), "restore", "my-api", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "--volume") || !strings.Contains(err.Error(), "data, uploads") {
		t.Errorf("err = %v", err)
	}
}

func TestRestoreOfARunningApplicationSaysToStopIt(t *testing.T) {
	f := newVolumeAgent(t, api.Volume{Name: "data", Path: "/var/lib/data"})
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Running: 1}}}
	path := filepath.Join(t.TempDir(), "backup.tar")
	os.WriteFile(path, tarOf(t, map[string]string{"x": "y"}), 0o600)

	_, _, err := f.run(t.TempDir(), "restore", "my-api", path, "--yes")
	if msg := Render(err); !strings.Contains(msg, "shipwick stop") {
		t.Errorf("Render(%v) = %q", err, msg)
	}
}
