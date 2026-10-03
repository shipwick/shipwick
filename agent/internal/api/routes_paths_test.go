package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const protectedConfig = `name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
domain: example.com
path: /api
proxy:
  headers:
    X-Frame-Options: DENY
  basic_auth:
    - path: /api/admin
      username: admin
      password: ${ADMIN_PASSWORD}
  redirects:
    - from: /api/old
      to: /api/new
`

// fieldOf returns the one field error of an INVALID_CONFIG response.
func fieldOf(t *testing.T, status int, body []byte) map[string]any {
	t.Helper()
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig || e.Message != "invalid deploy.yaml" {
		t.Fatalf("status = %d, error = %+v", status, e)
	}
	fields, _ := e.Details["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("fields = %v", e.Details["fields"])
	}
	field, _ := fields[0].(map[string]any)
	return field
}

func TestABasicAuthPasswordIsFilledInJudgedAndNeverReturned(t *testing.T) {
	f := newFixture(t)

	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", protectedConfig)
	if field := fieldOf(t, status, body); field["field"] != "proxy.basic_auth[0].password" || field["expected"] != "shipwick secret set ADMIN_PASSWORD" ||
		field["message"] != "refers to ${ADMIN_PASSWORD}, which is not set where shipwick runs and not stored on the server" {
		t.Errorf("field = %v", field)
	}

	f.do("PUT", "/api/v1/secrets/ADMIN_PASSWORD", `{"value": "tiny-pw"}`)
	status, body = f.do("POST", "/api/v1/applications/my-api/deploy", protectedConfig)
	if field := fieldOf(t, status, body); field["field"] != "proxy.basic_auth[0].password" ||
		field["message"] != "with ${ADMIN_PASSWORD} filled in from the server's secrets, the password is too short: at least 8 characters" {
		t.Errorf("field = %v", field)
	}
	if strings.Contains(string(body), "tiny-pw") {
		t.Errorf("the response repeats the secret: %s", body)
	}

	f.do("PUT", "/api/v1/secrets/ADMIN_PASSWORD", `{"value": "correct horse"}`)
	status, body = f.do("POST", "/api/v1/applications/my-api/deploy", protectedConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy with a usable password stored: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	for _, path := range []string{"/api/v1/deployments/1", "/api/v1/applications/my-api", "/api/v1/deployments?application=my-api"} {
		_, body := f.do("GET", path, "")
		if strings.Contains(string(body), "correct horse") {
			t.Errorf("GET %s returns the password: %s", path, body)
		}
	}
	_, body = f.do("GET", "/api/v1/deployments/1", "")
	d := decode[api.DeploymentDetail](t, body)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s, error = %q", d.Status, d.Error)
	}
	p := d.Spec.Proxy
	if d.Spec.Path != "/api" || p == nil || len(p.BasicAuth) != 1 || p.BasicAuth[0].Password != "********" || p.BasicAuth[0].Username != "admin" || p.BasicAuth[0].Path != "/api/admin" {
		t.Errorf("spec = path %q, proxy %+v; want the block as written and the password masked", d.Spec.Path, p)
	}
	if p.Headers["X-Frame-Options"] != "DENY" || len(p.Redirects) != 1 || p.Redirects[0].Status != 308 {
		t.Errorf("proxy = %+v", p)
	}
	if strings.Contains(f.logs.String(), "correct horse") || strings.Contains(f.logs.String(), "tiny-pw") {
		t.Error("the log holds a password")
	}

	_, body = f.do("GET", "/api/v1/applications", "")
	if apps := decode[[]api.Application](t, body); len(apps) != 1 || apps[0].Domain != "example.com" || apps[0].Path != "/api" {
		t.Errorf("applications = %+v, want the path next to the domain", apps)
	}
}

func TestAPathAlreadyServedIsRefusedUnderItsField(t *testing.T) {
	f := newFixture(t)
	deploy := func(name, path string) (int, []byte) {
		config := "name: " + name + "\nimage: app:1\nport: 8080\ndomain: example.com\n"
		if path != "" {
			config += "path: " + path + "\n"
		}
		status, body := f.do("POST", "/api/v1/applications/"+name+"/deploy", config)
		f.engine.Wait()
		return status, body
	}
	for _, first := range [][2]string{{"web", ""}, {"api", "/api"}} {
		if status, body := deploy(first[0], first[1]); status != http.StatusAccepted {
			t.Fatalf("%s: status = %d, body = %s", first[0], status, body)
		}
	}

	status, body := deploy("other", "/API")
	if field := fieldOf(t, status, body); field["field"] != "path" ||
		field["message"] != `example.com/API is already served by application "api"; applications share a domain under different paths` {
		t.Errorf("field = %v", field)
	}
	status, body = deploy("other", "/")
	if field := fieldOf(t, status, body); field["field"] != "domain" || field["message"] != `already served by application "web"` {
		t.Errorf("field = %v", field)
	}
	if status, body := deploy("other", "/shop"); status != http.StatusAccepted {
		t.Errorf("a path nobody has: status = %d, body = %s", status, body)
	}
}
