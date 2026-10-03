package api

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
)

const exportPassphrase = "a passphrase for the move"

// exportOf asks the fixture for an export and returns the response.
func (f *fixture) exportOf(auth, body string) (*http.Response, []byte) {
	f.t.Helper()
	req, err := http.NewRequest("POST", f.srv.URL+"/api/v1/export", strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp, data
}

// importInto uploads an export and returns the status and raw body.
func (f *fixture) importInto(query string, archive []byte, passphrase string) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest("POST", f.srv.URL+"/api/v1/import"+query, bytes.NewReader(archive))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(api.PassphraseHeader, base64.StdEncoding.EncodeToString([]byte(passphrase)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestExportIsEncryptedAndImportsOnAnotherServer(t *testing.T) {
	a := newFixture(t)
	a.deployStateful(map[string]string{"PG_VERSION": "17"})
	if err := a.store.SetSecret(t.Context(), "DB_PASSWORD", "s3cret-value", a.api.now()); err != nil {
		t.Fatal(err)
	}

	resp, archive := a.exportOf("Bearer "+testToken, `{"passphrase":"`+exportPassphrase+`"}`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), ".swexport") {
		t.Fatalf("export: %d %v", resp.StatusCode, resp.Header)
	}
	if !backupfile.IsEncrypted(archive) || bytes.Contains(archive, []byte("s3cret-value")) || bytes.Contains(archive, []byte("PG_VERSION")) {
		t.Fatal("the export is not encrypted")
	}
	if resp.Trailer.Get(exportErrorTrailer) != "" {
		t.Fatalf("trailer = %q", resp.Trailer.Get(exportErrorTrailer))
	}
	if logs := a.logs.String(); strings.Contains(logs, exportPassphrase) || strings.Contains(logs, "s3cret-value") {
		t.Error("the passphrase or a secret reached the log")
	}

	b := newFixture(t)
	if status, body := b.do("GET", "/api/v1/import", ""); status != http.StatusNotFound {
		t.Fatalf("import status before any import: %d %s", status, body)
	}
	status, body := b.importInto("", archive, "not the passphrase at all")
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidExport || !strings.Contains(e.Message, "passphrase") {
		t.Fatalf("wrong passphrase: %d %s", status, body)
	}
	status, body = b.importInto("", []byte("PK\x03\x04 not an export, whatever else it may be"), exportPassphrase)
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidExport {
		t.Fatalf("not an export: %d %s", status, body)
	}
	if len(b.rt.Containers()) != 0 {
		t.Fatal("a refused import created something")
	}

	status, body = b.importInto("?stopped=true", archive, exportPassphrase)
	if status != http.StatusOK {
		t.Fatalf("import: %d %s", status, body)
	}
	result := decode[api.Import](t, body)
	if result.Status != api.ImportSucceeded || !result.Stopped || result.Secrets != 1 || len(result.Applications) != 1 ||
		result.Applications[0].Status != api.ImportAppImported || len(result.Applications[0].Volumes) != 1 {
		t.Fatalf("import = %+v", result)
	}
	if strings.Contains(string(body), "s3cret-value") || strings.Contains(b.logs.String(), exportPassphrase) {
		t.Error("the answer or the log of an import carries a secret")
	}
	c := b.rt.Containers()[0]
	if c.Running || string(b.rt.Files(c.ID)["/var/lib/data/PG_VERSION"]) != "17" {
		t.Errorf("the imported database runs (%v), or lacks its data", c.Running)
	}

	_, body = b.do("GET", "/api/v1/import", "")
	if got := decode[api.Import](t, body); got.CompletedAt == nil || got.Source != "upload" {
		t.Errorf("import status = %+v", got)
	}
	_, body = b.do("GET", "/api/v1/standby", "")
	standby := decode[api.Standby](t, body)
	if len(standby.Applications) != 1 || standby.Applications[0].Name != "db" || standby.Pull != nil || standby.Records == nil {
		t.Fatalf("standby = %s", body)
	}

	status, body = b.do("POST", "/api/v1/standby/promote", "")
	promotion := decode[api.Promotion](t, body)
	if status != http.StatusOK || len(promotion.Applications) != 1 || promotion.Applications[0].Status != api.PromotedRunning ||
		promotion.Status != api.PromotionSucceeded || promotion.CompletedAt == nil {
		t.Fatalf("promote, as a client that cannot follow one asks: %d %s", status, body)
	}
	if !b.rt.Containers()[0].Running {
		t.Error("the promotion did not start the database")
	}
}

func TestExportRequestsAreValidated(t *testing.T) {
	f := newFixture(t)
	for name, body := range map[string]string{
		"no body":            ``,
		"a short passphrase": `{"passphrase":"shortpass"}`,
		"an unknown field":   `{"passphrase":"` + exportPassphrase + `","to":"/tmp/x"}`,
		"a bad name":         `{"passphrase":"` + exportPassphrase + `","applications":["../etc"]}`,
	} {
		resp, data := f.exportOf("Bearer "+testToken, body)
		if e := decodeError(t, data); resp.StatusCode != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
			t.Errorf("%s: %d %s", name, resp.StatusCode, data)
		}
		if strings.Contains(string(data), "shortpass") {
			t.Errorf("%s: the answer repeats the passphrase", name)
		}
	}
	resp, data := f.exportOf("Bearer "+testToken, `{"passphrase":"`+exportPassphrase+`","applications":["nope"]}`)
	if e := decodeError(t, data); resp.StatusCode != http.StatusNotFound || e.Code != api.CodeNotFound {
		t.Errorf("an unknown application: %d %s", resp.StatusCode, data)
	}
}

func TestExportImportAndPromotionTakeAdmin(t *testing.T) {
	f := newFixture(t)
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token
	reader := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	for _, call := range [][2]string{
		{"POST", "/api/v1/export"}, {"GET", "/api/v1/exports"}, {"POST", "/api/v1/exports"},
		{"POST", "/api/v1/import"}, {"GET", "/api/v1/import"},
		{"POST", "/api/v1/standby/pull"}, {"POST", "/api/v1/standby/promote"},
	} {
		if status, body := f.doWithAuth(call[0], call[1], "", deployer); status != http.StatusForbidden {
			t.Errorf("%s %s with a deploy token: %d %s", call[0], call[1], status, body)
		}
	}
	if status, body := f.doWithAuth("GET", "/api/v1/standby", "", reader); status != http.StatusOK {
		t.Errorf("GET /standby with a read token: %d %s", status, body)
	}
	if status, body := f.doWithAuth("POST", "/api/v1/standby/promote?wait=false", "", reader); status != http.StatusForbidden {
		t.Errorf("POST /standby/promote with a read token: %d %s", status, body)
	}
}

func TestExportsToTheBackupDestinationAndPullsSayWhatIsMissing(t *testing.T) {
	f := newFixture(t)
	status, body := f.do("POST", "/api/v1/exports", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeBackupNotUsable {
		t.Errorf("an export without a backup destination: %d %s", status, body)
	}
	status, body = f.do("POST", "/api/v1/standby/pull", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeStandbyNotConfigured {
		t.Errorf("a pull without a bucket: %d %s", status, body)
	}

	plain, _ := newBackupFixture(t, "")
	status, body = plain.do("POST", "/api/v1/exports", "")
	if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodeBackupsNotEncrypted {
		t.Errorf("an export without a passphrase: %d %s", status, body)
	}

	sealed, _ := newBackupFixture(t, backupPassphrase)
	sealed.deployBackedUp(map[string]string{"PG_VERSION": "17"})
	status, body = sealed.do("POST", "/api/v1/exports", "")
	if status != http.StatusAccepted {
		t.Fatalf("an export to the backup destination: %d %s", status, body)
	}
	sealed.engine.Wait()
	_, body = sealed.do("GET", "/api/v1/exports", "")
	runs := decode[[]api.BackupRun](t, body)
	if len(runs) != 1 || runs[0].Status != api.BackupSucceeded || !runs[0].Encrypted || runs[0].Volumes[0].Volume != "export.tar" {
		t.Fatalf("exports = %s", body)
	}
}

// checkedConfig is an application whose readiness the test decides: its
// health check is a command, and the fake runtime answers it as it is told.
const checkedConfig = `
name: db
image: postgres:17
health:
  command: ["pg_isready"]
  interval: 1s
  retries: 60
`

func TestAPromotionIsStartedAndFollowed(t *testing.T) {
	a := newFixture(t)
	if status, body := a.do("POST", "/api/v1/applications/db/deploy", checkedConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	a.engine.Wait()
	_, archive := a.exportOf("Bearer "+testToken, `{"passphrase":"`+exportPassphrase+`"}`)

	b := newFixture(t)
	if status, body := b.importInto("?stopped=true", archive, exportPassphrase); status != http.StatusOK {
		t.Fatalf("import: %d %s", status, body)
	}
	status, body := b.do("GET", "/api/v1/standby/promotion", "")
	if e := decodeError(t, body); status != http.StatusNotFound || e.Code != api.CodeNotFound {
		t.Fatalf("the promotion of a server that was never promoted: %d %s", status, body)
	}
	if status, body := b.do("POST", "/api/v1/standby/promote?wait=perhaps", ""); status != http.StatusBadRequest {
		t.Fatalf("wait=perhaps: %d %s", status, body)
	}

	// Not ready until the test says so: the promotion stays under way.
	replica := b.rt.Containers()[0].Name
	b.rt.SetExecResult(replica, dockertest.ExecResult{ExitCode: 1})

	req, _ := http.NewRequest("POST", b.srv.URL+"/api/v1/standby/promote?wait=false", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	begun := decode[api.Promotion](t, body)
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Location") != "/api/v1/standby/promotion" ||
		begun.ID != 1 || begun.Status != api.PromotionRunning || begun.CompletedAt != nil ||
		len(begun.Applications) != 1 || begun.Applications[0].Status != api.PromotedPending || begun.Records == nil {
		t.Fatalf("promote: %d %s", resp.StatusCode, body)
	}

	for _, query := range []string{"", "?wait=false"} {
		status, body = b.do("POST", "/api/v1/standby/promote"+query, "")
		if e := decodeError(t, body); status != http.StatusConflict || e.Code != api.CodePromotionInProgress {
			t.Errorf("a second promotion%s while one runs: %d %s", query, status, body)
		}
	}
	reader := "Bearer " + b.createToken("viewer", api.RoleRead).Token
	status, body = b.doWithAuth("GET", "/api/v1/standby/promotion", "", reader)
	if got := decode[api.Promotion](t, body); status != http.StatusOK || got.ID != 1 || got.CompletedAt != nil {
		t.Errorf("the promotion while it runs, with a read token: %d %s", status, body)
	}
	_, body = b.do("GET", "/api/v1/standby", "")
	if standby := decode[api.Standby](t, body); standby.Promotion == nil || standby.Promotion.Status != api.PromotionRunning {
		t.Errorf("standby while a promotion runs = %s", body)
	}

	b.rt.SetExecResult(replica, dockertest.ExecResult{})
	b.engine.Wait()
	_, body = b.do("GET", "/api/v1/standby/promotion", "")
	final := decode[api.Promotion](t, body)
	if final.Status != api.PromotionSucceeded || final.CompletedAt == nil || final.Applications[0].Status != api.PromotedRunning {
		t.Fatalf("the promotion when it has ended = %s", body)
	}

	// Nothing is left to start: answered at once, and the record stays.
	status, body = b.do("POST", "/api/v1/standby/promote?wait=false", "")
	if again := decode[api.Promotion](t, body); status != http.StatusOK || again.CompletedAt == nil || len(again.Applications) != 0 || again.ID != 0 {
		t.Errorf("a promotion with nothing to start: %d %s", status, body)
	}
	_, body = b.do("GET", "/api/v1/standby/promotion", "")
	if kept := decode[api.Promotion](t, body); kept.ID != 1 || len(kept.Applications) != 1 {
		t.Errorf("the last promotion = %s", body)
	}
}
