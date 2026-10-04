package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// referring is an application whose secret values are all references.
const referring = `name: my-api
image: my-api:1.0
port: 8080
domain: api.example.com
env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app
  KEYS: ${API_KEY},$${NOT_ONE}
proxy:
  basic_auth:
    - username: admin
      password: ${ADMIN_PASSWORD}
`

func (h *harness) secrets(pairs ...string) {
	h.t.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := h.store.SetSecret(context.Background(), pairs[i], pairs[i+1], time.Now()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) deployDocument(document string) store.Deployment {
	h.t.Helper()
	a, err := spec.Parse([]byte(document))
	if err != nil {
		h.t.Fatalf("parse: %v\n%s", err, document)
	}
	d := h.deploy(a)
	if d.Status != api.StatusActive {
		h.t.Fatalf("deployment: status = %s, error = %q", d.Status, d.Error)
	}
	return d
}

func (h *harness) config(name string) api.ApplicationConfig {
	h.t.Helper()
	config, err := h.engine.Config(context.Background(), name, false)
	if err != nil {
		h.t.Fatalf("Config: %v", err)
	}
	return config
}

// wantReferences fails unless the document of the application says every
// reference of `referring` and no value.
func (h *harness) wantReferences(name string) api.ApplicationConfig {
	h.t.Helper()
	config := h.config(name)
	for _, reference := range []string{"postgres://app:${DB_PASSWORD}@postgres:5432/app", "${API_KEY},$${NOT_ONE}", "password: ${ADMIN_PASSWORD}"} {
		if !strings.Contains(config.Document, reference) {
			h.t.Errorf("the document does not say %s:\n%s", reference, config.Document)
		}
	}
	if len(config.Masked) != 0 || strings.Contains(config.Document, spec.Mask) {
		h.t.Errorf("masked = %v:\n%s", config.Masked, config.Document)
	}
	for _, value := range []string{"hunter2", "k-1", "correct horse", "rotated"} {
		if strings.Contains(config.Document, value) {
			h.t.Errorf("the document holds the value %q:\n%s", value, config.Document)
		}
	}
	return config
}

func TestADocumentDeployedAgainUnchangedStartsTheSameReplicas(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2", "API_KEY", "k-1", "ADMIN_PASSWORD", "correct horse")
	first := h.deployDocument(referring)
	before := h.rt.Spec(h.replica("my-api").ID)

	config := h.wantReferences("my-api")
	if config.Application != "my-api" || config.DeploymentID != first.ID || config.Sequence != 1 || config.Version != "1.0" || config.StaticDigest != "" {
		t.Errorf("config = %+v", config)
	}
	second := h.deployDocument(config.Document)
	after := h.rt.Spec(h.replica("my-api").ID)
	if second.ID == first.ID || !reflect.DeepEqual(after.Env, before.Env) {
		t.Errorf("env = %v, want %v", after.Env, before.Env)
	}
	if after.Env["DATABASE_URL"] != "postgres://app:hunter2@postgres:5432/app" || after.Env["KEYS"] != "k-1,${NOT_ONE}" {
		t.Errorf("env = %v", after.Env)
	}
	stored, err := h.store.GetDeployment(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	was, _ := h.store.GetDeployment(context.Background(), first.ID)
	if !reflect.DeepEqual(stored.Spec, was.Spec) {
		t.Errorf("spec = %+v, want %+v", stored.Spec, was.Spec)
	}
	// And the document of what runs now is the document that was deployed.
	if again := h.config("my-api"); again.Document != config.Document {
		t.Errorf("the document changed:\n%s\nwas:\n%s", again.Document, config.Document)
	}
}

func TestADocumentDeployedAgainResolvesItsSecretsAgain(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2", "API_KEY", "k-1", "ADMIN_PASSWORD", "correct horse")
	h.deployDocument(referring)
	config := h.config("my-api")

	h.secrets("DB_PASSWORD", "rotated")
	h.deployDocument(config.Document)
	if env := h.rt.Spec(h.replica("my-api").ID).Env; env["DATABASE_URL"] != "postgres://app:rotated@postgres:5432/app" {
		t.Errorf("env = %v", env)
	}
	h.wantReferences("my-api")
}

func TestALiteralSecretIsNeverReturnedAndItsMaskIsNeverDeployed(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2")
	h.deployDocument(`name: my-api
image: my-api:1.0
port: 8080
domain: api.example.com
env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app
  API_KEY: k-literal-1
  LOG_LEVEL: debug
proxy:
  basic_auth:
    - username: admin
      password: a literal password
`)
	config := h.config("my-api")
	want := []string{"env.API_KEY", "env.LOG_LEVEL", "proxy.basic_auth[0].password"}
	if !reflect.DeepEqual(config.Masked, want) {
		t.Errorf("masked = %v, want %v", config.Masked, want)
	}
	for _, value := range []string{"k-literal-1", "debug", "a literal password", "hunter2"} {
		if strings.Contains(config.Document, value) {
			t.Errorf("the document holds the value %q:\n%s", value, config.Document)
		}
	}
	if !strings.Contains(config.Document, "${DB_PASSWORD}") {
		t.Errorf("the reference is gone:\n%s", config.Document)
	}

	a, err := spec.Parse([]byte(config.Document))
	if err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, config.Document)
	}
	replica := h.replica("my-api").ID
	for name, try := range map[string]func() error{
		"deploy":   func() error { _, err := h.engine.Deploy(context.Background(), a); return err },
		"validate": func() error { return h.engine.Validate(context.Background(), a) },
	} {
		var masked *MaskedValuesError
		if err := try(); !errors.As(err, &masked) || !reflect.DeepEqual(masked.Masked, want) {
			t.Fatalf("%s: err = %v, want a refusal that names %v", name, err, want)
		}
		fields := masked.Fields()
		if len(fields) != 3 || fields[0].Field != "env.API_KEY" || !strings.Contains(fields[0].Expected, "shipwick secret set") || strings.Contains(fields[0].Message, "k-literal") {
			t.Errorf("fields = %+v", fields)
		}
	}
	h.engine.Wait()
	if now := h.replica("my-api").ID; now != replica {
		t.Error("the refused document replaced the replica")
	}
	if all, _ := h.store.ListDeployments(context.Background(), store.DeploymentFilter{Application: "my-api"}); len(all) != 1 {
		t.Errorf("a refused document was recorded: %d deployments", len(all))
	}

	// With every mask replaced it deploys, and the values are the new ones.
	fixed := strings.NewReplacer(
		`API_KEY: "********"`, "API_KEY: k-literal-2",
		`LOG_LEVEL: "********"`, "LOG_LEVEL: info",
		`password: "********"`, "password: another password",
	).Replace(config.Document)
	h.deployDocument(fixed)
	env := h.rt.Spec(h.replica("my-api").ID).Env
	if env["API_KEY"] != "k-literal-2" || env["LOG_LEVEL"] != "info" || env["DATABASE_URL"] != "postgres://app:hunter2@postgres:5432/app" {
		t.Errorf("env = %v", env)
	}
}

