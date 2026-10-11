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
)

func TestDeployFillsEnvPlaceholdersFromTheStoredSecrets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "", "DB_PASSWORD", "hunter2", time.Now())
	h.store.SetSecret(ctx, "", "API_KEY", "k-1", time.Now())

	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{
		"DATABASE_URL": "postgres://app:${DB_PASSWORD}@postgres:5432/app",
		"KEYS":         "${API_KEY},${API_KEY}",
		"LITERAL":      "$${DB_PASSWORD}",
		"PLAIN":        "$HOME",
	}
	d := h.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s, error = %q", d.Status, d.Error)
	}

	// The containers got the values, the document sent kept its placeholders.
	env := h.rt.Spec(h.rt.Containers()[0].ID).Env
	want := map[string]string{
		"DATABASE_URL": "postgres://app:hunter2@postgres:5432/app",
		"KEYS":         "k-1,k-1",
		"LITERAL":      "${DB_PASSWORD}",
		"PLAIN":        "$HOME",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("container env %s = %q, want %q", k, env[k], v)
		}
	}
	if a.Env["DATABASE_URL"] != "postgres://app:${DB_PASSWORD}@postgres:5432/app" {
		t.Error("the caller's spec must not be modified")
	}

	// The stored record holds the resolved values: a rollback to it needs no
	// secret that may have changed since.
	stored, _ := h.store.GetDeployment(ctx, d.ID)
	if stored.Spec.Env["DATABASE_URL"] != want["DATABASE_URL"] || stored.Spec.Env["LITERAL"] != "${DB_PASSWORD}" {
		t.Errorf("stored env = %v", stored.Spec.Env)
	}
	redacted := stored.Spec.Redacted()
	for k, v := range redacted.Env {
		if v != "********" {
			t.Errorf("redacted %s = %q", k, v)
		}
	}
	for _, e := range h.events(d.ID) {
		if strings.Contains(e.Message, "hunter2") || strings.Contains(e.Message, "k-1") {
			t.Errorf("event leaks a secret: %q", e.Message)
		}
	}
}

func TestDeployRefusesAPlaceholderNoSecretAnswers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "", "PRESENT", "here", time.Now())

	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{
		"DATABASE_URL": "postgres://app:${DB_PASSWORD}@postgres:5432/app",
		"OK":           "${PRESENT}",
		"TWO":          "${A}:${B}",
	}
	_, err := h.engine.Deploy(ctx, a)
	var missing *MissingSecretsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want MissingSecretsError", err)
	}
	fields := missing.Fields()
	if len(fields) != 3 {
		t.Fatalf("fields = %+v, want one per unresolved reference", fields)
	}
	if fields[0].Field != "env.DATABASE_URL" ||
		fields[0].Message != "refers to ${DB_PASSWORD}, which is not set where shipwick runs and not stored on the server" ||
		fields[0].Expected != "shipwick secret set DB_PASSWORD" {
		t.Errorf("unexpected field: %+v", fields[0])
	}
	if fields[1].Field != "env.TWO" || fields[2].Field != "env.TWO" || fields[1].Expected != "shipwick secret set A" || fields[2].Expected != "shipwick secret set B" {
		t.Errorf("unexpected fields: %+v", fields[1:])
	}
	if s := err.Error(); strings.Contains(s, "here") || !strings.Contains(s, "${DB_PASSWORD}") {
		t.Errorf("error = %q", s)
	}

	// Nothing was recorded, and the application is free for the next attempt.
	if list, _ := h.store.ListDeployments(ctx, store.DeploymentFilter{Application: "my-api"}); len(list) != 0 {
		t.Errorf("a refused deployment must leave no record, got %d", len(list))
	}
	h.store.SetSecret(ctx, "", "DB_PASSWORD", "x", time.Now())
	h.store.SetSecret(ctx, "my-api", "A", "x", time.Now())
	h.store.SetSecret(ctx, "", "B", "x", time.Now())
	if d := h.deploy(a); d.Status != api.StatusActive {
		t.Errorf("after storing the secrets: status = %s, error = %q", d.Status, d.Error)
	}
}

