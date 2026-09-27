package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
)

// Volume is a named volume Shipwick created for an application.
type Volume struct {
	Name      string // Docker's name: shipwick_<app>_<volume>
	App       string
	Volume    string // the name in deploy.yaml
	SizeBytes int64  // -1 when the daemon does not report it
}

// volumePrefix is what VolumeName puts in front of an application's name.
const volumePrefix = "shipwick_"

// ParseVolumeName splits a Docker volume name into the application and the
// volume it was created for, the inverse of VolumeName. Neither part can
// contain an underscore, so the split is unambiguous.
func ParseVolumeName(name string) (app, volume string, err error) {
	rest, ok := strings.CutPrefix(name, volumePrefix)
	if !ok {
		return "", "", fmt.Errorf("%q is not a Shipwick volume: names look like %s<application>_<volume>", name, volumePrefix)
	}
	app, volume, ok = strings.Cut(rest, "_")
	if !ok || app == "" || volume == "" {
		return "", "", fmt.Errorf("%q is not a Shipwick volume: names look like %s<application>_<volume>", name, volumePrefix)
	}
	return app, volume, nil
}

// ListVolumes lists the volumes carrying Shipwick's labels, with their sizes.
// The listing itself does not report sizes; the daemon's disk-usage report
// does, one walk of every volume's files, so it is asked once for all of
// them. When it fails, the volumes are listed all the same, sized -1.
func (r *Runtime) ListVolumes(ctx context.Context) ([]Volume, error) {
	filters := client.Filters{}
	filters.Add("label", LabelManaged+"=true")
	res, err := r.cli.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	sizes := map[string]int64{}
	if usage, err := r.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true}); err == nil {
		for _, v := range usage.Volumes.Items {
			if v.UsageData != nil {
				sizes[v.Name] = v.UsageData.Size
			}
		}
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		app := v.Labels[LabelApp]
		volume, ok := strings.CutPrefix(v.Name, VolumeName(app, ""))
		if app == "" || !ok || volume == "" {
			continue // labelled by hand, or by a build that named volumes differently
		}
		size, known := sizes[v.Name]
		if !known {
			size = -1
		}
		out = append(out, Volume{Name: v.Name, App: app, Volume: volume, SizeBytes: size})
	}
	return out, nil
}
