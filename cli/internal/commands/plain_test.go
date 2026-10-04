package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

// plainConfig has an env value of every kind the CLI tells apart.
const plainConfig = `name: my-api
image: ghcr.io/company/my-api:${TAG}
port: 8080
env:
  LOG_LEVEL: debug
  WORKERS: 4
  TEMPLATE: Hello $${NAME}
  API_KEY: ${API_KEY}
  DATABASE_URL: postgres://app:${DB_PASSWORD}@postgres:5432/app
  CACHE_URL: redis://${CACHE_HOST}:6379
  SHARED: &shared ${API_KEY}
  AGAIN: *shared
`

func TestExpandNamesTheEnvValuesItLeftAsTheFileWroteThem(t *testing.T) {
	values := map[string]string{"TAG": "1.4.2", "API_KEY": "sk-1", "CACHE_HOST": "cache"}
	_, used, err := expand([]byte(plainConfig), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	// Not API_KEY and CACHE_URL, which were filled in here; not AGAIN, which
	// repeats a value that was. DATABASE_URL is the file's own text: the agent
	// knows a reference when it sees one.
	if want := [][]string{{"LOG_LEVEL", "WORKERS", "TEMPLATE", "DATABASE_URL"}}; !reflect.DeepEqual(used.plain, want) {
		t.Errorf("plain = %v, want %v", used.plain, want)
	}

	many := "apps:\n  - name: api\n    image: api:1\n    env:\n      LOG_LEVEL: debug\n      API_KEY: ${API_KEY}\n  - name: db\n    image: postgres:17\n  - name: web\n    image: web:1\n    env:\n      API_URL: http://api:8080\n"
	_, used, err = expand([]byte(many), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"LOG_LEVEL"}, nil, {"API_URL"}}; !reflect.DeepEqual(used.plain, want) {
		t.Errorf("plain of a shipwick.yaml = %v, want %v", used.plain, want)
	}
}

func TestDeployTellsTheAgentWhichEnvValuesStoodInTheFile(t *testing.T) {
	f := newFakeAgent(t)
	f.deploymentPolls = []api.DeploymentDetail{{Deployment: api.Deployment{ID: 1, Status: api.StatusActive, CompletedAt: &fixedNow}}}
	f.env = map[string]string{"TAG": "1.4.2", "API_KEY": "sk-1", "CACHE_HOST": "cache"}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(plainConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, _, err := f.run(dir, "deploy"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if want := []string{"env.LOG_LEVEL,env.WORKERS,env.TEMPLATE,env.DATABASE_URL"}; !reflect.DeepEqual(f.deployPlain, want) {
		t.Errorf("plain = %q, want %q", f.deployPlain, want)
	}
	for _, name := range []string{"API_KEY", "CACHE_URL", "SHARED", "AGAIN"} {
		if strings.Contains(f.deployPlain[0], "env."+name) {
			t.Errorf("%s was filled in here and must not be called plain: %s", name, f.deployPlain[0])
		}
	}

	// A file without env says nothing.
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, err := f.run(dir, "deploy"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if f.deployPlain[1] != "" || strings.Contains(f.requests[len(f.requests)-1], "plain") {
		t.Errorf("plain = %q", f.deployPlain[1])
	}
}

func TestDeployManyTellsTheAgentPerApplication(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("api", "db", "web")
	f.env = map[string]string{"API_KEY": "sk-1"}
	many := "apps:\n  - name: api\n    image: api:1\n    env:\n      LOG_LEVEL: debug\n      API_KEY: ${API_KEY}\n  - name: db\n    image: postgres:17\n  - name: web\n    image: web:1\n    env:\n      API_URL: http://api:8080\n"

	// Named out of order and one left out: what was learned about the file
	// is still matched to the application it was learned about.
	if out, _, err := f.run(writeMany(t, many), "deploy", "--parallel", "1", "web", "api"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if len(f.deployBodies) != 2 {
		t.Fatalf("got %d deployments, want 2", len(f.deployBodies))
	}
	for i, body := range f.deployBodies {
		want := "env.API_URL"
		if strings.Contains(body, "name: api") {
			want = "env.LOG_LEVEL"
		}
		if f.deployPlain[i] != want {
			t.Errorf("plain = %q, want %q, for:\n%s", f.deployPlain[i], want, body)
		}
	}
}
