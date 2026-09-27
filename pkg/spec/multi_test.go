package spec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manyConfig = `
apps:
  - name: postgres
    image: postgres:17
    port: 5432
    volumes: [{ name: data, path: /var/lib/postgresql/data }]
    health: { tcp: 5432 }
    deploy: { strategy: recreate }
  - name: api
    image: ghcr.io/company/api:2.3.0
    port: 8080
    domain: api.example.com
    after: [postgres]
  - name: web
    image: ghcr.io/company/web:2.3.0
    port: 3000
    domain: example.com
`

func TestParseManyEveryEntryIsADeployYAML(t *testing.T) {
	entries, err := ParseMany([]byte(manyConfig))
	if err != nil {
		t.Fatalf("ParseMany: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	names := []string{entries[0].App.Name, entries[1].App.Name, entries[2].App.Name}
	if strings.Join(names, ",") != "postgres,api,web" {
		t.Errorf("entries out of file order: %v", names)
	}
	if entries[0].App.Deploy.Strategy != StrategyRecreate || len(entries[0].App.Volumes) != 1 || entries[1].App.Domain != "api.example.com" {
		t.Errorf("entries were not parsed like deploy.yaml: %+v", entries)
	}
	if len(entries[0].After) != 0 || strings.Join(entries[1].After, ",") != "postgres" || len(entries[2].After) != 0 {
		t.Errorf("unexpected after: %v %v %v", entries[0].After, entries[1].After, entries[2].After)
	}

	// What the agent receives is an ordinary deploy.yaml, without `after`.
	for _, e := range entries {
		if strings.Contains(string(e.Config), "after") {
			t.Errorf("after must not reach the agent:\n%s", e.Config)
		}
		app, err := Parse(e.Config)
		if err != nil {
			t.Errorf("Config of %s is not a valid deploy.yaml: %v\n%s", e.App.Name, err, e.Config)
		}
		if app.Name != e.App.Name || app.Image != e.App.Image {
			t.Errorf("Config of %s describes %s/%s", e.App.Name, app.Name, app.Image)
		}
	}
}

func TestIsMany(t *testing.T) {
	if !IsMany([]byte(manyConfig)) {
		t.Error("a file with apps is a shipwick.yaml")
	}
	for _, data := range []string{fullConfig, "", "- a\n- b\n", "name: [unclosed"} {
		if IsMany([]byte(data)) {
			t.Errorf("%q is not a shipwick.yaml", data)
		}
	}
}

func TestParseManyReportsEveryMistakeByEntry(t *testing.T) {
	_, err := ParseMany([]byte(`
apps:
  - name: api
    image: nginx
    prot: 80
  - name: worker
    image: nginx
    resources: { memory: abc }
  - name: Bad Name
    image: nginx
`))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	if !strings.HasPrefix(verr.Error(), "invalid shipwick.yaml\n") {
		t.Errorf("the report must name the file it judges:\n%s", verr)
	}
	want := map[string]string{
		"apps[0]":                  `unknown field "prot"`,
		"apps[1].resources.memory": `invalid value "abc"`,
		"apps[2].name":             "",
	}
	for field, msg := range want {
		if got, ok := findField(verr, field); !ok || !strings.Contains(got, msg) {
			t.Errorf("missing %s (%s) in:\n%s", field, msg, verr)
		}
	}
	for _, f := range verr.Fields {
		if strings.HasPrefix(f.Field, "line ") || f.Field == "deploy.yaml" {
			t.Errorf("a line number of the re-encoded entry means nothing to the user: %+v", f)
		}
	}
}

func TestParseManyDependencyGraph(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // a fragment of the report
	}{
		{"unknown name", "apps:\n  - name: api\n    image: nginx\n    after: [db]\n", `apps[0].after:` + "\n  " + `"db" is not an application in this file`},
		{"self reference", "apps:\n  - name: api\n    image: nginx\n    after: [api]\n", "apps[0].after:\n  api cannot wait for itself"},
		{"duplicate names", "apps:\n  - name: api\n    image: nginx\n  - name: api\n    image: nginx:2\n", `apps[1].name:` + "\n  " + `"api" is also the name of apps[0]`},
		{"cycle", "apps:\n  - name: a\n    image: nginx\n    after: [c]\n  - name: b\n    image: nginx\n    after: [a]\n  - name: c\n    image: nginx\n    after: [b]\n", "a → c → b → a: an application cannot wait for itself"},
		{"after is a list", "apps:\n  - name: api\n    image: nginx\n    after: postgres\n", "apps[0].after:\n  must be a list of application names"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMany([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v\nwant a report containing %q", err, tt.want)
			}
		})
	}

	// A dependency may point forward in the file: order comes from the graph.
	if _, err := ParseMany([]byte("apps:\n  - name: api\n    image: nginx\n    after: [db]\n  - name: db\n    image: postgres:17\n")); err != nil {
		t.Errorf("a forward reference is allowed: %v", err)
	}
}

func TestParseManyRefusesTheOtherKindOfFile(t *testing.T) {
	// A deploy.yaml's keys next to apps: whose name is `name`?
	_, err := ParseMany([]byte("name: api\nimage: nginx\napps:\n  - name: web\n    image: nginx\n"))
	if err == nil || !strings.Contains(err.Error(), "name:\n  cannot be set at the top of shipwick.yaml") {
		t.Errorf("err = %v", err)
	}

	for _, data := range []string{fullConfig, "", "- a\n", "apps: []\n", "apps: postgres\n"} {
		if _, err := ParseMany([]byte(data)); err == nil {
			t.Errorf("ParseMany(%q) must fail: it is not a list of applications", data)
		}
	}
}

func findField(verr *ValidationError, field string) (string, bool) {
	for _, f := range verr.Fields {
		if f.Field == field {
			return f.Message, true
		}
	}
	return "", false
}

func TestParseManyExampleFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "shipwick.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseMany(data)
	if err != nil {
		t.Fatalf("the example must stay valid: %v", err)
	}
	if len(entries) != 3 || entries[1].App.Name != "api" || strings.Join(entries[1].After, ",") != "postgres" {
		t.Errorf("the example describes postgres, api after postgres, web; got %+v", entries)
	}
}
