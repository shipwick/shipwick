package store

import (
	"context"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func TestApplicationsWithDeploymentsReadBothInOneGo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	d, err := s.CreateDeployment(ctx, spec.App{Name: "web", Image: "web:1", Replicas: 1}, now)
	if err != nil {
		t.Fatal(err)
	}

	apps, inFlight, err := s.ApplicationsWithDeployments(ctx, DeploymentFilter{Statuses: []api.DeploymentStatus{api.StatusPending}})
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Name != "web" || len(inFlight) != 1 || inFlight[0].ID != d.ID {
		t.Errorf("apps %+v, in flight %+v", apps, inFlight)
	}

	app, inFlight, err := s.ApplicationWithDeployments(ctx, "web", DeploymentFilter{Statuses: []api.DeploymentStatus{api.StatusPending}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "web" || len(inFlight) != 1 {
		t.Errorf("app %+v, in flight %+v", app, inFlight)
	}
	if _, _, err := s.ApplicationWithDeployments(ctx, "nope", DeploymentFilter{}); err != ErrNotFound {
		t.Errorf("unknown application: %v, want ErrNotFound", err)
	}
}
