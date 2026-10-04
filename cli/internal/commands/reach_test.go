package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestRenderTellsACommandInsideAnApplicationWhereToRunInstead(t *testing.T) {
	refused := &client.APIError{Status: 403, Code: api.CodeApplicationCaller,
		Message: "the API answers the proxy, the dashboard and the server itself, not the containers of applications; from a container, call it at its hostname"}
	got := Render(refused)
	for _, want := range []string{"does not answer the containers of applications", "outside the container", "shipwick login --url https://"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render = %q, without %q", got, want)
		}
	}
}

func TestDoctorSaysWhenApplicationsCanReachTheAPI(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if strings.Contains(out, "Application containers can reach") {
		t.Fatalf("an agent that does not say so is not reported as open:\n%s", out)
	}

	f.server.OpenToApplications = true
	out, _, err = f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("an open API is worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Proxy serving 2 routes",
		"! Application containers can reach the agent's API",
		"run the installer again",
		"\"Who can reach the API\" in the handbook",
	})
}
