package deploy

import (
	"context"
	"errors"
	"sort"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// Images pile up on a server that deploys often: every version ever deployed
// stays in Docker's store until something removes it, and nothing did. So
// after a deployment succeeds, and after an application is deleted, the images
// that only retired deployments refer to are removed.
//
// What stays: the image of every application's active deployment, and the
// image of its most recent superseded one — that is the rollback target, and a
// rollback should not have to wait for a pull. Docker refuses to untag an image
// a container uses, so a mistake here cannot stop anything; and only images
// that some deployment named are ever candidates, never what the user pulled
// for their own reasons.

// keptImages are the images no pruning may remove, across all applications.
func (e *Engine) keptImages(ctx context.Context) (map[string]bool, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, app := range apps {
		if app.ActiveDeploymentID != nil {
			d, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID)
			if err != nil {
				return nil, err
			}
			keep[d.Image] = true
		}
		previous, err := e.store.ListDeployments(ctx, store.DeploymentFilter{
			Application: app.Name, Statuses: []api.DeploymentStatus{api.StatusSuperseded}, Limit: 1,
		})
		if err != nil {
			return nil, err
		}
		if len(previous) > 0 {
			keep[previous[0].Image] = true
		}
	}
	return keep, nil
}

// deployedImages are the images an application's deployments have named.
func (e *Engine) deployedImages(ctx context.Context, app string) ([]string, error) {
	all, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: app})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var images []string
	for _, d := range all {
		if !seen[d.Image] {
			seen[d.Image] = true
			images = append(images, d.Image)
		}
	}
	sort.Strings(images)
	return images, nil
}

// pruneImages removes those of candidates that nothing keeps. It reports how
// many went; failures are logged, never returned — a stale image is not
// worth failing anything over.
func (e *Engine) pruneImages(ctx context.Context, candidates []string) int {
	keep, err := e.keptImages(ctx)
	if err != nil {
		e.log.Warn("could not decide which images to keep", "error", err)
		return 0
	}
	removed := 0
	for _, image := range candidates {
		if keep[image] {
			continue
		}
		switch err := e.rt.RemoveImage(ctx, image); {
		case err == nil:
			removed++
		case errors.Is(err, docker.ErrImageInUse):
			// Some container still runs it: perhaps one the user started
			// by hand. Not ours to decide.
		default:
			e.log.Warn("could not remove image", "image", image, "error", err)
		}
	}
	return removed
}
