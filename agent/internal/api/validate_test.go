package api

import (
	"net/http"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestValidateAcceptsWhatDeployWouldAndCreatesNothing(t *testing.T) {
	f := newFixture(t)

	status, body := f.do("POST", "/api/v1/applications/my-api/validate", validConfig)
	if status != http.StatusOK || !decode[api.Validation](t, body).Valid {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if status, _ := f.do("GET", "/api/v1/applications/my-api", ""); status != http.StatusNotFound {
		t.Errorf("the application exists after a validation: status = %d", status)
	}
	_, body = f.do("GET", "/api/v1/deployments", "")
	if deployments := decode[[]api.Deployment](t, body); len(deployments) != 0 {
		t.Errorf("a validation recorded %d deployments", len(deployments))
	}
	if len(f.rt.Containers()) != 0 || len(f.rt.Pulled()) != 0 {
		t.Errorf("a validation touched Docker: %d containers, pulled %v", len(f.rt.Containers()), f.rt.Pulled())
	}
}

func TestValidateAnswersExactlyAsDeployWould(t *testing.T) {
	f := newFixture(t)
	const taken = "name: web\nimage: nginx:1.27\nport: 80\ndomain: example.com\n"
	if status, body := f.do("POST", "/api/v1/applications/web/deploy", taken); status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	for name, config := range map[string]string{
		"invalid field":             "name: my-api\nimage: nginx:1.27\nreplicas: 0\n",
		"unknown key":               "name: my-api\nimage: nginx:1.27\nreplica: 2\n",
		"not YAML":                  "name: [",
		"another application":       "name: other\nimage: nginx:1.27\n",
		"a hostname that is taken":  "name: my-api\nimage: nginx:1.27\nport: 80\ndomain: example.com\n",
		"an alias that is taken":    "name: my-api\nimage: nginx:1.27\nport: 80\ndomain: mine.example.com\naliases: [example.com]\n",
		"a secret that is missing":  "name: my-api\nimage: nginx:1.27\nenv:\n  TOKEN: ${API_TOKEN}\n",
		"an image that is not one":  "name: my-api\nimage: 'nginx 1.27'\n",
		"a static site with a port": "name: my-api\nstatic: dist\ndomain: mine.example.com\nport: 80\n",
	} {
		vStatus, vBody := f.do("POST", "/api/v1/applications/my-api/validate", config)
		dStatus, dBody := f.do("POST", "/api/v1/applications/my-api/deploy", config)
		if vStatus != dStatus || string(vBody) != string(dBody) {
			t.Errorf("%s:\nvalidate answered %d %s\ndeploy answered   %d %s", name, vStatus, vBody, dStatus, dBody)
		}
		if vStatus != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, vStatus)
		}
	}
}

func TestValidateDoesNotAskForWhatComesAfterIt(t *testing.T) {
	f := newFixture(t)

	// Before the build there is no image, and before the upload no digest:
	// deploy refuses both documents as they are, validate must not.
	for name, config := range map[string]string{
		"build without an image":           "name: my-api\nbuild: .\nport: 8080\n",
		"a static site without its upload": "name: my-api\nstatic: dist\ndomain: mine.example.com\n",
	} {
		status, body := f.do("POST", "/api/v1/applications/my-api/validate", config)
		if status != http.StatusOK || !decode[api.Validation](t, body).Valid {
			t.Errorf("%s: validate answered %d %s", name, status, body)
		}
		if status, _ := f.do("POST", "/api/v1/applications/my-api/deploy", config); status != http.StatusBadRequest {
			t.Errorf("%s: deploy answered %d, want 400: the relaxation is validate's alone", name, status)
		}
	}
}

func TestValidateNeedsTheDeployRole(t *testing.T) {
	f := newFixture(t)
	reader := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token

	status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/validate", validConfig, reader)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "deploy" {
		t.Errorf("read role: status = %d, error = %+v", status, e)
	}
	if status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/validate", validConfig, deployer); status != http.StatusOK {
		t.Errorf("deploy role: status = %d, body = %s", status, body)
	}
	if status, _ := f.do("POST", "/api/v1/applications/Not_A_Name/validate", validConfig); status != http.StatusBadRequest {
		t.Errorf("an invalid name in the URL: status = %d, want 400", status)
	}
}
