package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// VerifyJob is the job name of the container a backup is verified in. It is
// a job as far as everything that manages replicas is concerned — skipped by
// the supervisor, swept up after a restart of the agent — and the dot keeps
// it apart from every job of a deploy.yaml, whose names cannot contain one.
const VerifyJob = "backup.verify"

// scratchMarker separates a volume's name from the backup run a scratch copy
// of it belongs to. Volume names cannot contain an underscore, so a name with
// the marker in it is never an application's own volume.
const scratchMarker = "_verify_"

// ScratchVolume names the throwaway copy of an application's volume that a
// backup is restored into to verify it. Used as a Mount's volume, it yields
// the Docker volume shipwick_<app>_<volume>_verify_<run>: the application's
// by its labels, so that nothing else claims it, and nobody's data.
func ScratchVolume(volume string, run int64) string {
	return volume + scratchMarker + strconv.FormatInt(run, 10)
}

// IsScratchVolume reports whether a volume's name, as ScratchVolume makes
// them, is a throwaway copy. Listings leave those out.
func IsScratchVolume(volume string) bool {
	return strings.Contains(volume, scratchMarker)
}

// RemoveScratchVolumes removes every scratch volume no container uses and
// returns how many went. A verification removes its own; this is for the ones
// an agent that died half-way left behind.
func (r *Runtime) RemoveScratchVolumes(ctx context.Context) (int, error) {
	filters := client.Filters{}
	filters.Add("label", LabelManaged+"=true")
	res, err := r.cli.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return 0, fmt.Errorf("list volumes: %w", err)
	}
	removed := 0
	for _, v := range res.Items {
		if !IsScratchVolume(v.Name) {
			continue
		}
		_, err := r.cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
		switch {
		case err == nil:
			removed++
		case cerrdefs.IsNotFound(err), cerrdefs.IsConflict(err):
			// Gone already, or a verification that is running right now has it.
		default:
			return removed, fmt.Errorf("remove volume %s: %w", v.Name, err)
		}
	}
	return removed, nil
}
