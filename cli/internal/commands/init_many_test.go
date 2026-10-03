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

const oneEntry = "apps:\n  - name: api\n    image: ghcr.io/company/api:${TAG}\n    port: 8080\n"

const twoEntries = `# The stack.
apps:
  # The database first.
  - name: postgres
    image: postgres:17

  - name: api
    image: ghcr.io/company/api:2.3.0   # pinned
    port: 8080
    after: [postgres]
`

func TestCommandsTakeTheNameFromAShipwickYAMLWithOneEntry(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "api"}}

	// ${TAG} is set nowhere here; the name is all that is wanted.
	if out, _, err := f.run(writeMany(t, oneEntry), "status"); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !requested(f, "GET /api/v1/applications/api") {
		t.Errorf("status should have asked for api: %v", f.requests)
	}

	// -f naming such a file is the same.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(oneEntry), 0o644); err != nil {
		t.Fatal(err)
	}
	f.requests = nil
	if _, _, err := f.run(dir, "status", "-f", "stack.yaml"); err != nil {
		t.Fatalf("status -f: %v", err)
	}
	if !requested(f, "GET /api/v1/applications/api") {
		t.Errorf("status -f should have asked for api: %v", f.requests)
	}
}

func TestCommandsAskWhichApplicationOfSeveral(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeMany(t, twoEntries)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "shipwick status postgres"},
		{[]string{"logs"}, "shipwick logs postgres"},
		{[]string{"stop"}, "shipwick stop postgres"},
		{[]string{"start"}, "shipwick start postgres"},
		{[]string{"rollback"}, "shipwick rollback postgres"},
		{[]string{"redeploy"}, "shipwick redeploy postgres"},
		{[]string{"jobs"}, "shipwick jobs postgres"},
		{[]string{"open"}, "shipwick open postgres"},
		{[]string{"backup"}, "shipwick backup postgres"},
		{[]string{"restore", "data.tar"}, "shipwick restore postgres <archive.tar>"},
		{[]string{"run", "--", "env"}, "shipwick run postgres -- <command> [args...]"},
	} {
		t.Run(tt.args[0], func(t *testing.T) {
			f.requests = nil
			_, _, err := f.run(dir, tt.args...)
			want := "shipwick.yaml describes 2 applications: postgres, api\n\nSay which one, e.g.: " + tt.want
			if err == nil || err.Error() != want {
				t.Errorf("err = %v\nwant  %s", err, want)
			}
			if len(f.requests) != 0 {
				t.Errorf("the agent is not asked before the application is known: %v", f.requests)
			}
		})
	}
}

func TestDeployYAMLComesBeforeShipwickYAML(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api"}}
	dir := writeConfig(t, validConfig)
	if err := os.WriteFile(filepath.Join(dir, spec.MultiFile), []byte(twoEntries), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(dir, "status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !requested(f, "GET /api/v1/applications/my-api") {
		t.Errorf("deploy.yaml names the application when both files exist: %v", f.requests)
	}

	// Neither file: the error names the one that was looked for, and the command.
	_, _, err := f.run(t.TempDir(), "logs")
	if err == nil || !strings.Contains(err.Error(), "deploy.yaml was not found here") || !strings.Contains(err.Error(), "shipwick logs my-api") {
		t.Errorf("err = %v", err)
	}
}

func TestInitAddsAnEntryToShipwickYAML(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{
		spec.MultiFile:          twoEntries,
		"web/package.json":      `{"dependencies":{"nuxt":"^4"},"scripts":{"build":"nuxt build"}}`,
		"web/package-lock.json": "{}",
	})

	out, _, err := f.run(dir, "init", "web", "--domain", "www.example.com")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Recognised a Nuxt application",
		"✓ Wrote web/Dockerfile and web/.dockerignore",
		"✓ Added web to shipwick.yaml",
		"Review them, then run: shipwick deploy",
	})

	// The file as it was, then the entry: indented like its neighbours and
	// set apart by an empty line as they are.
	want := twoEntries + "\n  - name: web\n    build: ./web\n    init: true\n    port: 3000\n    domain: www.example.com\n    health:\n      path: /\n"
	if got := readOrEmpty(t, dir, spec.MultiFile); got != want {
		t.Errorf("shipwick.yaml:\n%s\nwant:\n%s", got, want)
	}
	entries, err := spec.ParseMany([]byte(want))
	if err != nil || len(entries) != 3 || entries[2].App.Build == nil || entries[2].App.Build.Context != "web" {
		t.Fatalf("the result must be a valid shipwick.yaml: %v", err)
	}
	if !strings.Contains(readOrEmpty(t, dir, "web/Dockerfile"), "RUN npm run build") || readOrEmpty(t, dir, "web/.dockerignore") == "" {
		t.Error("the Dockerfile and .dockerignore belong in the project's directory")
	}
	if readOrEmpty(t, dir, DefaultFile) != "" || readOrEmpty(t, dir, "Dockerfile") != "" {
		t.Error("nothing is written next to shipwick.yaml")
	}

	// The name is taken now.
	_, _, err = f.run(dir, "init", "web")
	if err == nil || !strings.Contains(err.Error(), "shipwick.yaml already has an application named web") {
		t.Errorf("err = %v", err)
	}
	if got := readOrEmpty(t, dir, spec.MultiFile); got != want {
		t.Errorf("a refused init must not change the file:\n%s", got)
	}
}

