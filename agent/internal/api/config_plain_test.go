package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

const plainConfig = `name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
  LOG_LEVEL: debug
  REGION: eu-central
  API_KEY: sk-filled-in
`

func TestAValueTheDeploymentCalledPlainIsInTheDocument(t *testing.T) {
	f := newFixture(t)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)

	// Both ways of saying it: a list, and the parameter again.
	status, body := f.do("POST", "/api/v1/applications/my-api/deploy?plain=env.LOG_LEVEL,env.REGION&plain=env.LOG_LEVEL", plainConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy: status = %d, body = %s", status, body)
	}
	f.engine.Wait()

	status, body = f.do("GET", "/api/v1/applications/my-api/config", "")
	if status != http.StatusOK || strings.Contains(string(body), "hunter2") || strings.Contains(string(body), "sk-filled-in") {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	config := decode[api.ApplicationConfig](t, body)
	if !reflect.DeepEqual(config.Plain, []string{"env.LOG_LEVEL", "env.REGION"}) || !reflect.DeepEqual(config.Masked, []string{"env.API_KEY"}) {
		t.Errorf("plain = %v, masked = %v", config.Plain, config.Masked)
	}
	for _, line := range []string{"  LOG_LEVEL: debug\n", "  REGION: eu-central\n", `  API_KEY: "********" # not handed out`, "  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app\n"} {
		if !strings.Contains(config.Document, line) {
			t.Errorf("the document lacks %q:\n%s", line, config.Document)
		}
	}

	// Everywhere else the value stays masked: the statement is about the document.
	if _, body := f.do("GET", "/api/v1/applications/my-api", ""); strings.Contains(string(body), "debug") || strings.Contains(string(body), "eu-central") {
		t.Errorf("the application's detail shows a plain value: %s", body)
	}

	// The document that names its application takes the statement too.
	status, body = f.do("POST", "/api/v1/applications?plain=env.REGION", plainConfig)
	if status != http.StatusAccepted {
		t.Fatalf("deploy a document: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	if config := f.config("my-api", ""); !reflect.DeepEqual(config.Plain, []string{"env.REGION"}) || strings.Contains(config.Document, "debug") {
		t.Errorf("plain = %v:\n%s", config.Plain, config.Document)
	}
}

func TestADeploymentThatSaysNothingIsMaskedAsBefore(t *testing.T) {
	f := newFixture(t)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	if d := deployAndWait(t, f, plainConfig); d.Status != api.StatusActive {
		t.Fatalf("deploy: %+v", d)
	}
	_, body := f.do("GET", "/api/v1/applications/my-api/config", "")
	if !strings.Contains(string(body), `"plain":[]`) {
		t.Errorf("an agent that was told nothing says so with an empty list: %s", body)
	}
	if config := decode[api.ApplicationConfig](t, body); !reflect.DeepEqual(config.Masked, []string{"env.API_KEY", "env.LOG_LEVEL", "env.REGION"}) {
		t.Errorf("masked = %v", config.Masked)
	}
}

func TestPlainTakesEnvValuesOfTheDocumentAndNothingElse(t *testing.T) {
	f := newFixture(t)
	f.do("PUT", "/api/v1/secrets/DB_PASSWORD", `{"value": "hunter2"}`)
	for _, query := range []string{"plain=env.NOT_SET", "plain=LOG_LEVEL", "plain=proxy.basic_auth[0].password", "plain=env.LOG_LEVEL,env."} {
		status, body := f.do("POST", "/api/v1/applications/my-api/deploy?"+query, plainConfig)
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "is not an env value of the document") {
			t.Errorf("%s: status = %d, error = %+v", query, status, e)
		}
	}
	if _, body := f.do("GET", "/api/v1/applications", ""); len(decode[[]api.Application](t, body)) != 0 {
		t.Errorf("a refused statement deployed something: %s", body)
	}
}
