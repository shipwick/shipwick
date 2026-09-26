package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const secretConfig = "name: my-api\nimage: ghcr.io/company/my-api:${TAG}\nport: 8080\nenv:\n  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app\n  LITERAL: $${NOT_A_PLACEHOLDER}\n  DOLLAR: $HOME\n"

func TestExpand(t *testing.T) {
	values := map[string]string{"TAG": "1.4.2", "DB_PASSWORD": "s3cret"}
	out, used, err := expand([]byte(secretConfig), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"my-api:1.4.2", "postgres://app:s3cret@postgres", "LITERAL: ${NOT_A_PLACEHOLDER}", "DOLLAR: $HOME"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Join(used, ",") != "TAG,DB_PASSWORD" {
		t.Errorf("used = %v", used)
	}

	_, _, err = expand([]byte(secretConfig), func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "${DB_PASSWORD}, ${TAG}") || !strings.Contains(err.Error(), "--env-file") {
		t.Errorf("err = %v", err)
	}
}

func TestDeploySubstitutesFromEnvironmentAndEnvFile(t *testing.T) {
	f := newFakeAgent(t)
	f.app.Name = "my-api"
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Application: "my-api", Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done}}}

	dir := writeConfig(t, secretConfig)
	envFile := filepath.Join(dir, "secrets.env")
	os.WriteFile(envFile, []byte("# comment\nexport DB_PASSWORD=\"from-file\"\nTAG=from-file\n"), 0o600)
	f.env = map[string]string{"TAG": "1.4.2"} // the environment wins over the file

	out, _, err := f.run(dir, "deploy", "--env-file", envFile)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(2 variables substituted)") {
		t.Errorf("no substitution note:\n%s", out)
	}
	sent, err := spec.Parse([]byte(f.deployBodies[0]))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Image != "ghcr.io/company/my-api:1.4.2" || sent.Env["DATABASE_URL"] != "postgres://app:from-file@postgres:5432/app" || sent.Env["LITERAL"] != "${NOT_A_PLACEHOLDER}" {
		t.Errorf("sent image=%s env=%v", sent.Image, sent.Env)
	}
	if strings.Contains(out, "from-file") || strings.Contains(out, "1.4.2@") {
		t.Errorf("a value was echoed:\n%s", out)
	}
}

func TestDeployRefusesUnsetPlaceholdersWithoutContactingTheAgent(t *testing.T) {
	f := newFakeAgent(t)
	_, _, err := f.run(writeConfig(t, secretConfig), "deploy")
	if err == nil || !strings.Contains(Render(err), "${DB_PASSWORD}") || !strings.Contains(Render(err), "not set") {
		t.Errorf("err = %v", err)
	}
	if len(f.deployBodies) != 0 {
		t.Error("nothing must be sent while a placeholder is unresolved")
	}
	if _, _, err := f.run(writeConfig(t, secretConfig), "validate"); err == nil {
		t.Error("validate must fail the same way")
	}
}

func TestStatusFindsTheNameDespitePlaceholders(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Status: api.AppHealthy}, ActiveDeployment: &api.Deployment{Version: "1.4.2"}}
	if out, _, err := f.run(writeConfig(t, secretConfig), "status"); err != nil {
		t.Errorf("status: %v\n%s", err, out)
	}
}

func TestDeploySeveralApplicationsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	f := newFakeAgent(t)
	done := fixedNow
	f.deploymentPolls = []api.DeploymentDetail{
		{Deployment: api.Deployment{ID: 1, Application: "api", Status: api.StatusActive, Version: "1.0", CompletedAt: &done}},
		{Deployment: api.Deployment{ID: 1, Application: "worker", Status: api.StatusFailed, CompletedAt: &done, Error: "exited with code 1"}},
	}
	dir := t.TempDir()
	for _, name := range []string{"api", "worker", "web"} {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "deploy.yaml"), []byte("name: "+name+"\nimage: ghcr.io/company/"+name+":1.0\n"), 0o644)
	}

	out, _, err := f.run(dir, "deploy", "-f", "api/deploy.yaml", "-f", "worker/deploy.yaml", "-f", "web/deploy.yaml")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Deploying api", "api 1.0  deployed in", "Deploying worker", "✗ Deployment failed", "Stopped at worker: 1 of 3 applications deployed."})
	if strings.Contains(out, "Deploying web") {
		t.Error("web must not be deployed after worker failed")
	}
	if len(f.deployBodies) != 2 {
		t.Errorf("%d deploy requests, want 2", len(f.deployBodies))
	}

	if _, _, err := f.run(dir, "deploy", "-f", "api/deploy.yaml", "-f", "web/deploy.yaml", "--image", "x:1"); err == nil || !strings.Contains(err.Error(), "--image applies to one application") {
		t.Errorf("err = %v", err)
	}
}

func TestExpandTouchesValuesOnly(t *testing.T) {
	in := "# ${DATABASE_PASSWORD} is filled in at deploy time\nname: my-api # not ${THIS}\nimage: nginx\nenv:\n  PORT: ${PORT}\n"
	out, used, err := expand([]byte(in), func(n string) (string, bool) { return map[string]string{"PORT": "8080"}[n], n == "PORT" })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(used, ",") != "PORT" {
		t.Errorf("used = %v", used)
	}
	app, err := spec.Parse(out)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, out)
	}
	if app.Env["PORT"] != "8080" {
		t.Errorf("env = %v", app.Env)
	}
	// A placeholder that stands for a number is still sent as the string the
	// user wrote, and a comment is left alone.
	if !strings.Contains(string(out), `"8080"`) && !strings.Contains(string(out), `'8080'`) && !strings.Contains(string(out), "PORT: 8080") {
		t.Errorf("unexpected rendering:\n%s", out)
	}

	// Without any placeholder the document is sent byte for byte.
	same, _, err := expand([]byte("name: a\nimage: nginx   # keep my spacing\n"), nil)
	if err != nil || string(same) != "name: a\nimage: nginx   # keep my spacing\n" {
		t.Errorf("a document without placeholders must be returned untouched: %q, %v", same, err)
	}
}