func TestInitAddsWhatTheCurrentDirectoryHolds(t *testing.T) {
	f := newFakeAgent(t)

	// An image, with the file's own indentation and line endings.
	crlf := "apps:\r\n    -   name: api\r\n        image: nginx:1.27\r\n"
	dir := writeTree(t, map[string]string{spec.MultiFile: crlf})
	if out, _, err := f.run(dir, "init", "--name", "cache", "--image", "redis:7", "--port", "6379"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if got := readOrEmpty(t, dir, spec.MultiFile); got != crlf+"    -   name: cache\r\n        image: redis:7\r\n        port: 6379\r\n" {
		t.Errorf("shipwick.yaml = %q", got)
	}

	// A built site in a folder: the path is the one from shipwick.yaml.
	dir = writeTree(t, map[string]string{
		spec.MultiFile:      oneEntry,
		"site/package.json": `{"devDependencies":{"vite":"^6"},"scripts":{"build":"vite build"}}`,
	})
	if out, _, err := f.run(dir, "init", "site", "--domain", "example.com"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	want := oneEntry + "  - name: site\n    # What `npm run build` writes; run it in site/ before `shipwick deploy`.\n    static: site/dist/\n    domain: example.com\n"
	if got := readOrEmpty(t, dir, spec.MultiFile); got != want {
		t.Errorf("shipwick.yaml:\n%s\nwant:\n%s", got, want)
	}
	if readOrEmpty(t, dir, "site/Dockerfile") != "" {
		t.Error("a static site needs no Dockerfile")
	}

	// The project next to shipwick.yaml itself.
	dir = writeTree(t, map[string]string{spec.MultiFile: oneEntry, "go.mod": "module example.com/worker\n\ngo 1.27\n", "main.go": "package main\n"})
	if out, _, err := f.run(dir, "init", "--name", "worker"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if got := readOrEmpty(t, dir, spec.MultiFile); got != oneEntry+"  - name: worker\n    build: .\n    port: 8080\n" {
		t.Errorf("shipwick.yaml:\n%s", got)
	}
	if readOrEmpty(t, dir, "Dockerfile") == "" {
		t.Error("the Dockerfile belongs next to the project")
	}
}

func TestInitLeavesAShipwickYAMLItCannotEditSafely(t *testing.T) {
	f := newFakeAgent(t)
	file := "apps:\n  - name: api\n    image: nginx:1.27\nx-notes: apps is not the last key here\n"
	dir := writeTree(t, map[string]string{
		spec.MultiFile:     file,
		"worker/go.mod":    "module example.com/worker\n\ngo 1.27\n",
		"worker/main.go":   "package main\n",
		"worker/README.md": "",
	})

	out, _, err := f.run(dir, "init", "worker")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v, want ErrReported: the entry was not added", err)
	}
	if got := readOrEmpty(t, dir, spec.MultiFile); got != file {
		t.Errorf("the file must be left as it is:\n%s", got)
	}
	assertInOrder(t, out, []string{
		"✓ Wrote worker/Dockerfile and worker/.dockerignore",
		"✗ Left shipwick.yaml as it is: apps is not its last key",
		"Add this to its apps list by hand:",
		"  - name: worker\n    build: ./worker\n    port: 8080\n",
	})
}

func TestInitWithADirectoryNeedsAShipwickYAML(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{"web/index.html": "<html>"})
	_, _, err := f.run(dir, "init", "web")
	if err == nil || !strings.Contains(err.Error(), "there is none here") || !strings.Contains(err.Error(), "inside web") {
		t.Errorf("err = %v", err)
	}

	dir = writeTree(t, map[string]string{spec.MultiFile: oneEntry})
	for args, want := range map[string]string{
		"..":                "is outside this directory",
		"missing":           "is not a directory here",
		". --image nginx:1": "leave one of them out",
	} {
		_, _, err := f.run(dir, append([]string{"init"}, strings.Fields(args)...)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("init %s: err = %v", args, err)
		}
	}
	if got := readOrEmpty(t, dir, spec.MultiFile); got != oneEntry {
		t.Errorf("a refused init must not change the file:\n%s", got)
	}
}

func TestAppendEntry(t *testing.T) {
	entry := []string{"name: web", "image: nginx:1.27"}
	tests := []struct {
		name string
		file string
		want string // the whole result, or "" when the file must be refused
	}{
		{"after the last entry", "apps:\n  - name: api\n    image: a:1\n", "apps:\n  - name: api\n    image: a:1\n  - name: web\n    image: nginx:1.27\n"},
		{"a list at the margin", "apps:\n- name: api\n  image: a:1\n", "apps:\n- name: api\n  image: a:1\n- name: web\n  image: nginx:1.27\n"},
		{"no newline at the end", "apps:\n  - name: api\n    image: a:1", "apps:\n  - name: api\n    image: a:1\n  - name: web\n    image: nginx:1.27\n"},
		{"a comment after the list stays above the entry", "apps:\n  - name: api\n    image: a:1\n  # more to come\n", "apps:\n  - name: api\n    image: a:1\n  # more to come\n  - name: web\n    image: nginx:1.27\n"},
		{"an empty list", "# nothing yet\napps:\n", "# nothing yet\napps:\n  - name: web\n    image: nginx:1.27\n"},
		{"a block scalar at the end", "apps:\n  - name: api\n    image: a:1\n    env:\n      KEY: |\n        line one\n        line two\n", "apps:\n  - name: api\n    image: a:1\n    env:\n      KEY: |\n        line one\n        line two\n  - name: web\n    image: nginx:1.27\n"},
		{"apps is not the last key", "apps:\n  - name: api\n    image: a:1\nother: 1\n", ""},
		{"a flow list", "apps: [{name: api, image: a:1}]\n", ""},
		{"a flow entry", "apps:\n  - {name: api, image: a:1}\n", ""},
		{"the first key under its dash", "apps:\n  -\n    name: api\n    image: a:1\n", ""},
		{"two documents", "apps:\n  - name: api\n    image: a:1\n---\napps: []\n", ""},
		{"not a mapping", "- name: api\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := appendEntry([]byte(tt.file), "web", entry)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("expected a refusal, got:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("appendEntry: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestInitSaysWhenThereIsNoLockFile(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeTree(t, map[string]string{"package.json": `{"dependencies":{"express":"^5"},"scripts":{"start":"node server.js"}}`})
	_, errOut, err := f.run(dir, "init", "--name", "api")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if errOut != "! no lock file: the Dockerfile installs with npm install, and the build is not reproducible until package-lock.json is committed\n" {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(readOrEmpty(t, dir, "Dockerfile"), "RUN npm install --omit=dev\n") {
		t.Error("without a lock file the Dockerfile installs with npm install")
	}

	// With a lock file there is nothing to say; nor when the Dockerfile is the developer's.
	for _, extra := range []string{"package-lock.json", "Dockerfile"} {
		dir := writeTree(t, map[string]string{"package.json": `{"dependencies":{"express":"^5"}}`, extra: "{}"})
		if _, errOut, err := f.run(dir, "init", "--name", "api"); err != nil || errOut != "" {
			t.Errorf("with %s: err = %v, stderr = %q", extra, err, errOut)
		}
	}
}
