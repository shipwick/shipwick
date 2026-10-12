package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
)

// ImageIDs maps every tag the daemon has to the ID of the image it names.
// A load applies an archive's tags as it goes, so what a tag named before is
// known only to whoever asked beforehand: the engine asks here, and undoes a
// refused load with TagImage.
func (r *Runtime) ImageIDs(ctx context.Context) (map[string]string, error) {
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	ids := map[string]string{}
	for _, img := range res.Items {
		for _, tag := range img.RepoTags {
			// An image without a tag is listed as <none>:<none> by the
			// classic store; nothing in a deployment could name it.
			if strings.Contains(tag, "<none>") {
				continue
			}
			ids[tag] = img.ID
		}
	}
	return ids, nil
}

// TagImage points ref at the image id names, moving the tag if it exists.
func (r *Runtime) TagImage(ctx context.Context, id, ref string) error {
	if _, err := r.cli.ImageTag(ctx, client.ImageTagOptions{Source: id, Target: ref}); err != nil {
		return fmt.Errorf("tag image %s as %s: %w", id, ref, err)
	}
	return nil
}
