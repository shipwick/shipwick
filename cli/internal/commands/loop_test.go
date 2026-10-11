package commands

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const loopMessage = "https://api.example.com/ sends its visitors to www.example.com, which `redirects` sends back: every request loops. " +
	"Make www.example.com the `domain` and api.example.com a redirect, or stop the application redirecting"

// routedDeployment is a completed deployment whose hostname went into the
// proxy, with the agent's look through it still to come.
func routedDeployment() api.DeploymentDetail {
	done := fixedNow
	return api.DeploymentDetail{
		Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done},
		Spec:       spec.App{Domain: "api.example.com"},
		Events: []api.Event{
			event(1, api.EventState, api.LevelInfo, "BUILDING"),
			event(2, api.EventStep, api.LevelInfo, "Routed https://api.example.com to 2 replicas"),
			event(3, api.EventState, api.LevelInfo, "ACTIVE"),
			event(4, api.EventStep, api.LevelInfo, "Deployment successful"),
		},
	}
}

func withProbe(d api.DeploymentDetail, level, message string) api.DeploymentDetail {
	d.Events = append(append([]api.Event(nil), d.Events...), event(5, api.EventProbe, level, message))
	return d
}

func TestDeployPrintsWhatTheAgentFoundThroughTheProxyUnderTheSummary(t *testing.T) {
	f := newFakeAgent(t)
	completed := routedDeployment()
	// The look comes a poll after completed_at, as it does on the server.
	f.deploymentPolls = []api.DeploymentDetail{completed, withProbe(completed, api.LevelWarn, loopMessage)}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2, Running: 2, Healthy: 2}}}

	out, errOut, err := f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("a loop is a warning, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Deployment successful", "my-api 1.4.2", "https://api.example.com"})
	if !strings.Contains(errOut, "! "+loopMessage) {
		t.Errorf("the loop is not reported:\n%s\n%s", out, errOut)
	}

	// An answer that is in order is a line too, under the summary.
	f.deploymentPolls = []api.DeploymentDetail{completed, withProbe(completed, api.LevelInfo, "https://api.example.com/ answers HTTP 200 through the proxy")}
	out, errOut, err = f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"https://api.example.com\n", "✓ https://api.example.com/ answers HTTP 200 through the proxy"})
	if strings.Contains(errOut, "still asking") {
		t.Errorf("the answer arrived; nothing is pending:\n%s", errOut)
	}
}

func TestDeployWaitsForTheLookOnlyFromAnAgentThatLooks(t *testing.T) {
	polls := func(f *fakeAgent) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, r := range f.requests {
			if r == "GET /api/v1/deployments/1" {
				n++
			}
		}
		return n
	}

	// An agent before 0.9 never looks: one poll finds the deployment
	// complete, and nothing waits for more.
	f := newFakeAgent(t)
	f.health = api.Health{Status: "ok", Version: "0.8.1"}
	f.deploymentPolls = []api.DeploymentDetail{routedDeployment()}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	_, errOut, err := f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if polls(f) != 1 || strings.Contains(errOut, "still asking") {
		t.Errorf("against 0.8.1: %d polls, stderr %q; want one poll and no waiting", polls(f), errOut)
	}

	// A newer agent is given its time, and when the look has not come, the
	// deployment's output says where it will be.
	f = newFakeAgent(t)
	f.deploymentPolls = []api.DeploymentDetail{routedDeployment()}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	_, errOut, err = f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if polls(f) != 1+probePolls {
		t.Errorf("against a looking agent: %d polls, want %d", polls(f), 1+probePolls)
	}
	if !strings.Contains(errOut, "The agent is still asking https://api.example.com/ through the proxy; what it finds is shown by: shipwick status my-api") {
		t.Errorf("stderr:\n%s", errOut)
	}

	// Without a domain there is no hostname to ask: no wait either.
	f = newFakeAgent(t)
	d := routedDeployment()
	d.Spec.Domain, d.Events = "", d.Events[:1]
	f.deploymentPolls = []api.DeploymentDetail{d}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	if _, _, err := f.run(writeConfig(t, validConfig), "deploy"); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if polls(f) != 1 {
		t.Errorf("without a domain: %d polls, want 1", polls(f))
	}
}

func TestStatusShowsALoopTheAgentFound(t *testing.T) {
	f := newFakeAgent(t)
	started := fixedNow
	f.app = api.ApplicationDetail{
		Application:      api.Application{Name: "my-api", Status: api.AppHealthy, Domain: "api.example.com", Replicas: api.ReplicaCount{Desired: 1, Running: 1, Healthy: 1}},
		ActiveDeployment: &api.Deployment{ID: 1, Sequence: 3, Version: "1.4.2", StartedAt: started, CompletedAt: &started},
	}
	f.deploymentPolls = []api.DeploymentDetail{withProbe(routedDeployment(), api.LevelWarn, loopMessage)}
	out, _, err := f.run(t.TempDir(), "status", "my-api")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"https://api.example.com", "! " + loopMessage})

	// An answer in order is nothing to add to the screen.
	f.deploymentPolls = []api.DeploymentDetail{withProbe(routedDeployment(), api.LevelInfo, "https://api.example.com/ answers HTTP 200 through the proxy")}
	out, _, err = f.run(t.TempDir(), "status", "my-api")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "through the proxy") {
		t.Errorf("an answer in order is shown:\n%s", out)
	}
}

func TestDoctorReportsARedirectLoopAndLeavesOtherRedirectsAlone(t *testing.T) {
	redirect := func(status int, location string) roundTrip {
		return func(*http.Request) (*http.Response, error) {
			resp := answer(status)
			resp.Header.Set("Location", location)
			return resp, nil
		}
	}
	f := doctorAgent(t, func(string) ([]string, error) { return []string{"203.0.113.10"}, nil }, redirect(301, "https://www.example.com/"))
	out, _, err := f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("a loop is a problem: err=%v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✗ " + loopMessage, "1 problem found."})

	f = doctorAgent(t, func(string) ([]string, error) { return []string{"203.0.113.10"}, nil }, redirect(302, "https://api.example.com/login"))
	out, _, err = f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("a redirect to a sign-in page is the application's business: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ https://api.example.com/ answers HTTP 302"})
}
