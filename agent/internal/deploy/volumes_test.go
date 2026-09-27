package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func volumeApp() spec.App {
	a := app("db", "postgres:17", 1)
	a.Volumes = []spec.Volume{{Name: "data", Path: "/var/lib/data"}}
	a.Deploy.Strategy = spec.StrategyRecreate
	return a
}

// deployWithFiles deploys the stateful app and writes files into its volume.
func (h *harness) deployWithFiles(files map[string]string) (store.Deployment, string) {
	h.t.Helper()
	d := h.deploy(volumeApp())
	id := h.rt.Containers()[0].ID
	for name, content := range files {
		if err := h.rt.PutFile(id, "/var/lib/data/"+name, []byte(content)); err != nil {
			h.t.Fatalf("PutFile: %v", err)
		}
	}
	return d, id
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

func untar(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		data, _ := io.ReadAll(tr)
		out[hdr.Name] = string(data)
	}
}

func TestBackupStreamsTheVolumeAsATar(t *testing.T) {
	h := newHarness(t)
	h.deployWithFiles(map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"})
	ctx := context.Background()

	rc, err := h.engine.Backup(ctx, "db", "data")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	// The application is held for as long as the archive streams: nothing
	// may replace the container it is read through.
	if err := h.engine.Stop(ctx, "db"); !errors.Is(err, ErrBusy) {
		t.Errorf("Stop during a backup = %v, want ErrBusy", err)
	}
	got := untar(t, rc)
	rc.Close()
	if got["a.txt"] != "alpha" || got["sub/b.txt"] != "beta" || len(got) != 2 {
		t.Errorf("archive = %v", got)
	}
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Errorf("Stop after the backup closed: %v", err)
	}
}

func TestBackupOfAnUnknownVolumeFails(t *testing.T) {
	h := newHarness(t)
	h.deployWithFiles(nil)
	if _, err := h.engine.Backup(context.Background(), "db", "logs"); !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("err = %v, want ErrVolumeNotFound", err)
	}
	if _, err := h.engine.Backup(context.Background(), "nope", "data"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
	volumes, err := h.engine.Volumes(context.Background(), "db")
	if err != nil || len(volumes) != 1 || volumes[0] != (api.Volume{Name: "data", Path: "/var/lib/data"}) {
		t.Errorf("Volumes = %v, %v", volumes, err)
	}
}

func TestRestoreRefusesARunningApplication(t *testing.T) {
	h := newHarness(t)
	_, id := h.deployWithFiles(map[string]string{"a.txt": "alpha"})
	err := h.engine.Restore(context.Background(), "db", "data", bytes.NewReader(tarOf(t, map[string]string{"c.txt": "gamma"})))
	if !errors.Is(err, ErrNotStopped) {
		t.Fatalf("err = %v, want ErrNotStopped", err)
	}
	if files := h.rt.Files(id); string(files["/var/lib/data/a.txt"]) != "alpha" || len(h.rt.RemovedVolumes()) != 0 {
		t.Errorf("a refused restore must touch nothing: files %v, removed %v", files, h.rt.RemovedVolumes())
	}
}

func TestRestoreReplacesTheVolume(t *testing.T) {
	h := newHarness(t)
	d, old := h.deployWithFiles(map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	ctx := context.Background()
	if err := h.engine.Stop(ctx, "db"); err != nil {
		t.Fatal(err)
	}

	archive := tarOf(t, map[string]string{"c.txt": "gamma", "sub/d.txt": "delta"})
	if err := h.engine.Restore(ctx, "db", "data", bytes.NewReader(archive)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// A fresh volume behind a fresh container: what the archive did not
	// name is gone.
	if got := h.rt.RemovedVolumes(); len(got) != 1 || got[0] != "shipwick_db_data" {
		t.Errorf("removed volumes = %v", got)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 || containers[0].ID == old || containers[0].Running || containers[0].DeploymentID != d.ID {
		t.Fatalf("want one new, stopped container of the same deployment, got %+v", containers)
	}
	files := h.rt.Files(containers[0].ID)
	if len(files) != 2 || string(files["/var/lib/data/c.txt"]) != "gamma" || string(files["/var/lib/data/sub/d.txt"]) != "delta" {
		t.Errorf("files after restore = %v", keysOf(files))
	}
	replicas, _ := h.store.ListReplicas(ctx, d.ID)
	if len(replicas) != 1 || replicas[0].ContainerID != containers[0].ID {
		t.Errorf("replica rows = %+v, want the new container only", replicas)
	}

	events, _ := h.engine.Events(ctx, "db", 10)
	var restored bool
	for _, e := range events {
		restored = restored || strings.HasPrefix(e.Message, "Volume data restored from a backup (")
	}
	if !restored {
		t.Errorf("no restore event among %+v", events)
	}

	// The application stays stopped; start brings the new container up.
	if a, _ := h.engine.Application(ctx, "db"); a.Status != api.AppStopped {
		t.Errorf("status after restore = %s, want STOPPED", a.Status)
	}
	if err := h.engine.Start(ctx, "db"); err != nil {
		t.Fatalf("Start after restore: %v", err)
	}
	if c := h.rt.Containers()[0]; !c.Running {
		t.Errorf("container not running after start: %+v", c)
	}
}

func TestRestoreRefusesWhatIsNotATar(t *testing.T) {
	h := newHarness(t)
	_, id := h.deployWithFiles(map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	h.engine.Stop(ctx, "db")

	for _, body := range []string{"", "\x1f\x8b\x08\x00 gzip, not tar", strings.Repeat("x", 1024)} {
		err := h.engine.Restore(ctx, "db", "data", strings.NewReader(body))
		if !errors.Is(err, ErrInvalidArchive) {
			t.Errorf("Restore(%q…) = %v, want ErrInvalidArchive", body[:min(len(body), 8)], err)
		}
	}
	if files := h.rt.Files(id); string(files["/var/lib/data/a.txt"]) != "alpha" || len(h.rt.RemovedVolumes()) != 0 {
		t.Errorf("a refused archive must touch nothing: files %v, removed %v", files, h.rt.RemovedVolumes())
	}
}

func TestRestoreFailsFastWhileTheApplicationIsBusy(t *testing.T) {
	h := newHarness(t)
	h.deployWithFiles(nil)
	ctx := context.Background()

	rc, err := h.engine.Backup(ctx, "db", "data")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	err = h.engine.Restore(ctx, "db", "data", bytes.NewReader(tarOf(t, map[string]string{"c.txt": "gamma"})))
	if !errors.Is(err, ErrBusy) {
		t.Errorf("Restore during a backup = %v, want ErrBusy", err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
