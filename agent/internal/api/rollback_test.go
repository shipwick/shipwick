package api

import (
	"net/http"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestRedeployAndRollbackEndpoints(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.engine.Wait()

	// Redeploy with another image. This body is the contract the dashboard
	// was written against.
	status, body := f.do("POST", "/api/v1/applications/my-api/redeploy", `{"image": "ghcr.io/company/my-api:1.5.0"}`)
	if status != http.StatusAccepted {
		t.Fatalf("redeploy: %d %s", status, body)
	}
	d := decode[api.Deployment](t, body)
	if d.Kind != api.KindRedeploy || d.Version != "1.5.0" || d.SourceDeploymentID == nil || *d.SourceDeploymentID != 1 {
		t.Errorf("unexpected deployment: %+v", d)
	}
	f.engine.Wait()

	// Rollback with no body at all.
	status, body = f.do("POST", "/api/v1/applications/my-api/rollback", "")
	if status != http.StatusAccepted {
		t.Fatalf("rollback: %d %s", status, body)
	}
	if d = decode[api.Deployment](t, body); d.Kind != api.KindRollback || d.Version != "1.4.2" {
		t.Errorf("unexpected rollback: %+v", d)
	}
	f.engine.Wait()

	// Rollback to a chosen deployment.
	status, body = f.do("POST", "/api/v1/applications/my-api/rollback", `{"deployment_id": 2}`)
	if d = decode[api.Deployment](t, body); status != http.StatusAccepted || d.Version != "1.5.0" {
		t.Errorf("rollback to #2: %d %+v", status, d)
	}
	f.engine.Wait()

	// The real env values travelled with every one of them, server-side only.
	for _, c := range f.rt.Containers() {
		if f.rt.Spec(c.ID).Env["DATABASE_PASSWORD"] != "hunter2" {
			t.Error("redeploy/rollback must re-use the stored configuration, secrets included")
		}
	}

	tests := []struct {
		path, body string
		status     int
		code       string
	}{
		{"/api/v1/applications/ghost/rollback", "", http.StatusNotFound, api.CodeNotFound},
		{"/api/v1/applications/ghost/redeploy", "", http.StatusNotFound, api.CodeNotFound},
		{"/api/v1/applications/my-api/rollback", `{"deployment_id": 999}`, http.StatusNotFound, api.CodeNotFound},
		{"/api/v1/applications/my-api/rollback", `{"deployment_id": 4}`, http.StatusConflict, api.CodeNoRollbackTarget}, // the active one
		{"/api/v1/applications/my-api/rollback", `{"deployment_id": -1}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/redeploy", `{"image": "Not Valid"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		// A typo must not silently redeploy the old image.
		{"/api/v1/applications/my-api/redeploy", `{"imgae": "nginx:2"}`, http.StatusBadRequest, api.CodeInvalidRequest},
		{"/api/v1/applications/my-api/redeploy", `{not json`, http.StatusBadRequest, api.CodeInvalidRequest},
	}
	for _, tt := range tests {
		status, body := f.do("POST", tt.path, tt.body)
		if e := decodeError(t, body); status != tt.status || e.Code != tt.code {
			t.Errorf("POST %s %s: status = %d, error = %+v", tt.path, tt.body, status, e)
		}
	}
}