func TestTheMaskIsRefusedInADocumentThatNeverCameFromTheAgent(t *testing.T) {
	h := newHarness(t)
	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{"SECRET": spec.Mask, "OTHER": "fine"}
	var masked *MaskedValuesError
	if _, err := h.engine.Deploy(context.Background(), a); !errors.As(err, &masked) || !reflect.DeepEqual(masked.Masked, []string{"env.SECRET"}) {
		t.Fatalf("err = %v", err)
	}
	// A secret whose value happens to be eight asterisks is a value.
	h.secrets("STARS", spec.Mask)
	a.Env = map[string]string{"SECRET": "${STARS}"}
	if d := h.deploy(a); d.Status != api.StatusActive {
		t.Errorf("deployment: %+v", d)
	}

	static := spec.App{Name: "docs", Domain: "docs.example.com", Replicas: 1, Static: &spec.Static{Dir: "dist"},
		Proxy:   &spec.Proxy{BasicAuth: []spec.BasicAuth{{Username: "admin", Password: spec.Mask}}},
		Restart: spec.Restart{Policy: spec.RestartAlways}, Deploy: spec.Deploy{Strategy: spec.StrategyRolling}}
	if err := h.engine.Validate(context.Background(), static); !errors.As(err, &masked) || !reflect.DeepEqual(masked.Masked, []string{"proxy.basic_auth[0].password"}) {
		t.Errorf("static: err = %v", err)
	}
}

