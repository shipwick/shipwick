package api

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const referringConfig = `name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
command: ["sh", "-c", "exec api --dir ${DATA_DIR}"]
env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
  LOG_LEVEL: debug
`

func (f *fixture) config(name, query string) api.ApplicationConfig {
	f.t.Helper()
	status, body := f.do("GET", "/api/v1/applications/"+name+"/config"+query, "")
	if status != http.StatusOK {
		f.t.Fatalf("GET config: status = %d, body = %s", status, body)
	}
	return decode[api.ApplicationConfig](f.t, body)
}

func TestTheConfigOfAnApplicationIsItsDocumentWithReferencesAndMasks(t *testing.T) {
	f := newFixture(t)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	first := deployAndWait(t, f, referringConfig)
	if first.Status != api.StatusActive {
		t.Fatalf("deploy: %+v", first)
	}

	status, body := f.do("GET", "/api/v1/applications/my-api/config", "")
	if status != http.StatusOK || strings.Contains(string(body), "hunter2") || strings.Contains(string(body), "debug") {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	config := decode[api.ApplicationConfig](t, body)
	if config.Application != "my-api" || config.DeploymentID != first.ID || config.Sequence != 1 || config.Version != "1.4.2" {
		t.Errorf("config = %+v", config)
	}
	if !reflect.DeepEqual(config.Masked, []string{"env.LOG_LEVEL"}) {
		t.Errorf("masked = %v", config.Masked)
	}
	for _, line := range []string{
		"name: my-api\n",
		"  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app\n",
		`  LOG_LEVEL: "********" # not handed out`,
		`command: ["sh", "-c", "exec api --dir ${DATA_DIR}"]`,
	} {
		if !strings.Contains(config.Document, line) {
			t.Errorf("the document lacks %q:\n%s", line, config.Document)
		}
	}
	// For a file the CLI reads, what is not a reference is escaped.
	if escaped := f.config("my-api", "?escape=true"); !strings.Contains(escaped.Document, `exec api --dir $${DATA_DIR}`) ||
		!strings.Contains(escaped.Document, "postgres://app:${DB_PASSWORD}@db:5432/app") {
		t.Errorf("escaped:\n%s", escaped.Document)
	}
	if status, body := f.do("GET", "/api/v1/applications/my-api/config?escape=perhaps", ""); status != http.StatusBadRequest {
		t.Errorf("escape=perhaps: status = %d, body = %s", status, body)
	}

	// An application without masks says so with an empty list, not null.
	f.do("POST", "/api/v1/applications/web/deploy", "name: web\nimage: nginx:1\n")
	f.engine.Wait()
	if _, body := f.do("GET", "/api/v1/applications/web/config", ""); !strings.Contains(string(body), `"masked":[]`) {
		t.Errorf("body = %s", body)
	}
	if status, body := f.do("GET", "/api/v1/applications/nothing/config", ""); status != http.StatusNotFound || decodeError(t, body).Code != api.CodeNotFound {
		t.Errorf("an unknown application: status = %d, body = %s", status, body)
	}
}

func TestTheConfigIsForThoseWhoMayDeployTheApplication(t *testing.T) {
	f := newFixture(t)
	deployAndWait(t, f, validConfig)
	f.do("POST", "/api/v1/applications/web/deploy", otherConfig)
	f.engine.Wait()

	read := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	status, body := f.doWithAuth("GET", "/api/v1/applications/my-api/config", "", read)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "deploy" {
		t.Errorf("a read token: status = %d, body = %s", status, body)
	}
	limited := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api"]}`)
	if status, body := f.doWithAuth("GET", "/api/v1/applications/my-api/config", "", limited); status != http.StatusOK {
		t.Errorf("its own application: status = %d, body = %s", status, body)
	}
	status, body = f.doWithAuth("GET", "/api/v1/applications/web/config", "", limited)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeTokenLimited {
		t.Errorf("another application: status = %d, body = %s", status, body)
	}
}

func TestADocumentIsDeployedUnderTheNameItSays(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/validate", validConfig)
	if status != http.StatusOK || !decode[api.Validation](t, body).Valid {
		t.Fatalf("validate: status = %d, body = %s", status, body)
	}
	if _, body := f.do("GET", "/api/v1/applications", ""); len(decode[[]api.Application](t, body)) != 0 {
		t.Errorf("validating created an application: %s", body)
	}

	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/applications", strings.NewReader(validConfig))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Location") != "/api/v1/deployments/1" {
		t.Fatalf("deploy: status = %d, Location = %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	f.engine.Wait()
	_, body = f.do("GET", "/api/v1/deployments/1", "")
	if d := decode[api.DeploymentDetail](t, body); d.Application != "my-api" || d.Status != api.StatusActive || d.Kind != api.KindDeploy {
		t.Errorf("deployment = %+v", d.Deployment)
	}

	// The trail names the application, though the address did not; a
	// validation leaves no entry.
	entries, err := f.store.AuditEntries(context.Background(), store.AuditFilter{Limit: 10})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if e := entries[0]; e.Action != "deploy" || e.Application != "my-api" || e.Outcome != api.AuditOK || e.Detail != "deployment 1" {
		t.Errorf("entry = %+v", e)
	}
	if byApp := f.audit("?application=my-api"); len(byApp) != 1 {
		t.Errorf("the entry is not found under its application: %+v", byApp)
	}
}

func TestADocumentThatCannotBeDeployedIsAnsweredLikeADeployment(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/api/v1/applications", "/api/v1/validate"} {
		status, body := f.do("POST", path, "image: nginx:1\nreplicas: 0\n")
		e := decodeError(t, body)
		fields, _ := e.Details["fields"].([]any)
		if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig || len(fields) != 2 {
			t.Errorf("%s without a name: status = %d, body = %s", path, status, body)
		}
		if status, body := f.do("POST", path, "name: agent\nimage: nginx:1\n"); status != http.StatusBadRequest || decodeError(t, body).Code != api.CodeInvalidConfig {
			t.Errorf("%s with a reserved name: status = %d, body = %s", path, status, body)
		}
		status, body = f.do("POST", path, "name: my-api\nimage: nginx:1\nenv:\n  A: ${MISSING}\n")
		if field := fieldOf(t, status, body); field["field"] != "env.A" {
			t.Errorf("%s: field = %v", path, field)
		}
		status, _ = f.do("POST", path, "name: my-api\nimage: nginx:1\nenv:\n  A: "+strings.Repeat("x", spec.MaxConfigBytes)+"\n")
		if status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with a document that is too large: status = %d", path, status)
		}
	}
	if _, body := f.do("GET", "/api/v1/applications", ""); len(decode[[]api.Application](t, body)) != 0 {
		t.Errorf("a refused document created an application: %s", body)
	}
}

