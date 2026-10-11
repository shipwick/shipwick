package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// everyKey is an application that sets every field a configuration has: the
// table of keys an export may require. A field added to spec.App fails
// TestEveryKeyOfTheConfigurationIsOneTheReaderKnows until it is set here.
func everyKey() spec.App {
	return spec.App{
		Name:      "shop",
		Image:     spec.LocalImagePrefix("shop") + "20260301-ab12",
		Port:      8080,
		Domain:    "shop.example.com",
		Aliases:   []string{"www.shop.example.com"},
		Redirects: []string{"shop.example.org"},
		Replicas:  1,
		Env:       map[string]string{"DATABASE_URL": "postgres://db/shop"},
		Health: &spec.Health{Path: "/health", TCP: 8080, Command: []string{"true"}, Interval: spec.Duration(5 * time.Second),
			Timeout: spec.Duration(time.Second), Retries: 3, StartPeriod: spec.Duration(time.Minute)},
		Resources:  spec.Resources{CPU: 0.5, MemoryBytes: 256 << 20},
		Volumes:    []spec.Volume{{Name: "data", Path: "/var/lib/shop"}},
		Publish:    []spec.Publish{{Port: 5432, Host: 15432, Address: "10.0.0.5", Protocol: spec.ProtocolTCP}},
		Entrypoint: []string{"/usr/bin/tini", "--"},
		Command:    []string{"shop", "--listen", "0.0.0.0:8080"},
		User:       "1000:1000",
		PreDeploy:  &spec.Hook{Command: []string{"shop", "migrate"}, Timeout: spec.Duration(10 * time.Minute)},
		Jobs:       []spec.Job{{Name: "sweep", Schedule: "*/15 * * * *", Command: []string{"shop", "sweep"}, Timeout: spec.Duration(time.Hour)}},
		Logging:    &spec.Logging{Driver: "gelf", Options: map[string]string{"tag": "shop"}},
		Build:      &spec.Build{Context: "services/shop", Dockerfile: "Dockerfile"},
		Static:     &spec.Static{Dir: "dist", Fallback: "index.html"},
		Path:       "/store",
		Proxy: &spec.Proxy{
			StripPrefix: true,
			Headers:     map[string]string{"X-Frame-Options": "DENY"},
			BasicAuth:   []spec.BasicAuth{{Path: "/store/admin", Username: "admin", Password: "correct horse"}},
			Redirects:   []spec.PathRedirect{{From: "/store/old", To: "/store/new", Status: 301}},
		},
		Backups:  &spec.Backups{Schedule: "30 2 * * *", Keep: 14, Before: []string{"shop", "checkpoint"}, BeforeTimeout: spec.Duration(time.Hour), Stop: true, BeforeIn: spec.BeforeInContainer},
		Restart:  spec.Restart{Policy: spec.RestartOnFailure},
		Deploy:   spec.Deploy{Strategy: spec.StrategyRecreate, StopTimeout: spec.Duration(45 * time.Second)},
		Init:     true,
		Security: &spec.Security{ReadOnly: true, Tmpfs: []spec.Tmpfs{{Path: "/tmp", SizeBytes: spec.DefaultTmpfsBytes}}, Capabilities: &[]string{"CHOWN"}, NonRoot: true},
	}
}

func TestEveryKeyOfTheConfigurationIsOneTheReaderKnows(t *testing.T) {
	present := map[string]bool{}
	if err := keyPaths(everyKey(), reflect.TypeOf(spec.App{}), "spec", present); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	knownKeyPaths(reflect.TypeOf(exportApp{}), "", known, nil)
	for path := range present {
		if !known[path] {
			t.Errorf("an export would require %s, which the reader does not know", path)
		}
	}
	for path := range known {
		if strings.HasPrefix(path, "spec.") && !present[path] {
			t.Errorf("everyKey does not set %s: add it there, so that the key is covered", path)
		}
	}
	// Besides the configuration, the entry's own members.
	for _, path := range []string{"name", "version", "stopped", "image.included", "image.reason", "static", "volumes", "references.env", "references.plain"} {
		if !known[path] {
			t.Errorf("the reader does not know %s", path)
		}
	}
	// A map's keys are values, not keys of the format.
	for _, path := range []string{"spec.env.DATABASE_URL", "spec.proxy.headers.X-Frame-Options", "spec.logging.options.tag"} {
		if present[path] || known[path] {
			t.Errorf("%s is listed as a key", path)
		}
	}
	if got := unknownKeys([]string{"spec.name", "spec.hoverboard", "references.hovercraft"}); !reflect.DeepEqual(got, []string{"spec.hoverboard", "references.hovercraft"}) {
		t.Errorf("unknownKeys = %v", got)
	}
}

