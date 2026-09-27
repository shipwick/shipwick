package deploy

import (
	"context"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

// An application is never FAILED while its first deployment is under way or
// the moment it succeeds: the application row and the deployment it is
// waiting on are read together, so no poll can fall between the two.
func TestAnApplicationIsNeverFailedWhileItsDeploymentSucceeds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	d, err := h.engine.Deploy(ctx, app("my-api", "my-api:1.0", 2))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[api.ApplicationStatus]bool{}
	for {
		apps, err := h.engine.Applications(ctx)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := h.engine.Application(ctx, "my-api")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range append(apps, detail.Application) {
			seen[a.Status] = true
			if a.Status == api.AppFailed {
				t.Fatalf("application reported FAILED during a deployment that is succeeding: %+v", a)
			}
		}
		final, err := h.store.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.CompletedAt != nil {
			break
		}
	}
	h.engine.Wait()
	if !seen[api.AppDeploying] && !seen[api.AppHealthy] {
		t.Errorf("expected to observe DEPLOYING or HEALTHY, saw %v", seen)
	}
}
