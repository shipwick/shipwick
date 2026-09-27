package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestSecretLifecycle(t *testing.T) {
	f := newFixture(t)

	status, body := f.do("GET", "/api/v1/secrets", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Fatalf("empty list: status = %d, body = %s", status, body)
	}

	status, body = f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("set: status = %d, body = %s", status, body)
	}
	status, _ = f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter3"}`)
	if status != http.StatusNoContent {
		t.Fatalf("replace: status = %d", status)
	}

	_, body = f.do("GET", "/api/v1/secrets", "")
	secrets := decode[[]api.Secret](t, body)
	if len(secrets) != 1 || secrets[0].Name != "DB_PASSWORD" || secrets[0].CreatedAt.IsZero() || secrets[0].UpdatedAt.Before(secrets[0].CreatedAt) {
		t.Errorf("list = %+v", secrets)
	}
	if strings.Contains(string(body), "hunter") {
		t.Errorf("the list carries a value: %s", body)
	}
	if logs := f.logs.String(); strings.Contains(logs, "hunter") {
		t.Errorf("a value reached the log:\n%s", logs)
	}

	status, body = f.do("DELETE", "/api/v1/secrets/DB_PASSWORD", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", status, body)
	}
	status, body = f.do("DELETE", "/api/v1/secrets/DB_PASSWORD", "")
	if e := decodeError(t, body); status != http.StatusNotFound || e.Code != api.CodeNotFound {
		t.Errorf("delete again: status = %d, error = %+v", status, e)
	}
}

func TestSecretEndpointsValidateNameAndValue(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/secrets/lower-case", `{"value": "x"}`},
		{"PUT", "/api/v1/secrets/1STARTS_WITH_DIGIT", `{"value": "x"}`},
		{"PUT", "/api/v1/secrets/" + strings.Repeat("A", 65), `{"value": "x"}`},
		{"PUT", "/api/v1/secrets/OK", `{"value": ""}`},
		{"PUT", "/api/v1/secrets/OK", `{}`},
		{"PUT", "/api/v1/secrets/OK", `{"value": "x", "name": "OK"}`},
		{"PUT", "/api/v1/secrets/OK", `not json`},
		{"PUT", "/api/v1/secrets/OK", `{"value": "a\u0000b"}`},
		{"PUT", "/api/v1/secrets/OK", `{"value": "` + strings.Repeat("x", api.MaxSecretValueBytes+1) + `"}`},
		{"DELETE", "/api/v1/secrets/lower-case", ""},
	} {
		status, body := f.do(tt.method, tt.path, tt.body)
		e := decodeError(t, body)
		if status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path[:min(len(tt.path), 40)], status, e)
		}
		if strings.Contains(e.Message, "xxxx") {
			t.Errorf("%s: the error echoes the value", tt.path)
		}
	}
	if _, body := f.do("GET", "/api/v1/secrets", ""); strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Errorf("nothing may have been stored: %s", body)
	}
	// The largest value goes through.
	status, _ := f.do("PUT", "/api/v1/secrets/BIG", `{"value": "`+strings.Repeat("x", api.MaxSecretValueBytes)+`"}`)
	if status != http.StatusNoContent {
		t.Errorf("a value at the limit: status = %d", status)
	}
}

func TestSecretRolesReadListsAdminWrites(t *testing.T) {
	f := newFixture(t)
	reader := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token

	if status, _ := f.doWithAuth("GET", "/api/v1/secrets", "", reader); status != http.StatusOK {
		t.Errorf("read may list: status = %d", status)
	}
	for _, auth := range []string{reader, deployer} {
		for _, tt := range []struct{ method, path, body string }{
			{"PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "x"}`},
			{"DELETE", "/api/v1/secrets/DB_PASSWORD", ""},
		} {
			status, body := f.doWithAuth(tt.method, tt.path, tt.body, auth)
			if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "admin" {
				t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
			}
		}
	}
}

func TestDeployFillsEnvFromSecretsAndNamesWhatIsMissing(t *testing.T) {
	f := newFixture(t)
	const config = "name: my-api\nimage: ghcr.io/company/my-api:1.4.2\nport: 8080\nenv:\n  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app\n  LITERAL: $${DB_PASSWORD}\n"

	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", config)
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig || e.Message != "invalid deploy.yaml" {
		t.Fatalf("status = %d, error = %+v", status, e)
	}
	fields, _ := e.Details["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("fields = %v", e.Details["fields"])
	}
	field, _ := fields[0].(map[string]any)
	if field["field"] != "env.DATABASE_URL" || field["expected"] != "shipwick secret set DB_PASSWORD" ||
		field["message"] != "refers to ${DB_PASSWORD}, which is not set where shipwick runs and not stored on the server" {
		t.Errorf("field = %v", field)
	}

	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	status, body = f.do("POST", "/api/v1/applications/my-api/deploy", config)
	if status != http.StatusAccepted {
		t.Fatalf("deploy with the secret stored: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	_, body = f.do("GET", "/api/v1/deployments/1", "")
	d := decode[api.DeploymentDetail](t, body)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s, error = %q", d.Status, d.Error)
	}
	if d.Spec.Env["DATABASE_URL"] != "********" || d.Spec.Env["LITERAL"] != "********" {
		t.Errorf("env in the API = %v, want masked", d.Spec.Env)
	}
	if strings.Contains(string(body), "hunter2") {
		t.Errorf("the response carries the secret: %s", body)
	}
	c := f.rt.Containers()[0]
	if env := f.rt.Spec(c.ID).Env; env["DATABASE_URL"] != "postgres://app:hunter2@postgres:5432/app" || env["LITERAL"] != "${DB_PASSWORD}" {
		t.Errorf("container env = %v", env)
	}
	if logs := f.logs.String(); strings.Contains(logs, "hunter2") {
		t.Errorf("the secret reached the log:\n%s", logs)
	}
}
