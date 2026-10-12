package store

import (
	"context"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestActiveDeploymentsReadsEveryApplicationInOneGo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()

	// web: two deployments, the second active; its first replica was
	// replaced, so one of its three replica rows is removed.
	v1, err := s.CreateDeployment(ctx, testApp("web", "web:1"), now)
	if err != nil {
		t.Fatal(err)
	}
	advance(t, s, v1.ID, api.StatusHealthy)
	if _, err := s.ActivateDeployment(ctx, v1.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReplica(ctx, Replica{DeploymentID: v1.ID, Index: 1, ContainerID: "c-old", ContainerName: "web_1_1"}, now); err != nil {
		t.Fatal(err)
	}
	v2, err := s.CreateDeployment(ctx, testApp("web", "web:2"), now)
	if err != nil {
		t.Fatal(err)
	}
	advance(t, s, v2.ID, api.StatusHealthy)
	if _, err := s.ActivateDeployment(ctx, v2.ID, now); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"c-gone", "c-1", "c-2"} {
		if err := s.AddReplica(ctx, Replica{DeploymentID: v2.ID, Index: i, ContainerID: id, ContainerName: "web_2_" + id}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkReplicaRemoved(ctx, "c-gone", now); err != nil {
		t.Fatal(err)
	}
	// api: its first deployment is still under way.
	pending, err := s.CreateDeployment(ctx, testApp("api", "api:1"), now)
	if err != nil {
		t.Fatal(err)
	}

	a, err := s.ActiveDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Applications) != 2 || a.Applications[0].Name != "api" || a.Applications[1].Name != "web" {
		t.Errorf("applications = %+v, want api and web", a.Applications)
	}
	web := a.Applications[1]
	if len(a.Deployments) != 1 || a.Deployments[web.ID].ID != v2.ID || a.Deployments[web.ID].Spec.Env["KEY"] != "value" {
		t.Errorf("active deployments = %+v, want web's v2 with its spec open", a.Deployments)
	}
	replicas := a.Replicas[v2.ID]
	if len(a.Replicas) != 1 || len(replicas) != 2 || replicas[0].ContainerID != "c-1" || replicas[1].ContainerID != "c-2" {
		t.Errorf("replicas = %+v, want c-1 and c-2 of v2 only", a.Replicas)
	}
	if a.InFlight != nil {
		t.Errorf("in flight = %+v without asking for any status", a.InFlight)
	}

	a, err = s.ActiveDeployments(ctx, api.StatusPending, api.StatusBuilding)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.InFlight) != 1 || a.InFlight[0].ID != pending.ID {
		t.Errorf("in flight = %+v, want api's pending deployment", a.InFlight)
	}
}