func TestAMaskIsRefusedByEveryWayADocumentArrives(t *testing.T) {
	f := newFixture(t)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	deployAndWait(t, f, referringConfig)
	document := f.config("my-api", "").Document

	for _, path := range []string{"/api/v1/applications", "/api/v1/validate", "/api/v1/applications/my-api/deploy", "/api/v1/applications/my-api/validate"} {
		status, body := f.do("POST", path, document)
		field := fieldOf(t, status, body)
		if field["field"] != "env.LOG_LEVEL" || field["message"] != "******** is what the server shows in the place of this value, not the value" ||
			field["expected"] != "the value itself, or ${NAME} with the value stored by shipwick secret set NAME" {
			t.Errorf("%s: field = %v", path, field)
		}
	}
	f.engine.Wait()
	if _, body := f.do("GET", "/api/v1/deployments?application=my-api", ""); len(decode[[]api.Deployment](t, body)) != 1 {
		t.Errorf("a masked document was deployed: %s", body)
	}

	// The spec of GET /applications/{name}, sent back as a document, is
	// masks all over, and is refused for it.
	_, body := f.do("GET", "/api/v1/deployments/1", "")
	masked := decode[api.DeploymentDetail](t, body).Spec
	if masked.Env["DATABASE_URL"] != spec.Mask {
		t.Fatalf("spec = %+v", masked)
	}
	status, body := f.do("POST", "/api/v1/applications", "name: my-api\nimage: nginx:1\nenv:\n  DATABASE_URL: \""+masked.Env["DATABASE_URL"]+"\"\n")
	if field := fieldOf(t, status, body); field["field"] != "env.DATABASE_URL" {
		t.Errorf("field = %v", field)
	}
}
