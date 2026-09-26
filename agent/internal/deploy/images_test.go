package deploy

import (
	"context"
	"slices"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestImagesOfRetiredDeploymentsAreRemoved(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	h.deploy(app("my-api", "my-api:1.1", 1))
	if got := h.rt.RemovedImages(); len(got) != 0 {
		t.Errorf("after two deployments nothing may go: 1.0 is the rollback target; removed %v", got)
	}

	d := h.deploy(app("my-api", "my-api:1.2", 1))
	if got := h.rt.RemovedImages(); !slices.Equal(got, []string{"my-api:1.0"}) {
		t.Errorf("removed %v, want only 1.0: 1.1 is the rollback target, 1.2 is running", got)
	}
	var steps []string
	for _, e := range h.events(d.ID) {
		steps = append(steps, e.Message)
	}
	if !slices.Contains(steps, "Removed 1 image of older versions") {
		t.Errorf("the deployment does not say so: %v", steps)
	}
	if ok, _ := h.rt.ImageExists(context.Background(), "my-api:1.1"); !ok {
		t.Error("the rollback target's image must stay")
	}
}

func TestAnImageAnotherApplicationRunsIsKept(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("web", "shared:1.0", 1))
	h.deploy(app("worker", "shared:1.0", 1))
	h.deploy(app("web", "shared:1.1", 1))
	h.deploy(app("web", "shared:1.2", 1))

	if got := h.rt.RemovedImages(); len(got) != 0 {
		t.Errorf("shared:1.0 is worker's active image; removed %v", got)
	}
}

func TestDeleteRemovesTheApplicationsImagesButNotWhatOthersKeep(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("web", "shared:1.0", 1))
	h.deploy(app("worker", "shared:1.0", 1))
	h.deploy(app("web", "web:2.0", 1))

	if err := h.engine.Delete(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	got := h.rt.RemovedImages()
	if !slices.Equal(got, []string{"web:2.0"}) {
		t.Errorf("removed %v, want web:2.0 only — worker still runs shared:1.0", got)
	}
	if apps, _ := h.engine.Applications(context.Background()); len(apps) != 1 || apps[0].Name != "worker" || apps[0].Status != api.AppHealthy {
		t.Errorf("worker must be untouched: %+v", apps)
	}
}
