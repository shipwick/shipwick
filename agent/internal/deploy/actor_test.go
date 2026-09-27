package deploy

import (
	"context"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestDeploymentsRecordWhoMadeThem(t *testing.T) {
	h := newHarness(t)
	ctx := WithActor(context.Background(), "ci")

	d, err := h.engine.Deploy(ctx, app("my-api", "my-api:1.0", 1))
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	h.engine.Wait()

	stored, _ := h.store.GetDeployment(context.Background(), d.ID)
	if stored.Actor != "ci" || DeploymentView(stored).By != "ci" {
		t.Errorf("actor = %q, by = %q, want ci", stored.Actor, DeploymentView(stored).By)
	}

	// A redeploy is made by whoever asks for it, not by whoever deployed first.
	again, err := h.engine.Redeploy(WithActor(context.Background(), api.RootTokenName), "my-api", "")
	if err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	h.engine.Wait()
	stored, _ = h.store.GetDeployment(context.Background(), again.ID)
	if stored.Actor != api.RootTokenName {
		t.Errorf("redeploy actor = %q, want root", stored.Actor)
	}

	// Without an actor — engine code paths that are not requests — nothing
	// is claimed.
	anonymous := h.deploy(app("other", "other:1.0", 1))
	if anonymous.Actor != "" || DeploymentView(anonymous).By != "" {
		t.Errorf("actor without one in the context = %q", anonymous.Actor)
	}
}

func TestStopAndStartEventsNameTheActorUnlessRoot(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	a, _ := h.store.GetApplication(context.Background(), "my-api")

	ci := WithActor(context.Background(), "ci")
	root := WithActor(context.Background(), api.RootTokenName)
	if err := h.engine.Stop(ci, "my-api"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.engine.Start(root, "my-api"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events, err := h.store.ListApplicationEvents(context.Background(), a.ID, 10)
	if err != nil {
		t.Fatalf("ListApplicationEvents: %v", err)
	}
	if len(events) != 2 || events[0].Message != "Application started" || events[1].Message != "Application stopped by ci" {
		t.Errorf("unexpected events: %+v", events)
	}
}
