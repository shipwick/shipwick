package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestDeployFillsEnvPlaceholdersFromTheStoredSecrets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "DB_PASSWORD", "hunter2", time.Now())
	h.store.SetSecret(ctx, "API_KEY", "k-1", time.Now())

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
	h.store.SetSecret(ctx, "PRESENT", "here", time.Now())

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
	h.store.SetSecret(ctx, "DB_PASSWORD", "x", time.Now())
	h.store.SetSecret(ctx, "A", "x", time.Now())
	h.store.SetSecret(ctx, "B", "x", time.Now())
	if d := h.deploy(a); d.Status != api.StatusActive {
		t.Errorf("after storing the secrets: status = %s, error = %q", d.Status, d.Error)
	}
}

func TestAChangedSecretTakesEffectOnTheNextDeploymentOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SetSecret(ctx, "DB_PASSWORD", "first", time.Now())
	a := app("my-api", "my-api:1.0", 1)
	a.Env = map[string]string{"DB_PASSWORD": "${DB_PASSWORD}"}
	v1 := h.deploy(a)

	h.store.SetSecret(ctx, "DB_PASSWORD", "second", time.Now())
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