func manifestOf(t *testing.T, archive []byte) exportManifest {
	t.Helper()
	var m exportManifest
	if err := json.Unmarshal([]byte(untar(t, bytes.NewReader(archive))[exportManifestName]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestTheKeysAnExportRequiresAreThoseItsApplicationsUse(t *testing.T) {
	h := seeded(t)
	m := manifestOf(t, h.export())
	for _, want := range []string{"spec.name", "spec.aliases", "spec.volumes.path", "spec.resources.memory_bytes", "spec.deploy.strategy"} {
		if !has(m.Requires, want) {
			t.Errorf("requires lacks %s: %v", want, m.Requires)
		}
	}
	for _, absent := range []string{"spec.security", "spec.health", "spec.env.SECRET"} {
		if has(m.Requires, absent) {
			t.Errorf("requires has %s, which no application uses: %v", absent, m.Requires)
		}
	}

	locked := app("vault", "ghcr.io/acme/vault:1.0", 1)
	locked.Security = &spec.Security{ReadOnly: true, Capabilities: &[]string{}}
	h.deploy(locked)
	m = manifestOf(t, h.export("vault"))
	for _, want := range []string{"spec.security", "spec.security.read_only", "spec.security.capabilities"} {
		if !has(m.Requires, want) {
			t.Errorf("requires lacks %s: %v", want, m.Requires)
		}
	}
	if has(m.Requires, "spec.security.non_root") {
		t.Errorf("requires has a key that is not set: %v", m.Requires)
	}
}

// archiveOf is a tar of the members in the order given: an export is read
// from its first byte, and export.json must come first.
func archiveOf(t *testing.T, members ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, m := range members {
		if err := w.WriteHeader(&tar.Header{Name: m[0], Mode: 0o600, Size: int64(len(m[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, m[1])
	}
	w.Close()
	return buf.Bytes()
}

// handMade is an export of one application with the manifest as given.
func handMade(t *testing.T, manifest string) []byte {
	t.Helper()
	entry, err := json.Marshal(exportApp{Name: "web", Version: "1.0", Spec: app("web", "ghcr.io/acme/web:1.0", 1), Volumes: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return archiveOf(t, [2]string{exportManifestName, manifest}, [2]string{exportAppBase("web") + "app.json", string(entry)})
}

// export.json as 0.8 wrote it: no `requires`.
const manifestBefore09 = `{"format":1,"created_at":"2026-10-01T04:00:00Z","shipwick":"v0.8.1","secrets":[],"registries":[],"certificates":[],"applications":["web"]}`

func TestAnExportWrittenBeforeTheListOfKeysImports(t *testing.T) {
	b := newServer(t, otherKey)
	result := b.importExport(handMade(t, manifestBefore09), ImportOptions{})
	if result.Status != api.ImportSucceeded {
		t.Fatalf("import = %+v", result)
	}
	if web := imported(t, result, "web"); web.Status != api.ImportAppImported {
		t.Errorf("web: %+v", web)
	}
	if result.ExportedAt == nil || result.ExportedAt.Year() != 2026 {
		t.Errorf("exported_at = %v", result.ExportedAt)
	}
}

func TestAnExportThatUsesAKeyThisAgentLacksIsRefusedInWords(t *testing.T) {
	b := newServer(t, otherKey)
	newer := strings.Replace(manifestBefore09, `"shipwick":"v0.8.1"`, `"shipwick":"v0.12.0","requires":["spec.name","spec.hoverboard"]`, 1)
	_, err := b.engine.Import(context.Background(), bytes.NewReader(handMade(t, newer)), ImportOptions{})
	var invalid *InvalidExportError
	if !errors.As(err, &invalid) {
		t.Fatalf("Import: %v", err)
	}
	for _, want := range []string{"written by Shipwick v0.12.0", "the key spec.hoverboard", "this agent (Shipwick " + version.Version + ")", "Import it with an agent of that version or newer"} {
		if !strings.Contains(invalid.Reason, want) {
			t.Errorf("the refusal lacks %q: %s", want, invalid.Reason)
		}
	}
	if strings.Contains(invalid.Reason, "spec.name") {
		t.Errorf("the refusal names a key this agent knows: %s", invalid.Reason)
	}
	status, ok := b.engine.ImportStatus()
	if !ok || status.Status != api.ImportFailed || len(status.Applications) != 0 {
		t.Errorf("nothing of the export may have been taken in: %+v, %v", status, ok)
	}

	known := strings.Replace(manifestBefore09, `"shipwick":"v0.8.1"`, `"shipwick":"v0.12.0","requires":["spec.name","spec.image"]`, 1)
	if result := b.importExport(handMade(t, known), ImportOptions{}); result.Status != api.ImportSucceeded {
		t.Errorf("keys this agent knows were refused: %+v", result)
	}
}

func TestAnApplicationRedeployedUnderAnExportFailsItAndSaysSo(t *testing.T) {
	h := newHarness(t)
	d := h.deploy(app("web", "ghcr.io/acme/web:1.0", 1))
	_, err := h.engine.exportApplication(context.Background(), newExportWriter(io.Discard, time.Now()), "web", d.ID+1)
	if err == nil || !strings.Contains(err.Error(), "replaced while the export was being written") {
		t.Fatalf("an application whose deployment is not the one listed: %v", err)
	}
	if _, err := h.engine.exportApplication(context.Background(), newExportWriter(io.Discard, time.Now()), "web", d.ID); err != nil {
		t.Fatalf("the deployment listed: %v", err)
	}
}
