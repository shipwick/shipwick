package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// ErrImageIncomplete means an archive was loaded that does not carry every
// layer of its image, and the daemon does not have the others.
var ErrImageIncomplete = errors.New("the archive does not hold every layer of the image, and the server does not have the others")

// ImageLayers returns, for every image the daemon has, the diff IDs of its
// layers, base layer first: the RootFS of `docker image inspect`. It is the
// one thing both image stores report about what they hold, and the one thing
// both decide by when an archive arrives: a layer is on the daemon when an
// image there starts with the same diff IDs up to that layer.
func (r *Runtime) ImageLayers(ctx context.Context) ([][]string, error) {
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	layers := make([][]string, 0, len(res.Items))
	for _, img := range res.Items {
		inspect, err := r.cli.ImageInspect(ctx, img.ID)
		if cerrdefs.IsNotFound(err) {
			continue // removed since it was listed
		} else if err != nil {
			return nil, fmt.Errorf("inspect image %s: %w", img.ID, err)
		}
		if len(inspect.RootFS.Layers) > 0 {
			layers = append(layers, inspect.RootFS.Layers)
		}
	}
	return layers, nil
}

// unpackFailure is how the containerd image store reports an image it
// recorded but could not unpack: a progress line after "Loaded image", not
// an error. The image is tagged and cannot start a container.
const unpackFailure = "Error unpacking image "

// missingBlob reports whether a load failed because the archive names a
// layer file it does not carry, which is how the classic image store refuses
// an archive whose missing layers it does not have:
// "open /var/lib/docker/tmp/docker-import-…/blobs/sha256/…: no such file or directory".
func missingBlob(message string) bool {
	return strings.Contains(message, "/blobs/sha256/") && strings.HasSuffix(strings.TrimSpace(message), "no such file or directory")
}

// discardUnpacked untags images the daemon recorded without their layers.
// Best effort: what stays is a tag no deployment was told about.
func (r *Runtime) discardUnpacked(ctx context.Context, refs []string) {
	for _, ref := range refs {
		r.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{})
	}
}
