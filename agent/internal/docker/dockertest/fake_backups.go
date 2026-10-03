package dockertest

import (
	"context"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// RemoveScratchVolumes removes the scratch volumes no container mounts, like
// the real runtime.
func (f *Fake) RemoveScratchVolumes(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inUse := map[string]bool{}
	for _, spec := range f.specs {
		for _, m := range spec.Mounts {
			inUse[docker.VolumeName(spec.App, m.Volume)] = true
		}
	}
	removed := 0
	for name := range f.volumes {
		if docker.IsScratchVolume(name) && !inUse[name] {
			delete(f.volumes, name)
			f.removedVolumes = append(f.removedVolumes, name)
			removed++
		}
	}
	return removed, nil
}

// ScratchVolumes lists the scratch volumes that exist, by Docker name.
func (f *Fake) ScratchVolumes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for name := range f.volumes {
		if docker.IsScratchVolume(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// VolumeFiles returns the files of a Docker volume, by path relative to its
// mount point.
func (f *Fake) VolumeFiles(name string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for rel, content := range f.volumes[name] {
		out[strings.TrimPrefix(rel, "/")] = string(content)
	}
	return out
}
