package commands

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
)

const movePassphrase = "a passphrase for the move"

// exportAgent answers the export, import and standby endpoints in front of
// the fake agent.
type exportAgent struct {
	*fakeAgent
	mu sync.Mutex
	// cut makes the export end before its last chunk, as a connection that
	// dies does.
	cut bool
	// imported is what POST /import received, decrypted; importQuery and
	// importPassphrase how it was asked.
	imported         []byte
	importQuery      url.Values
	importPassphrase string
	result           api.Import
	standby          api.Standby
	promotion        api.Promotion
	promoted         int
}

// exportArchive is a small export as the agent writes one, in clear.
func exportArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	manifest := `{"format":1,"created_at":"2026-03-01T04:00:00Z","shipwick":"1.2.3","secrets":[{"name":"DB_PASSWORD","value":"s3cret-value"}],"registries":[],"certificates":[],"applications":["db","web"]}`
	for name, content := range map[string]string{"export.json": manifest} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.WriteHeader(&tar.Header{Name: "applications/db/app.json", Mode: 0o600, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("{}"))
	tw.Close()
	return buf.Bytes()
}

func newExportAgent(t *testing.T) *exportAgent {
	t.Helper()
	e := &exportAgent{fakeAgent: newFakeAgent(t)}
	upstream, _ := url.Parse(e.srv.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/export", func(w http.ResponseWriter, r *http.Request) {
		var req api.ExportRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/octet-stream")
		sealed, _ := backupfile.NewWriter(w, req.Passphrase)
		sealed.Write(exportArchive(t))
		if !e.cut {
			sealed.Close()
		}
	})
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		passphrase, _ := base64.StdEncoding.DecodeString(r.Header.Get(api.PassphraseHeader))
		plain, err := backupfile.NewReader(r.Body, string(passphrase))
		if err != nil {
			respondError(w, 400, api.Error{Code: api.CodeInvalidExport, Message: err.Error()})
			return
		}
		body, _ := io.ReadAll(plain)
		e.mu.Lock()
		e.imported, e.importQuery, e.importPassphrase = body, r.URL.Query(), string(passphrase)
		e.mu.Unlock()
		respond(w, 200, e.result)
	})
	mux.HandleFunc("GET /api/v1/import", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, e.result) })
	mux.HandleFunc("GET /api/v1/standby", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, e.standby) })
	mux.HandleFunc("POST /api/v1/standby/promote", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.promoted++
		e.mu.Unlock()
		respond(w, 200, e.promotion)
	})
	mux.Handle("/", proxy)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.srv = srv
	e.env = map[string]string{envExportPassphrase: movePassphrase}
	return e
}

func TestExportWritesAFileThatWasReadBackWhole(t *testing.T) {
	e := newExportAgent(t)
	dir := t.TempDir()
	out, _, err := e.run(dir, "export", "-o", "move.swexport")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 applications (db, web), 1 secret") || !strings.Contains(out, "shipwick import move.swexport") {
		t.Errorf("output:\n%s", out)
	}
	file, err := os.ReadFile(filepath.Join(dir, "move.swexport"))
	if err != nil || !backupfile.IsEncrypted(file) || bytes.Contains(file, []byte("s3cret-value")) {
		t.Fatalf("the file is missing or not encrypted: %v", err)
	}
	// Never over a file that is there.
	if _, _, err := e.run(dir, "export", "-o", "move.swexport"); err == nil {
		t.Error("an existing file was overwritten")
	}
}

