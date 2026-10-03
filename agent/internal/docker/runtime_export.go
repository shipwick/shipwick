package docker

import (
	"context"
	"fmt"
	"io"
)

// ImportJob is the job name of the container an import fills an application's
// volumes through. Like VerifyJob it is a job to everything that manages
// replicas, and its dot keeps it apart from the jobs of a deploy.yaml. The
// container is created and never started.
const ImportJob = "import.volumes"

// SaveImage writes an image out as an archive LoadImage reads: what `docker
// save` produces. It is not part of the engine's Runtime interface; an export
// reaches it through an optional one (see deploy/export.go).
func (r *Runtime) SaveImage(ctx context.Context, image string) (io.ReadCloser, error) {
	res, err := r.cli.ImageSave(ctx, []string{image})
	if err != nil {
		return nil, fmt.Errorf("save image %s: %w", image, err)
	}
	return res, nil
}
