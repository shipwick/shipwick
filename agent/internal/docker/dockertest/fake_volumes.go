package dockertest

import (
	"context"
	"sort"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// ListVolumes lists the volumes the fake knows, sized by their files. A
// volume exists from the moment a container mounting it is created, like the
// real runtime's, or from the first write into it.
func (f *Fake) ListVolumes(ctx context.Context) ([]docker.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]docker.Volume, 0, len(f.volumes))
	for name, files := range f.volumes {
		app, volume, err := docker.ParseVolumeName(name)
		if err != nil || docker.IsScratchVolume(volume) {
			continue
		}
		var size int64
		for _, content := range files {
			size += int64(len(content))
		}
		out = append(out, docker.Volume{Name: name, App: app, Volume: volume, SizeBytes: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