func TestReferencesSurviveARedeployAndARollback(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.secrets("DB_PASSWORD", "hunter2", "API_KEY", "k-1", "ADMIN_PASSWORD", "correct horse")
	h.deployDocument(referring)

	d, err := h.engine.Redeploy(ctx, "my-api", "my-api:1.1")
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	if config := h.wantReferences("my-api"); config.DeploymentID != d.ID || !strings.Contains(config.Document, "image: my-api:1.1") {
		t.Errorf("config = %+v", config)
	}

	// Another document in between, without references.
	plain := app("my-api", "my-api:2.0", 1)
	plain.Env = map[string]string{"ONLY": "a literal"}
	h.deploy(plain)
	if config := h.config("my-api"); !reflect.DeepEqual(config.Masked, []string{"env.ONLY"}) || strings.Contains(config.Document, "PASSWORD}") {
		t.Errorf("a document without references:\n%s", config.Document)
	}

	back, err := h.engine.Rollback(ctx, "my-api", 0)
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	if config := h.wantReferences("my-api"); config.DeploymentID != back.ID || !strings.Contains(config.Document, "image: my-api:1.1") {
		t.Errorf("config = %+v", config)
	}
	// What a rollback starts is what ran, not what the secrets say today.
	h.secrets("DB_PASSWORD", "rotated")
	again, err := h.engine.Redeploy(ctx, "my-api", "")
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	if env := h.rt.Spec(h.replica("my-api").ID).Env; env["DATABASE_URL"] != "postgres://app:hunter2@postgres:5432/app" {
		t.Errorf("env after the redeploy #%d = %v", again.Sequence, env)
	}
}

func TestReferencesSurviveAKeyRotation(t *testing.T) {
	h := newHarness(t)
	h.secrets("DB_PASSWORD", "hunter2", "API_KEY", "k-1", "ADMIN_PASSWORD", "correct horse")
	h.deployDocument(referring)
	before := h.config("my-api").Document

	if _, err := h.store.RotateKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := h.wantReferences("my-api").Document; after != before {
		t.Errorf("the document changed with the key:\n%s\nwas:\n%s", after, before)
	}
}

func TestAnImportedApplicationKeepsItsReferences(t *testing.T) {
	old := newHarness(t)
	old.secrets("DB_PASSWORD", "hunter2", "API_KEY", "k-1", "ADMIN_PASSWORD", "correct horse")
	old.deployDocument(referring)
	want := old.config("my-api").Document
	archive := old.export()

	h := newServer(t, otherKey)
	result := h.importExport(archive, ImportOptions{})
	if a := imported(t, result, "my-api"); a.Status != api.ImportAppImported {
		t.Fatalf("import: %+v", a)
	}
	h.engine.Wait()
	if got := h.wantReferences("my-api").Document; got != want {
		t.Errorf("the imported document:\n%s\nwant:\n%s", got, want)
	}
	// The secrets came along, so the document deploys there as it is.
	h.deployDocument(want)
	if env := h.rt.Spec(h.replica("my-api").ID).Env; env["DATABASE_URL"] != "postgres://app:hunter2@postgres:5432/app" {
		t.Errorf("env = %v", env)
	}
}

func TestAnExportWithoutReferencesImportsAsBefore(t *testing.T) {
	old := newHarness(t)
	old.deploy(app("my-api", "my-api:1.0", 1))
	h := newServer(t, otherKey)
	if a := imported(t, h.importExport(old.export(), ImportOptions{}), "my-api"); a.Status != api.ImportAppImported {
		t.Fatalf("import: %+v", a)
	}
	h.engine.Wait()
	if config := h.config("my-api"); !reflect.DeepEqual(config.Masked, []string{"env.SECRET"}) {
		t.Errorf("masked = %v", config.Masked)
	}
}

func TestImportedReferencesAreHeldToWhatADocumentCouldSay(t *testing.T) {
	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{"A": "x", "B": "y"}
	ok := &spec.References{Env: map[string]string{"A": "${A}-$${B}"}}
	if got, err := importedReferences(a, ok); err != nil || !reflect.DeepEqual(got, *ok) {
		t.Errorf("references = %+v, err = %v", got, err)
	}
	if got, err := importedReferences(a, nil); err != nil || !got.Empty() {
		t.Errorf("no references: %+v, %v", got, err)
	}
	for name, refs := range map[string]*spec.References{
		"a variable it does not have": {Env: map[string]string{"C": "${C}"}},
		"no reference in it":          {Env: map[string]string{"A": "a literal"}},
		"an account it does not have": {BasicAuth: map[int]string{0: "${P}"}},
		"a NUL byte":                  {Env: map[string]string{"A": "${A}\x00"}},
		"too large":                   {Env: map[string]string{"A": "${A}" + strings.Repeat("x", spec.MaxConfigBytes)}},
	} {
		if got, err := importedReferences(a, refs); err == nil {
			t.Errorf("%s: accepted as %+v", name, got)
		}
	}
}

func TestTheDocumentOfAnApplicationThatIsNotDeployed(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.Config(context.Background(), "nothing", false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
