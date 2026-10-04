package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const returnedDocument = `name: my-api

image: ghcr.io/company/my-api:1.4.2

command: ["sh", "-c", "exec api --dir $${DATA_DIR}"]

replicas: 1

env:
  DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
  LOG_LEVEL: "********" # not handed out: write the value again, or refer to a secret as ${NAME}

restart:
  policy: always
`

// configAgent answers GET …/config, or 404 like an agent that predates it.
type configAgent struct {
	*fakeAgent
	config  *api.ApplicationConfig
	queries []string
}

func newConfigAgent(t *testing.T) *configAgent {
	t.Helper()
	a := &configAgent{fakeAgent: &fakeAgent{t: t}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.config == nil || r.Method != "GET" || r.URL.Path != "/api/v1/applications/"+a.config.Application+"/config" {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
			return
		}
		a.queries = append(a.queries, r.URL.RawQuery)
		respond(w, 200, a.config)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func TestConfigPrintsTheDocumentAndSaysWhatIsMissingBesideIt(t *testing.T) {
	a := newConfigAgent(t)
	a.config = &api.ApplicationConfig{Application: "my-api", DeploymentID: 12, Sequence: 7, Version: "1.4.2",
		Document: returnedDocument, Masked: []string{"env.LOG_LEVEL"}}

	out, errOut, err := a.run(t.TempDir(), "config", "my-api")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	// Standard output is the file and nothing else: it can be redirected.
	if out != returnedDocument {
		t.Errorf("stdout:\n%s\nwant the document as the agent wrote it", out)
	}
	assertInOrder(t, errOut, []string{`1 value is not handed out and stands as "********" in the file: env.LOG_LEVEL`,
		"Write it again, or store it with shipwick secret set NAME and refer to it as ${NAME}.", "Until then shipwick deploy refuses the file."})
	// Asked for as a file the CLI reads.
	if len(a.queries) != 1 || a.queries[0] != "escape=true" {
		t.Errorf("queries = %v", a.queries)
	}
}

func TestConfigWritesAFileThatDeployReadsAsTheSameConfiguration(t *testing.T) {
	a := newConfigAgent(t)
	document := strings.Replace(returnedDocument, `"********" # not handed out: write the value again, or refer to a secret as ${NAME}`, "${LOG_LEVEL}", 1)
	a.config = &api.ApplicationConfig{Application: "my-api", DeploymentID: 12, Sequence: 7, Version: "1.4.2", Document: document, Masked: []string{}}
	dir := t.TempDir()

	out, errOut, err := a.run(dir, "config", "my-api", "-o", "deploy.yaml")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if out != "✓ Wrote deploy.yaml: my-api as deployment #7 (1.4.2) runs it\n" || errOut != "" {
		t.Errorf("out = %q, err = %q", out, errOut)
	}
	written, err := os.ReadFile(filepath.Join(dir, "deploy.yaml"))
	if err != nil || string(written) != document {
		t.Fatalf("file = %q, err = %v", written, err)
	}

	// What deploy would send: the references left for the server, and the
	// ${DATA_DIR} of the command a literal again.
	c, _ := newRoot(Options{Getenv: func(string) string { return "" }})
	data, app, vars, err := c.loadConfig(filepath.Join(dir, "deploy.yaml"), nil, "")
	if err != nil {
		t.Fatalf("the written file does not load: %v", err)
	}
	if app.Command[2] != "exec api --dir ${DATA_DIR}" || app.Env["DATABASE_URL"] != "postgres://app:${DB_PASSWORD}@db:5432/app" {
		t.Errorf("command = %q, env = %v", app.Command, app.Env)
	}
	if len(vars.substituted) != 0 || strings.Join(vars.deferred, " ") != "DB_PASSWORD LOG_LEVEL" {
		t.Errorf("placeholders = %+v", vars)
	}
	if sent, err := spec.Parse(data); err != nil || sent.Command[2] != "exec api --dir ${DATA_DIR}" {
		t.Errorf("sent = %+v, err = %v", sent.Command, err)
	}

	// An existing file is the user's until they say otherwise.
	_, _, err = a.run(dir, "config", "my-api", "-o", "deploy.yaml")
	if err == nil || Render(err) != "Error: deploy.yaml already exists\n\nOverwrite it with: shipwick config my-api -o deploy.yaml --force" {
		t.Errorf("a second time: %v", err)
	}
	if _, _, err := a.run(dir, "config", "my-api", "-o", "deploy.yaml", "--force"); err != nil {
		t.Errorf("--force: %v", err)
	}
}

func TestConfigSaysSoWhenTheAgentIsOlder(t *testing.T) {
	a := newConfigAgent(t)
	_, _, err := a.run(t.TempDir(), "config", "my-api")
	want := "Error: the agent is older than this shipwick and does not write an application's deploy.yaml\n\nCompare versions with: shipwick server status"
	if err == nil || Render(err) != want {
		t.Errorf("err = %v\nrendered: %s", err, Render(err))
	}
}

func TestDeployRendersARefusedMaskAsAFieldToFillIn(t *testing.T) {
	f := newFakeAgent(t)
	f.deployStatus = 400
	f.deployError = api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{"fields": []any{map[string]any{
		"field": "env.LOG_LEVEL", "message": "******** is what the server shows in the place of this value, not the value",
		"expected": "the value itself, or ${NAME} with the value stored by shipwick secret set NAME"}}}}
	dir := writeConfig(t, returnedDocument)

	_, _, err := f.run(dir, "deploy")
	want := "invalid deploy.yaml\n\nenv.LOG_LEVEL:\n  ******** is what the server shows in the place of this value, not the value\n  expected: the value itself, or ${NAME} with the value stored by shipwick secret set NAME\n"
	if err == nil || Render(err) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", Render(err), want)
	}
}
