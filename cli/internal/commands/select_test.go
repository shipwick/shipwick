package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

// chainConfig is manyConfig with one more link: web waits for api, which
// waits for postgres.
const chainConfig = manyConfig + "    after: [api, postgres]\n"

func deployedNames(t *testing.T, f *fakeAgent) []string {
	t.Helper()
	var names []string
	for _, body := range f.deployBodies {
		app, err := spec.Parse([]byte(body))
		if err != nil {
			t.Fatalf("the agent received a document that does not parse: %v\n%s", err, body)
		}
		names = append(names, app.Name)
	}
	return names
}

func TestDeployOneApplicationOfAShipwickFile(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}

	out, _, err := f.run(writeMany(t, manyConfig), "deploy", "api")
	if err != nil {
		t.Fatalf("deploy api: %v\n%s", err, out)
	}
	// It reads as one application does, and says what it did not wait for.
	assertInOrder(t, out, []string{
		"Deploying api...",
		"✓ Validated shipwick.yaml (1 variable substituted)",
		"Not deployed now and assumed to be running: postgres\n",
		"✓ Deployment successful",
	})
	if strings.Contains(out, "applications deployed") {
		t.Errorf("one application has no tally:\n%s", out)
	}
	if got := deployedNames(t, f); !reflect.DeepEqual(got, []string{"api"}) {
		t.Errorf("deployed %v, want api and nothing else", got)
	}
	if strings.Contains(f.deployBodies[0], "after") {
		t.Errorf("the agent must receive a deploy.yaml:\n%s", f.deployBodies[0])
	}
}

func TestDeploySeveralNamedApplicationsKeepsTheirOrder(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}

	// Named against the order of the file; api twice.
	out, _, err := f.run(writeMany(t, chainConfig), "deploy", "web", "api", "api")
	if err != nil {
		t.Fatalf("deploy web api: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Deploying 2 applications...",
		"✓ Validated shipwick.yaml",
		"Not deployed now and assumed to be running: postgres\n",
		"2 of 2 applications deployed.",
	})
	if strings.Count(out, "assumed to be running") != 1 {
		t.Errorf("what is assumed is said once:\n%s", out)
	}
	got := deployedNames(t, f)
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("deployed %v, want api and web", got)
	}
	f.many.mu.Lock()
	defer f.many.mu.Unlock()
	if at := f.many.started["web"]; !slices.Contains(at, "api") {
		t.Errorf("web must start only after api finished, but at its start %v had finished", at)
	}
	if at := f.many.started["api"]; len(at) != 0 {
		t.Errorf("api waits for nothing that is deployed now, but started after %v", at)
	}
}

func TestDeployEveryApplicationByNameAssumesNothing(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}

	out, _, err := f.run(writeMany(t, manyConfig), "deploy", "web", "postgres", "api")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if strings.Contains(out, "assumed") || !strings.Contains(out, "3 of 3 applications deployed.") {
		t.Errorf("unexpected output:\n%s", out)
	}
	f.many.mu.Lock()
	defer f.many.mu.Unlock()
	if at := f.many.started["api"]; !slices.Contains(at, "postgres") {
		t.Errorf("api must start only after postgres finished, but at its start %v had finished", at)
	}
}

func TestDeployOneNamedApplicationWithAnotherImage(t *testing.T) {
	f := newFakeAgent(t)
	f.many.outcomes = allActive("postgres", "api", "web")
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}
	dir := writeMany(t, manyConfig)

	out, _, err := f.run(dir, "deploy", "api", "--image", "ghcr.io/company/api:abc123")
	if err != nil {
		t.Fatalf("deploy api --image: %v\n%s", err, out)
	}
	app, err := spec.Parse([]byte(f.deployBodies[0]))
	if err != nil || len(f.deployBodies) != 1 || app.Name != "api" || app.Image != "ghcr.io/company/api:abc123" || app.Domain != "api.example.com" {
		t.Errorf("deployed %+v (%v), want api with the image given and the rest of its entry", app, err)
	}

	before := len(f.deployBodies)
	for args, want := range map[string]string{
		"web api": "--image applies to one application, and 2 are named",
		"":        "--image applies to one application, and shipwick.yaml describes several\n\nName the one it is for: shipwick deploy <name> --image x:1",
	} {
		argv := append([]string{"deploy", "--image", "x:1"}, strings.Fields(args)...)
		_, _, err := f.run(dir, argv...)
		if got := Render(err); !strings.Contains(got, want) {
			t.Errorf("deploy %s: unexpected rendering: %s", args, got)
		}
	}
	// An image that cannot be one is refused before anything is sent.
	if _, _, err := f.run(dir, "deploy", "api", "--image", "not an image"); err == nil {
		t.Error("an invalid image was accepted")
	}
	built := strings.Replace(manyConfig, "    image: ghcr.io/company/api:2.3.0\n", "    build: .\n", 1)
	_, _, err = f.run(writeMany(t, built), "deploy", "api", "--image", "x:1")
	if got := Render(err); !strings.Contains(got, "api has build: and is built here, so --image does not apply") {
		t.Errorf("unexpected rendering: %s", got)
	}
	if len(f.deployBodies) != before {
		t.Errorf("a refused command deployed something: %v", f.deployBodies[before:])
	}
}

