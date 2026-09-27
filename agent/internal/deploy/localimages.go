package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// An application with `build:` in its deploy.yaml has its image built where
// `shipwick deploy` runs and sent here as an archive, which the agent loads.
// The agent never builds: a build runs whatever the Dockerfile says, with the
// network and the CPU of the machine it runs on, and that machine is the
// developer's, where Docker already is, not the server that serves everyone.
//
// Such images live under spec.LocalImageHost, a host that does not exist. So
// they are never pulled — there is nowhere to pull from — and one that is
// missing is missing for good until it is sent again.

// ErrImageNotBuilt means a deploy.yaml with `build:` arrived without the
// image the CLI fills in after building.
var ErrImageNotBuilt = errors.New("image: this application is built where shipwick deploy runs and sent to the server first; the agent never builds. Run shipwick deploy from the project")

// localImageMissing is the failure of a deployment whose local image is not on
// the server: never seen, or pruned since.
func localImageMissing(image string) error {
	return fmt.Errorf("%s is not on this server; it was built on a developer's machine — run shipwick deploy from the project again", image)
}

// LoadImage loads an image archive sent by the CLI for the application and
// returns what arrived. The archive must carry exactly one image, tagged
// shipwick.local/<name>:<tag>: an image is loaded for the application it
// belongs to, never into another's namespace. Loading touches no container,
// so it takes no lock; a deployment that runs meanwhile does not care.
func (e *Engine) LoadImage(ctx context.Context, app string, archive io.Reader) (api.LoadedImage, error) {
	counted := &countingReader{r: archive}
	refs, err := e.rt.LoadImage(ctx, counted)
	if err != nil {
		return api.LoadedImage{}, err
	}

	prefix := spec.LocalImagePrefix(app)
	var reason string
	switch {
	case len(refs) == 0:
		reason = "the archive holds no tagged image"
	case len(refs) > 1:
		reason = fmt.Sprintf("the archive holds %d images (%s); send one", len(refs), strings.Join(refs, ", "))
	case !strings.HasPrefix(refs[0], prefix) || len(refs[0]) == len(prefix):
		reason = fmt.Sprintf("the archive holds %s; an image for %s is tagged %s<tag>", refs[0], app, prefix)
	}
	if reason != "" {
		// What was loaded does not belong here, so it does not stay. Best
		// effort: the daemon would refuse an image a container uses, and
		// then it was there before this upload.
		for _, ref := range refs {
			if rerr := e.rt.RemoveImage(ctx, ref); rerr != nil {
				e.log.Warn("could not remove image from a refused upload", "image", ref, "error", rerr)
			}
		}
		return api.LoadedImage{}, &InvalidImageError{Reason: reason}
	}

	e.log.Info("image loaded", "app", app, "image", refs[0], "bytes", counted.n)
	return api.LoadedImage{Image: refs[0], SizeBytes: counted.n}, nil
}
