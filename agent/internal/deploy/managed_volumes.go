package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// ErrNoSuchVolume means no volume by that name was created by Shipwick.
var ErrNoSuchVolume = errors.New("no volume by that name is managed by Shipwick; list them with: shipwick volumes")

// VolumeInUseError means the volume's application still exists. Its data is
// the application's for as long as it does; `shipwick delete` is the step
// that makes the volume the operator's to remove.
type VolumeInUseError struct {
	Volume string
	App    string
}

func (e *VolumeInUseError) Error() string {
	return fmt.Sprintf("the volume belongs to application %s; delete the application first — its data stays until the volume is removed", e.App)
}

// ManagedVolumes lists every volume Shipwick created, and whether the
// application it was created for still exists. A volume outlives its
// application on purpose (see Restore and the handbook); this is where the
// ones that did become visible.
func (e *Engine) ManagedVolumes(ctx context.Context) ([]api.VolumeInfo, error) {
	volumes, err := e.rt.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(apps))
	for _, a := range apps {
		exists[a.Name] = true
	}
	out := make([]api.VolumeInfo, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, api.VolumeInfo{Name: v.Name, Application: v.App, Volume: v.Volume, SizeBytes: v.SizeBytes, Orphan: !exists[v.App]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RemoveManagedVolume removes a volume of a deleted application, with
// everything in it. A volume whose application exists is refused: its data
// belongs to the application, and Restore is the way to replace it.
func (e *Engine) RemoveManagedVolume(ctx context.Context, name string) error {
	app, volume, err := docker.ParseVolumeName(name)
	if err != nil {
		return ErrNoSuchVolume
	}
	if spec.ValidateName(app) != nil || spec.ValidateName(volume) != nil {
		return ErrNoSuchVolume
	}
	volumes, err := e.rt.ListVolumes(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, v := range volumes {
		if v.Name == name {
			found = true
			break
		}
	}
	if !found {
		return ErrNoSuchVolume
	}
	// The application's existence is what decides, not whether a container
	// mounts the volume: a stopped application has its containers removed
	// and its volumes very much in use.
	if _, err := e.store.GetApplication(ctx, app); err == nil {
		return &VolumeInUseError{Volume: name, App: app}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := e.rt.RemoveVolume(ctx, app, volume); err != nil {
		return err
	}
	e.log.Info("volume of a deleted application removed", "volume", name, "app", app, "by", actorFrom(ctx))
	return nil
}
