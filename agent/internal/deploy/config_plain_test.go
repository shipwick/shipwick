package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// mixed has one value of each kind: a reference, two that stood in the file,
// and one the CLI filled in before it sent the document.
const mixed = `name: my-api
image: my-api:1.0
port: 8080
domain: api.example.com
env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app
  LOG_LEVEL: debug
  TEMPLATE: Hello $${NAME}
  API_KEY: sk-filled-in-by-the-cli
`

func (h *harness) deployWith(document string, plain ...string) {
	h.t.Helper()
	a, err := spec.Parse([]byte(document))
	if err != nil {
		h.t.Fatalf("parse: %v\n%s", err, document)
	}
	d, err := h.engine.DeployWith(context.Background(), a, plain)
	if err != nil {
		h.t.Fatalf("DeployWith: %v", err)
	}
	h.engine.Wait()
	if final, err := h.store.GetDeployment(context.Background(), d.ID); err != nil || final.Status != api.StatusActive {
		h.t.Fatalf("deployment: %+v, %v", final, err)
	}
}

// wantMixed fails unless the document says the two plain values, the
// reference, and a mask where the filled-in value is.
func (h *harness) wantMixed(name string) api.ApplicationConfig {
	h.t.Helper()
	config := h.config(name)
	for _, line := range []string{
		"  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app\n",
		"  LOG_LEVEL: debug\n",
		"  TEMPLATE: Hello $${NAME}\n",
		`  API_KEY: "********" # not handed out`,
	} {
		if !strings.Contains(config.Document, line) {
			h.t.Errorf("the document lacks %q:\n%s", line, config.Document)
		}
	}
	if !reflect.DeepEqual(config.Masked, []string{"env.API_KEY"}) || !reflect.DeepEqual(config.Plain, []string{"env.LOG_LEVEL", "env.TEMPLATE"}) {
		h.t.Errorf("masked = %v, plain = %v", config.Masked, config.Plain)
	}
	for _, value := range []string{"hunter2", "sk-filled-in-by-the-cli"} {
		if strings.Contains(config.Document, value) {
			h.t.Errorf("the document holds the value %q:\n%s", value, config.Document)
		}
	}
	return config
}

func TestAValueSaidToBePlainIsGivenBackAsItIs(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2")
	h.deployWith(mixed, "LOG_LEVEL", "TEMPLATE")
	config := h.wantMixed("my-api")

	// The document deploys again once the mask has a value, and the
	// containers get what the first deployment gave them.
	h.deployWith(strings.Replace(config.Document, `API_KEY: "********"`, "API_KEY: sk-filled-in-by-the-cli", 1), "LOG_LEVEL", "TEMPLATE")
	env := h.rt.Spec(h.replica("my-api").ID).Env
	if env["LOG_LEVEL"] != "debug" || env["TEMPLATE"] != "Hello ${NAME}" || env["API_KEY"] != "sk-filled-in-by-the-cli" {
		t.Errorf("env = %v", env)
	}
	h.wantMixed("my-api")
}

func TestAValueNobodySpokeForIsMasked(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2")
	h.deployWith(mixed)
	config := h.config("my-api")
	if !reflect.DeepEqual(config.Masked, []string{"env.API_KEY", "env.LOG_LEVEL", "env.TEMPLATE"}) || len(config.Plain) != 0 || strings.Contains(config.Document, "debug") {
		t.Errorf("masked = %v, plain = %v:\n%s", config.Masked, config.Plain, config.Document)
	}

	// What was said about one deployment is not said about the next.
	h.deployWith(mixed, "LOG_LEVEL")
	h.deployWith(mixed)
	if config := h.config("my-api"); len(config.Plain) != 0 || strings.Contains(config.Document, "debug") {
		t.Errorf("plain = %v:\n%s", config.Plain, config.Document)
	}
}

func TestAReferenceStaysAReferenceWhateverIsSaidAboutIt(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2")
	h.deployWith(mixed, "DATABASE_URL", "LOG_LEVEL", "LOG_LEVEL", "NOT_SET")
	config := h.config("my-api")
	if !reflect.DeepEqual(config.Plain, []string{"env.LOG_LEVEL"}) || strings.Contains(config.Document, "hunter2") {
		t.Errorf("plain = %v:\n%s", config.Plain, config.Document)
	}
	if !strings.Contains(config.Document, "postgres://app:${DB_PASSWORD}@postgres:5432/app") {
		t.Errorf("the reference is gone:\n%s", config.Document)
	}
}

func TestPlainValuesSurviveARedeployARollbackAndAKeyRotation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.secrets("DB_PASSWORD", "hunter2")
	h.deployWith(mixed, "LOG_LEVEL", "TEMPLATE")

	if _, err := h.engine.Redeploy(ctx, "my-api", "my-api:1.1"); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	if config := h.wantMixed("my-api"); !strings.Contains(config.Document, "image: my-api:1.1") {
		t.Errorf("after the redeploy:\n%s", config.Document)
	}

	other := app("my-api", "my-api:2.0", 1)
	other.Env = map[string]string{"LOG_LEVEL": "warn"}
	h.deploy(other)
	if config := h.config("my-api"); !reflect.DeepEqual(config.Masked, []string{"env.LOG_LEVEL"}) {
		t.Errorf("a deployment nobody spoke for: masked = %v", config.Masked)
	}
	if _, err := h.engine.Rollback(ctx, "my-api", 0); err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	h.wantMixed("my-api")

	if _, err := h.store.RotateKey(ctx); err != nil {
		t.Fatal(err)
	}
	h.wantMixed("my-api")
}

func TestAnImportedApplicationKeepsItsPlainValues(t *testing.T) {
	old := newHarness(t)
	old.secrets("DB_PASSWORD", "hunter2")
	old.deployWith(mixed, "LOG_LEVEL", "TEMPLATE")
	want := old.wantMixed("my-api").Document

	h := newServer(t, otherKey)
	if a := imported(t, h.importExport(old.export(), ImportOptions{}), "my-api"); a.Status != api.ImportAppImported {
		t.Fatalf("import: %+v", a)
	}
	h.engine.Wait()
	if got := h.wantMixed("my-api").Document; got != want {
		t.Errorf("the imported document:\n%s\nwant:\n%s", got, want)
	}
}

func TestImportedPlainValuesAreHeldToWhatTheConfigurationHas(t *testing.T) {
	a, err := spec.Parse([]byte(mixed))
	if err != nil {
		t.Fatal(err)
	}
	for name, carried := range map[string]spec.References{
		"a variable the configuration does not have": {Plain: []string{"GONE"}},
		"a value that is a reference":                {Env: map[string]string{"DATABASE_URL": "${DB_PASSWORD}"}, Plain: []string{"DATABASE_URL"}},
		"a name twice":                               {Plain: []string{"LOG_LEVEL", "LOG_LEVEL"}},
	} {
		if _, err := importedReferences(a, &carried); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if got, err := importedReferences(a, &spec.References{Plain: []string{"TEMPLATE", "LOG_LEVEL"}}); err != nil || !reflect.DeepEqual(got.Plain, []string{"LOG_LEVEL", "TEMPLATE"}) {
		t.Errorf("plain = %v, err = %v", got.Plain, err)
	}
}