func TestAnUnknownNameListsTheApplicationsOfTheFile(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"POSTGRES_PASSWORD": "s3cret"}
	dir := writeMany(t, manyConfig)

	for _, command := range []string{"deploy", "validate"} {
		_, _, err := f.run(dir, command, "api", "worker", "cron")
		if got, want := Render(err), "shipwick.yaml has no application named worker, cron\n\nIt describes: postgres, api, web"; !strings.Contains(got, want) {
			t.Errorf("%s: unexpected rendering:\n%s", command, got)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("an unknown name must not reach the agent, saw %v", f.requests)
	}
}

func TestANameIsRefusedWhereOneApplicationIsDescribed(t *testing.T) {
	f := newFakeAgent(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.yaml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"deploy", "validate"} {
		_, _, err := f.run(dir, command, "my-api")
		if got, want := Render(err), "deploy.yaml describes one application and takes no name: a name chooses among the applications of a shipwick.yaml\n\nLeave the name out: shipwick "+command; !strings.Contains(got, want) {
			t.Errorf("%s my-api: unexpected rendering:\n%s", command, got)
		}
		_, _, err = f.run(dir, command, "-f", "other.yaml", "my-api")
		if got, want := Render(err), "other.yaml describes one application and takes no name"; !strings.Contains(got, want) || !strings.Contains(got, "shipwick "+command+" -f other.yaml") {
			t.Errorf("%s -f other.yaml my-api: unexpected rendering:\n%s", command, got)
		}
		// Where there is no file at all, deploy must not start writing one.
		_, _, err = f.run(t.TempDir(), command, "my-api")
		if got, want := Render(err), "a name chooses among the applications of a shipwick.yaml, and there is none here"; !strings.Contains(got, want) {
			t.Errorf("%s my-api in an empty directory: unexpected rendering:\n%s", command, got)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("a refused name must not reach the agent, saw %v", f.requests)
	}
}

func TestValidateNamedApplicationsShowsWhatDeployWouldDo(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"POSTGRES_PASSWORD": "x"}
	out, _, err := f.run(writeMany(t, chainConfig), "validate", "web", "api")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ shipwick.yaml is valid (1 variable substituted)",
		"api\n",
		"web  after api\n",
		"shipwick deploy web api leaves out postgres and assumes it running.",
		"Name", "api", "api.example.com",
		"Name", "web", "example.com",
	})
	if strings.Contains(out, "recreate") {
		t.Errorf("postgres was not asked for:\n%s", out)
	}

	// The whole file is still checked.
	broken := strings.Replace(manyConfig, "port: 5432", "port: 0", 1)
	_, _, err = f.run(writeMany(t, broken), "validate", "web")
	if got := Render(err); !strings.Contains(got, "apps[0].port") {
		t.Errorf("a mistake in an application that was not named:\n%s", got)
	}
}

func TestSelectEntries(t *testing.T) {
	entries, err := spec.ParseMany([]byte(strings.ReplaceAll(chainConfig, "${POSTGRES_PASSWORD}", "x")))
	if err != nil {
		t.Fatal(err)
	}
	all, err := selectEntries("shipwick.yaml", entries, nil)
	if err != nil || len(all.entries) != 3 || !reflect.DeepEqual(all.index, []int{0, 1, 2}) || len(all.assumed) != 0 {
		t.Errorf("no names: %+v, %v", all, err)
	}
	s, err := selectEntries("shipwick.yaml", entries, []string{"web"})
	if err != nil || len(s.entries) != 1 || !reflect.DeepEqual(s.index, []int{2}) || len(s.entries[0].After) != 0 {
		t.Fatalf("web: %+v, %v", s, err)
	}
	// In the order of the file, whatever `after` listed first.
	if !reflect.DeepEqual(s.assumed, []string{"postgres", "api"}) {
		t.Errorf("assumed = %v", s.assumed)
	}
	if !reflect.DeepEqual(entries[2].After, []string{"api", "postgres"}) {
		t.Errorf("the file's own entry was edited: after = %v", entries[2].After)
	}
}