func TestAnExportThatWasCutIsNotKept(t *testing.T) {
	e := newExportAgent(t)
	e.cut = true
	dir := t.TempDir()
	_, _, err := e.run(dir, "export", "-o", "move.swexport")
	if err == nil || !strings.Contains(Render(err), "not a whole export") {
		t.Fatalf("a cut export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "move.swexport")); !os.IsNotExist(err) {
		t.Error("part of an export was kept")
	}
}

func TestExportNeedsAPassphraseOfSomeLength(t *testing.T) {
	e := newExportAgent(t)
	e.env = map[string]string{}
	if _, _, err := e.run(t.TempDir(), "export"); err == nil || !strings.Contains(err.Error(), envExportPassphrase) {
		t.Fatalf("no passphrase and no terminal: %v", err)
	}
	e.env = map[string]string{envExportPassphrase: "short"}
	if _, _, err := e.run(t.TempDir(), "export"); err == nil || !strings.Contains(err.Error(), "at least 12") {
		t.Fatalf("a short passphrase: %v", err)
	}
}

func TestImportSendsTheFileAndReportsEveryApplication(t *testing.T) {
	e := newExportAgent(t)
	dir := t.TempDir()
	if _, _, err := e.run(dir, "export", "-o", "move.swexport"); err != nil {
		t.Fatal(err)
	}
	id := int64(7)
	e.result = api.Import{Status: api.ImportFailed, Source: "upload", Secrets: 1, Warnings: []string{"GREETING: a secret by that name exists on this server and was kept; --overwrite replaces it"},
		Applications: []api.ImportedApplication{
			{Name: "db", Status: api.ImportAppImported, Version: "17", DeploymentID: &id, Volumes: []string{"data"}},
			{Name: "web", Status: api.ImportAppSkipped, Message: "it exists on this server and was left as it is; import with --overwrite to replace it and its volumes"},
			{Name: "api", Status: api.ImportAppFailed, Message: "its deployment #9 ended FAILED: replica 1 exited with code 1 shortly after start"},
		}}
	out, errOut, err := e.run(dir, "import", "move.swexport")
	if err != ErrReported {
		t.Fatalf("an import with a failed application: %v", err)
	}
	all := out + errOut
	for _, want := range []string{"Export of ", "2 applications (db, web)", "db 17 (1 volume restored: data)", "web was not imported", "--overwrite",
		"api: its deployment #9 ended FAILED", "1 secret stored", "GREETING", "Not everything was imported"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
	if e.importPassphrase != movePassphrase || !bytes.Equal(e.imported, exportArchive(t)) {
		t.Error("the agent did not receive the file with its passphrase")
	}
	if e.importQuery.Get("stopped") != "false" || e.importQuery.Get("overwrite") != "false" {
		t.Errorf("query = %v", e.importQuery)
	}

	// --overwrite asks, and does not ask a pipe.
	if _, _, err := e.run(dir, "import", "move.swexport", "--overwrite"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("--overwrite without --yes and without a terminal: %v", err)
	}
	e.result = api.Import{Status: api.ImportSucceeded, Stopped: true, Applications: []api.ImportedApplication{{Name: "db", Status: api.ImportAppImported, Version: "17"}}}
	out, _, err = e.run(dir, "import", "move.swexport", "--overwrite", "--stopped", "--yes")
	if err != nil || !strings.Contains(out, "db 17, stopped") || !strings.Contains(out, "shipwick standby promote") {
		t.Fatalf("a stopped import: %v\n%s", err, out)
	}
	if e.importQuery.Get("stopped") != "true" || e.importQuery.Get("overwrite") != "true" {
		t.Errorf("query = %v", e.importQuery)
	}
}

func TestImportFindsAWrongPassphraseBeforeItSendsAnything(t *testing.T) {
	e := newExportAgent(t)
	dir := t.TempDir()
	if _, _, err := e.run(dir, "export", "-o", "move.swexport"); err != nil {
		t.Fatal(err)
	}
	e.env = map[string]string{envExportPassphrase: "another passphrase"}
	_, _, err := e.run(dir, "import", "move.swexport")
	if err == nil || !strings.Contains(err.Error(), "passphrase does not match") || e.imported != nil {
		t.Fatalf("a wrong passphrase: %v, sent: %v", err, e.imported != nil)
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not an export"), 0o600)
	if _, _, err := e.run(dir, "import", "notes.txt"); err == nil || !strings.Contains(err.Error(), "not a file shipwick export wrote") {
		t.Fatalf("a file that is not an export: %v", err)
	}
}

func TestStandbyPromoteStartsAndPrintsTheRecords(t *testing.T) {
	e := newExportAgent(t)
	out, _, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	if err != nil || !strings.Contains(out, "No application is waiting") || e.promoted != 0 {
		t.Fatalf("nothing to promote: %v\n%s", err, out)
	}

	e.standby = api.Standby{Applications: []api.StandbyApplication{{Name: "db", Version: "17", Hostnames: []string{}, ImportedAt: fixedNow}, {Name: "web", Version: "1.4.2", Hostnames: []string{"web.example.com"}, ImportedAt: fixedNow}}}
	out, _, err = e.run(t.TempDir(), "standby")
	if err != nil || !strings.Contains(out, "web.example.com") || !strings.Contains(out, "shipwick standby promote") {
		t.Fatalf("standby: %v\n%s", err, out)
	}
	if _, _, err := e.run(t.TempDir(), "standby", "promote"); err == nil || !strings.Contains(err.Error(), "--yes") || e.promoted != 0 {
		t.Fatalf("a promotion without confirmation: %v", err)
	}

	e.promotion = api.Promotion{
		Applications: []api.PromotedApplication{{Name: "db", Status: api.PromotedRunning}, {Name: "web", Status: api.PromotedStarted, Message: "started, and not ready yet: connection refused. It is restarted until it is; watch it with: shipwick status web"}},
		Records:      []api.DNSRecord{{Hostname: "web.example.com", Type: "A", Value: "203.0.113.77"}},
	}
	out, errOut, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	all := out + errOut
	if err != nil || e.promoted != 1 {
		t.Fatalf("promote: %v\n%s", err, all)
	}
	for _, want := range []string{"db is running", "web: started, and not ready yet", "Change these DNS records", "web.example.com", "203.0.113.77"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
}

func TestExportErrorsSayWhatToDo(t *testing.T) {
	for code, want := range map[string]string{
		api.CodeImportInProgress:     "shipwick import --status",
		api.CodeExportInProgress:     "shipwick export --list",
		api.CodeStandbyNotConfigured: "SHIPWICK_STANDBY_SCHEDULE",
		api.CodeInvalidExport:        "shipwick export",
	} {
		e := newExportAgent(t)
		e.deployStatus, e.deployError = 409, api.Error{Code: code, Message: "the agent's sentence"}
		_, _, err := e.run(writeConfig(t, "name: my-api\nimage: nginx:1.27\n"), "deploy")
		if got := Render(err); !strings.Contains(got, want) {
			t.Errorf("%s renders as %q, want it to name %q", code, got, want)
		}
	}
}