func TestAChangedSecretTakesEffectOnTheNextDeploymentOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "", "DB_PASSWORD", "first", time.Now())
	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{"DB_PASSWORD": "${DB_PASSWORD}"}
	v1 := h.deploy(a)

	h.store.SetSecret(ctx, "", "DB_PASSWORD", "second", time.Now())
	// A redeploy re-uses the stored spec: the value it was deployed with.
	d, err := h.engine.Redeploy(ctx, "my-api", "")
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	redeployed, _ := h.store.GetDeployment(ctx, d.ID)
	if redeployed.Spec.Env["DB_PASSWORD"] != "first" || v1.Spec.Env["DB_PASSWORD"] != "first" {
		t.Errorf("redeploy env = %q, want the stored value", redeployed.Spec.Env["DB_PASSWORD"])
	}
	// A deploy resolves afresh.
	if v3 := h.deploy(a); v3.Spec.Env["DB_PASSWORD"] != "second" {
		t.Errorf("deploy env = %q, want the new value", v3.Spec.Env["DB_PASSWORD"])
	}
}

func TestAnApplicationsOwnSecretComesBeforeTheServersAndOtherApplicationsDoNotSeeIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "", "DB_PASSWORD", "shared", time.Now())
	h.store.SetSecret(ctx, "", "MAIL_KEY", "shared-mail", time.Now())
	h.store.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-own", time.Now())
	h.store.SetSecret(ctx, "api", "ONLY_API", "api-only", time.Now())

	env := func(name string) map[string]string {
		a := app(name, name+":1.0", 1)
		a.Env = map[string]string{"DB": "${DB_PASSWORD}", "MAIL": "${MAIL_KEY}"}
		d := h.deploy(a)
		if d.Status != api.StatusActive {
			t.Fatalf("%s: status = %s, error = %q", name, d.Status, d.Error)
		}
		return h.rt.Spec(h.replica(name).ID).Env
	}
	if got := env("api"); got["DB"] != "api-s-own" || got["MAIL"] != "shared-mail" {
		t.Errorf("api's env = %v: want its own DB_PASSWORD and the server's MAIL_KEY", got)
	}
	if got := env("web"); got["DB"] != "shared" || got["MAIL"] != "shared-mail" {
		t.Errorf("web's env = %v: want the server's for both", got)
	}

	// A secret of api is not web's: web is refused as if it did not exist.
	w := app("web", "web:1.1", 1)
	w.Env = map[string]string{"X": "${ONLY_API}"}
	_, err := h.engine.Deploy(ctx, w)
	var missing *MissingSecretsError
	if !errors.As(err, &missing) || missing.Fields()[0].Expected != "shipwick secret set ONLY_API" {
		t.Errorf("err = %v, want ONLY_API missing for web", err)
	}
}

func TestTheReferencesRecordWhichSecretAnsweredAndARollbackCarriesItOn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "", "DB_PASSWORD", "shared", time.Now())
	h.store.SetSecret(ctx, "", "MAIL_KEY", "shared-mail", time.Now())
	h.store.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-own", time.Now())

	a := app("api", "api:1.0", 1)
	a.Env = map[string]string{"DB": "${DB_PASSWORD}", "MAIL": "${MAIL_KEY}", "BOTH": "${DB_PASSWORD}:${MAIL_KEY}"}
	v1 := h.deploy(a)
	refs, err := h.store.DeploymentReferences(ctx, v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Env) != 3 || !reflect.DeepEqual(refs.Scoped, []string{"DB_PASSWORD"}) {
		t.Errorf("references = %+v: want the three texts and DB_PASSWORD named as api's own", refs)
	}

	// Later deployments of api see a changed world; a rollback does not look.
	h.store.DeleteSecret(ctx, "api", "DB_PASSWORD")
	a.Image = "api:1.1"
	v2 := h.deploy(a)
	if refs, _ := h.store.DeploymentReferences(ctx, v2.ID); len(refs.Scoped) != 0 || v2.Spec.Env["DB"] != "shared" {
		t.Errorf("v2 = env %v, references %+v: want the server's DB_PASSWORD and nothing scoped", v2.Spec.Env, refs)
	}
	rb := h.finish(h.engine.Rollback(ctx, "api", v1.ID))
	refs, _ = h.store.DeploymentReferences(ctx, rb.ID)
	if rb.Spec.Env["DB"] != "api-s-own" || !reflect.DeepEqual(refs.Scoped, []string{"DB_PASSWORD"}) {
		t.Errorf("rollback = env %v, references %+v: want v1's value and v1's record of where it came from", rb.Spec.Env, refs)
	}
	// And the document gives the reference back, whichever secret answered.
	config, err := h.engine.Config(ctx, "api", false)
	if err != nil || !strings.Contains(config.Document, "BOTH: ${DB_PASSWORD}:${MAIL_KEY}") || strings.Contains(config.Document, "api-s-own") {
		t.Errorf("config = %+v, %v", config, err)
	}
}
