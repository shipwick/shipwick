package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func history() []api.Deployment {
	return []api.Deployment{ // newest first, as the API returns it
		{ID: 50, Sequence: 5, Version: "1.4.3", Status: api.StatusFailed},
		{ID: 40, Sequence: 4, Version: "1.4.2", Status: api.StatusActive},
		{ID: 30, Sequence: 3, Version: "1.4.1", Status: api.StatusSuperseded},
		{ID: 20, Sequence: 2, Version: "1.4.0", Status: api.StatusRolledBack},
		{ID: 10, Sequence: 1, Version: "1.3.9", Status: api.StatusSuperseded},
	}
}

func TestRollbackTargetSelection(t *testing.T) {
	d, err := rollbackTarget(history(), 0)
	if err != nil || d.Sequence != 3 {
		t.Errorf("default target = #%d, %v; want #3, the last version that served successfully", d.Sequence, err)
	}
	if d, err = rollbackTarget(history(), 1); err != nil || d.ID != 10 {
		t.Errorf("--to 1 = %+v, %v", d, err)
	}

	for seq, want := range map[int]string{
		4:  "running right now",
		5:  "never ran successfully (FAILED)",
		2:  "never ran successfully (ROLLED_BACK)",
		99: "no deployment #99",
	} {
		if _, err := rollbackTarget(history(), seq); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--to %d: err = %v, want it to mention %q", seq, err, want)
		}
	}
	if _, err := rollbackTarget(history()[:2], 0); err == nil {
		t.Error("with no earlier success there is nothing to go back to")
	}
}

func TestRollbackCommand(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	f.history = history()
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusActive, Version: "1.4.1", CompletedAt: &done},
		Spec:       spec.App{Domain: "api.example.com"},
		Events:     []api.Event{event(1, api.EventStep, api.LevelInfo, "Replica 1/2 is serving 1.4.1; its 1.4.2 predecessor is retired")},
	}}

	out, _, err := f.run(writeConfig(t, validConfig), "rollback")
	if err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Rolling back my-api to 1.4.1", "(deployment #3)",
		"✓ Replica 1/2 is serving 1.4.1",
		"my-api 1.4.1",
		"https://api.example.com",
	})
	// What was announced is exactly what was requested: the ID, not "previous".
	if got := f.actionBodies["rollback"]; got != `{"deployment_id":30}` {
		t.Errorf("request body = %s", got)
	}

	if _, _, err := f.run(t.TempDir(), "rollback", "my-api", "--to", "1"); err != nil {
		t.Fatalf("rollback --to 1: %v", err)
	}
	if got := f.actionBodies["rollback"]; got != `{"deployment_id":10}` {
		t.Errorf("--to takes the #number users see and sends the id: body = %s", got)
	}

	// Refused locally, without bothering the agent.
	delete(f.actionBodies, "rollback")
	_, _, err = f.run(t.TempDir(), "rollback", "my-api", "--to", "5")
	if err == nil || !strings.Contains(Render(err), "never ran successfully") {
		t.Errorf("err = %v", err)
	}
	if _, sent := f.actionBodies["rollback"]; sent {
		t.Error("an impossible rollback must not be requested")
	}
}

func TestRollbackFailureExitsNonZero(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppHealthy}, ActiveDeployment: &api.Deployment{Version: "1.4.2"}}
	f.history = history()
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusFailed, CompletedAt: &done, Error: "replica 1 exited with code 1 shortly after start"},
	}}
	out, _, err := f.run(t.TempDir(), "rollback", "my-api")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v", err)
	}
	assertInOrder(t, out, []string{"✗ Deployment failed", "my-api is still running 1.4.2"})
}

func TestRedeployCommand(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusActive, Version: "1.5.0", CompletedAt: &done},
	}}

	// No deploy.yaml anywhere: that is the point of redeploy.
	out, _, err := f.run(t.TempDir(), "redeploy", "my-api", "--image", "ghcr.io/company/my-api:1.5.0")
	if err != nil {
		t.Fatalf("redeploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Redeploying my-api with ghcr.io/company/my-api:1.5.0", "my-api 1.5.0"})
	if got := f.actionBodies["redeploy"]; got != `{"image":"ghcr.io/company/my-api:1.5.0"}` {
		t.Errorf("request body = %s", got)
	}

	if _, _, err := f.run(t.TempDir(), "redeploy", "my-api"); err != nil {
		t.Fatalf("redeploy without image: %v", err)
	}
	if got := f.actionBodies["redeploy"]; got != `{"image":""}` {
		t.Errorf("request body = %s", got)
	}
}

func TestNoRollbackTargetFromAgentIsExplained(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	f.history = history()
	f.deployStatus = 409
	f.deployError = api.Error{Code: api.CodeNoRollbackTarget, Message: "no earlier successful deployment to roll back to"}

	_, _, err := f.run(t.TempDir(), "rollback", "my-api")
	if got := Render(err); !strings.Contains(got, "no earlier successful deployment") || !strings.Contains(got, "shipwick status") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestFailureReportSaysWhatIsTrueNow(t *testing.T) {
	// A rollback can fail too. "The failed deployment did not affect it"
	// would then be a lie told at the worst possible moment.
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{
		Application:      api.Application{Name: "my-api", Status: api.AppDegraded, Replicas: api.ReplicaCount{Desired: 3, Healthy: 2}},
		ActiveDeployment: &api.Deployment{Version: "1.4.1"},
	}
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusFailed, CompletedAt: &done,
			Error: "replica 2 exited with code 1; the rollback to 1.4.1 then failed too: no space left on device"},
	}}

	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, "did not affect it") {
		t.Errorf("must not claim the application is unaffected while it is DEGRADED:\n%s", out)
	}
	assertInOrder(t, out, []string{"the rollback to 1.4.1 then failed too", "my-api is running 1.4.1, but it is DEGRADED right now (2/3 replicas healthy)", "shipwick status my-api"})
}
