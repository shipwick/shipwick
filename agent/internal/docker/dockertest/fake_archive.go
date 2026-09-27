package dockertest

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// The fake's filesystem: a container sees its own files, and at each mount
// point the files of the named volume, which outlive the container the way a
// Docker volume does.

// PutFile writes a file into a container, at an absolute path. Under a mount
// point it lands in the volume.
func (f *Fake) PutFile(id, path string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	files, rel, err := f.locate(id, path)
	if err != nil {
		return err
	}
	files[rel] = append([]byte(nil), content...)
	return nil
}

// Files returns every file a container sees, by absolute path: its own and
// those of its volumes.
func (f *Fake) Files(id string) map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]byte{}
	for rel, content := range f.files[id] {
		out["/"+rel] = content
	}
	for _, m := range f.specs[id].Mounts {
		for rel, content := range f.volumes[docker.VolumeName(f.specs[id].App, m.Volume)] {
			out[m.Path+"/"+rel] = content
		}
	}
	return out
}

// VolumeExists reports whether the application's volume holds data or was
// ever written to since it was last removed.
func (f *Fake) VolumeExists(app, volume string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.volumes[docker.VolumeName(app, volume)]
	return ok
}

// RemovedVolumes lists the volumes removed so far, in order.
func (f *Fake) RemovedVolumes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removedVolumes...)
}

// locate finds the file map a path belongs to — a volume's, when the path is
// under one of the container's mounts, else the container's own — and the
// path relative to it. Called with f.mu held.
func (f *Fake) locate(id, path string) (files map[string][]byte, rel string, err error) {
	spec, ok := f.specs[id]
	if !ok {
		return nil, "", docker.ErrNotFound
	}
	for _, m := range spec.Mounts {
		if path == m.Path || strings.HasPrefix(path, m.Path+"/") {
			name := docker.VolumeName(spec.App, m.Volume)
			if f.volumes[name] == nil {
				f.volumes[name] = map[string][]byte{}
			}
			return f.volumes[name], strings.TrimPrefix(strings.TrimPrefix(path, m.Path), "/"), nil
		}
	}
	if f.files[id] == nil {
		f.files[id] = map[string][]byte{}
	}
	return f.files[id], strings.TrimPrefix(path, "/"), nil
}

// ExportPath streams a tar archive of the files under path, named relative
// to it, like the real runtime after rebasing. The container may be stopped.
func (f *Fake) ExportPath(_ context.Context, id, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	files, rel, err := f.locate(id, path)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if rel != "" {
		prefix = rel + "/"
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, name := range names {
		hdr := &tar.Header{Name: strings.TrimPrefix(name, prefix), Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}
		if err := w.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return io.NopCloser(&buf), nil
}

// ImportPath extracts a tar archive under path. Existing files that the
// archive does not name are kept, as Docker keeps them.
func (f *Fake) ImportPath(_ context.Context, id, path string, archive io.Reader) error {
	r := tar.NewReader(archive)
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid tar archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(strings.TrimPrefix(hdr.Name, "./"), "/")
		if err := f.PutFile(id, path+"/"+name, content); err != nil {
			return err
		}
	}
}

// RemoveVolume drops an application's volume and its files. Like the daemon,
// it refuses while a container — running or not — still mounts it.
func (f *Fake) RemoveVolume(_ context.Context, app, volume string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := docker.VolumeName(app, volume)
	for id, spec := range f.specs {
		for _, m := range spec.Mounts {
			if spec.App == app && m.Volume == volume {
				return fmt.Errorf("volume %s is in use by container %s", name, f.containers[id].Name)
			}
		}
	}
	delete(f.volumes, name)
	f.removedVolumes = append(f.removedVolumes, name)
	return nil
}
