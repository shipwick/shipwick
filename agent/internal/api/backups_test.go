package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
)

func init() { backupfile.Iterations = 1000 }

const backupPassphrase = "correct horse battery staple"

// newBackupFixture is newFixture with somewhere to keep backups; it returns
// the directory too.
func newBackupFixture(t *testing.T, passphrase string) (*fixture, string) {
	t.Helper()
	key := []byte("an-encryption-key-of-32-bytes!!!")
	st, err := store.Open(context.Background(), ":memory:", store.Options{EncryptionKey: key})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	dir := t.TempDir()
	rt := dockertest.New()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := deploy.New(st, rt, deploy.Options{StabilizeWindow: 20 * time.Millisecond, NameSettle: time.Millisecond, Logger: quiet,
		Backups: deploy.BackupOptions{Storage: backup.New(dir, nil, passphrase), EncryptionKey: key}})

	logs := &bytes.Buffer{}
	apiServer := New(engine, st, sha256.Sum256([]byte(testToken)), slog.New(slog.NewTextHandler(logs, nil)))
	srv := httptest.NewServer(apiServer.Handler())
	t.Cleanup(func() {
		apiServer.Close()
		srv.Close()
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &fixture{t: t, srv: srv, engine: engine, rt: rt, api: apiServer, logs: logs, store: st}, dir
}

const backedUpConfig = statefulConfig + `
backups:
  schedule: "0 3 * * *"
  keep: 3
`

func (f *fixture) deployBackedUp(files map[string]string) string {
	f.t.Helper()
	if status, body := f.do("POST", "/api/v1/applications/db/deploy", backedUpConfig); status != http.StatusAccepted {
		f.t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()
	id := f.rt.Containers()[0].ID
	for name, content := range files {
		if err := f.rt.PutFile(id, "/var/lib/data/"+name, []byte(content)); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

// takeBackup starts a backup over the API and waits for it.
func (f *fixture) takeBackup() api.BackupRunDetail {
	f.t.Helper()
	status, body := f.do("POST", "/api/v1/applications/db/backups", "")
	if status != http.StatusAccepted {
		f.t.Fatalf("start backup: %d %s", status, body)
	}
	started := decode[api.BackupRun](f.t, body)
	f.engine.Wait()
	return f.getBackup(started.ID)
}

func (f *fixture) getBackup(id int64) api.BackupRunDetail {
	f.t.Helper()
	status, body := f.do("GET", fmt.Sprintf("/api/v1/applications/db/backups/%d", id), "")
	if status != http.StatusOK {
		f.t.Fatalf("get backup: %d %s", status, body)
	}
	return decode[api.BackupRunDetail](f.t, body)
}

func TestBackupRunIsAcceptedAndPolled(t *testing.T) {
	f, _ := newBackupFixture(t, "")
	f.deployBackedUp(map[string]string{"a.txt": "alpha"})

	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/applications/db/backups", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Location") != "/api/v1/applications/db/backups/1" {
		t.Fatalf("status = %d, Location = %q: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	started := decode[api.BackupRun](t, body)
	if started.ID != 1 || started.Trigger != api.BackupTriggerManual || started.Status != api.BackupRunning {
		t.Fatalf("unexpected run: %+v", started)
	}
	// The shape clients rely on: lists, never null.
	if !strings.Contains(string(body), `"volumes":[]`) || !strings.Contains(string(body), `"destinations":[]`) {
		t.Errorf("a running backup's lists are not empty lists: %s", body)
	}
	f.engine.Wait()

	run := f.getBackup(1)
	if run.Status != api.BackupSucceeded || run.CompletedAt == nil || len(run.Volumes) != 1 || run.Volumes[0].Volume != "data" ||
		run.Volumes[0].SizeBytes == 0 || strings.Join(run.Destinations, ",") != "local" || run.Encrypted {
		t.Fatalf("unexpected run: %+v", run)
	}

	status, body := f.do("GET", "/api/v1/applications/db/backups", "")
	if runs := decode[[]api.BackupRun](t, body); status != http.StatusOK || len(runs) != 1 || runs[0].ID != 1 {
		t.Fatalf("list: %d %s", status, body)
	}
	if strings.Contains(string(body), "verify_output") {
		t.Errorf("the listing carries the verification output: %s", body)
	}
}

func TestBackupEndpointsValidateAndMapErrors(t *testing.T) {
	f, _ := newBackupFixture(t, "")
	f.deployBackedUp(nil)
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", validConfig); status != http.StatusAccepted {
		t.Fatalf("deploy: %d %s", status, body)
	}
	f.engine.Wait()

	tests := map[string]struct {
		method, path string
		status       int
		code         string
	}{
		"unknown application":      {"GET", "/api/v1/applications/nope/backups", http.StatusNotFound, api.CodeNotFound},
		"invalid application":      {"GET", "/api/v1/applications/No_Pe/backups", http.StatusBadRequest, api.CodeInvalidRequest},
		"unknown backup":           {"GET", "/api/v1/applications/db/backups/9", http.StatusNotFound, api.CodeNotFound},
		"backup id not a number":   {"GET", "/api/v1/applications/db/backups/abc", http.StatusBadRequest, api.CodeInvalidRequest},
		"latest only for verify":   {"POST", "/api/v1/applications/db/backups/latest/restore", http.StatusBadRequest, api.CodeInvalidRequest},
		"verify with no backup":    {"POST", "/api/v1/applications/db/backups/latest/verify", http.StatusNotFound, api.CodeNotFound},
		"no volumes":               {"POST", "/api/v1/applications/my-api/backups", http.StatusConflict, api.CodeNoVolumes},
		"limit out of range":       {"GET", "/api/v1/applications/db/backups?limit=0", http.StatusBadRequest, api.CodeInvalidRequest},
		"archive of bad volume":    {"GET", "/api/v1/applications/db/backups/1/volumes/Da_ta/archive", http.StatusBadRequest, api.CodeInvalidRequest},
		"state without passphrase": {"POST", "/api/v1/server/backups", http.StatusConflict, api.CodeBackupsNotEncrypted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			status, body := f.do(tc.method, tc.path, "")
			if status != tc.status || decodeError(t, body).Code != tc.code {
				t.Errorf("status = %d, body = %s; want %d %s", status, body, tc.status, tc.code)
			}
		})
	}
}

func TestBackupArchiveIsDownloadedDecrypted(t *testing.T) {
	f, dir := newBackupFixture(t, backupPassphrase)
	f.deployBackedUp(map[string]string{"a.txt": "alpha"})
	run := f.takeBackup()
	if !run.Encrypted {
		t.Fatalf("unexpected run: %+v", run)
	}

	req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/applications/db/backups/1/volumes/data/archive", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-tar" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="db-data-backup-1.tar"` {
		t.Fatalf("status = %d, headers = %v", resp.StatusCode, resp.Header)
	}
	if resp.ContentLength != run.Volumes[0].SizeBytes {
		t.Errorf("Content-Length = %d, want the archive's %d", resp.ContentLength, run.Volumes[0].SizeBytes)
	}
	archive, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := tarOf(t, map[string]string{"a.txt": "alpha"}); !bytes.Equal(archive, want) {
		t.Errorf("the download is not the tar archive that was taken (%d bytes, want %d)", len(archive), len(want))
	}

	// A file that was cut short on the server must not arrive looking whole.
	path := filepath.Join(dir, "db", "1", "data.tar.enc")
	data, _ := os.ReadFile(path)
	os.WriteFile(path, data[:len(data)-5], 0o600)
	status, body := f.do("GET", "/api/v1/applications/db/backups/1/volumes/data/archive", "")
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeBackupNotUsable {
		t.Errorf("a damaged backup: %d %s", status, body)
	}

	status, body = f.do("GET", "/api/v1/applications/db/backups/1/volumes/logs/archive", "")
	if status != http.StatusNotFound {
		t.Errorf("a volume the backup does not hold: %d %s", status, body)
	}
}

func TestBackupVerifyRestoreAndDeleteOverTheAPI(t *testing.T) {
	f, dir := newBackupFixture(t, "")
	id := f.deployBackedUp(map[string]string{"a.txt": "as backed up"})
	f.takeBackup()
	f.rt.PutFile(id, "/var/lib/data/a.txt", []byte("damaged since"))

	status, body := f.do("POST", "/api/v1/applications/db/backups/latest/verify", "")
	if status != http.StatusAccepted || decode[api.BackupRun](t, body).Activity != api.BackupActivityVerify {
		t.Fatalf("verify: %d %s", status, body)
	}
	f.engine.Wait()
	run := f.getBackup(1)
	if run.VerifiedAt == nil || run.VerifyError != "" || run.Activity != "" || run.VerifyOutput == "" {
		t.Fatalf("unexpected run after the verification: %+v", run)
	}

	// The same rule and the same code as `shipwick restore`.
	status, body = f.do("POST", "/api/v1/applications/db/backups/1/restore", "")
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeApplicationRunning {
		t.Fatalf("restore into a running application: %d %s", status, body)
	}
	if status, body := f.do("POST", "/api/v1/applications/db/stop", ""); status != http.StatusOK {
		t.Fatalf("stop: %d %s", status, body)
	}
	status, body = f.do("POST", "/api/v1/applications/db/backups/1/restore", "")
	if status != http.StatusAccepted || decode[api.BackupRun](t, body).Activity != api.BackupActivityRestore {
		t.Fatalf("restore: %d %s", status, body)
	}
	f.engine.Wait()
	if run := f.getBackup(1); run.RestoredAt == nil || run.RestoreError != "" {
		t.Fatalf("unexpected run after the restore: %+v", run)
	}
	if got := f.rt.Files(f.rt.Containers()[0].ID)["/var/lib/data/a.txt"]; string(got) != "as backed up" {
		t.Errorf("the volume holds %q", got)
	}

	if status, body := f.do("DELETE", "/api/v1/applications/db/backups/1", ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, body)
	}
	if status, _ := f.do("GET", "/api/v1/applications/db/backups/1", ""); status != http.StatusNotFound {
		t.Errorf("the deleted backup is still there: %d", status)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "db")); len(entries) != 0 {
		t.Errorf("its files are still there: %v", entries)
	}
}

func TestBackupEndpointsRequireTheirRoles(t *testing.T) {
	f, _ := newBackupFixture(t, backupPassphrase)
	f.deployBackedUp(map[string]string{"a.txt": "alpha"})
	f.takeBackup()
	read := "Bearer " + f.createToken("reader", api.RoleRead).Token
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token

	forbidden := func(auth, method, path string) {
		t.Helper()
		if status, body := f.doWithAuth(method, path, "", auth); status != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: %s", method, path, status, body)
		}
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/applications/db/backups", "", read); status != http.StatusOK {
		t.Errorf("a read token cannot list backups: %d", status)
	}
	forbidden(read, "POST", "/api/v1/applications/db/backups")
	forbidden(read, "POST", "/api/v1/applications/db/backups/1/verify")
	if status, body := f.doWithAuth("POST", "/api/v1/applications/db/backups/1/verify", "", deployer); status != http.StatusAccepted {
		t.Errorf("a deploy token cannot verify: %d %s", status, body)
	}
	f.engine.Wait()
	forbidden(deployer, "POST", "/api/v1/applications/db/backups/1/restore")
	forbidden(deployer, "GET", "/api/v1/applications/db/backups/1/volumes/data/archive")
	forbidden(deployer, "DELETE", "/api/v1/applications/db/backups/1")
	forbidden(deployer, "GET", "/api/v1/server/backups")
	forbidden(deployer, "POST", "/api/v1/server/backups")
}

func TestServerReportsBackupsAndTakesOneOfItsState(t *testing.T) {
	f, dir := newBackupFixture(t, backupPassphrase)

	status, body := f.do("GET", "/api/v1/server", "")
	server := decode[api.Server](t, body)
	if status != http.StatusOK || server.Backups == nil || server.Backups.Destination != api.BackupDestinationLocal ||
		!server.Backups.Encrypted || server.Backups.StateLastAt != nil || server.Backups.StateError != "" {
		t.Fatalf("unexpected server: %d %+v", status, server.Backups)
	}

	status, body = f.do("POST", "/api/v1/server/backups", "")
	if status != http.StatusAccepted {
		t.Fatalf("start state backup: %d %s", status, body)
	}
	f.engine.Wait()
	status, body = f.do("GET", "/api/v1/server/backups/1", "")
	run := decode[api.BackupRun](t, body)
	if status != http.StatusOK || run.Status != api.BackupSucceeded || !run.Encrypted || len(run.Volumes) != 2 {
		t.Fatalf("unexpected run: %d %+v", status, run)
	}
	if status, body = f.do("GET", "/api/v1/server/backups", ""); status != http.StatusOK || len(decode[[]api.BackupRun](t, body)) != 1 {
		t.Fatalf("list: %d %s", status, body)
	}
	for _, name := range []string{"shipwick.db.enc", "encryption.key.enc"} {
		data, err := os.ReadFile(filepath.Join(dir, "_agent", "1", name))
		if err != nil || !backupfile.IsEncrypted(data) {
			t.Errorf("%s is not there, or not encrypted: %v", name, err)
		}
	}
	// The agent's state is not an application's backup.
	if status, _ := f.do("GET", "/api/v1/applications/_agent/backups", ""); status != http.StatusBadRequest {
		t.Errorf("the agent's state is reachable as an application: %d", status)
	}

	_, body = f.do("GET", "/api/v1/server", "")
	if b := decode[api.Server](t, body).Backups; b.StateLastAt == nil || b.StateError != "" {
		t.Fatalf("unexpected status after the backup: %+v", b)
	}
	if strings.Contains(f.logs.String(), backupPassphrase) {
		t.Error("the passphrase is in the log")
	}
}

func TestServerSaysWhyItsStateIsNotBackedUp(t *testing.T) {
	f, _ := newBackupFixture(t, "")
	_, body := f.do("GET", "/api/v1/server", "")
	b := decode[api.Server](t, body).Backups
	if b == nil || b.Encrypted || !strings.Contains(b.StateError, "SHIPWICK_BACKUP_PASSPHRASE is not set") {
		t.Fatalf("unexpected status: %+v", b)
	}
	// An agent without anywhere to keep backups says so too.
	plain := newFixture(t)
	_, body = plain.do("GET", "/api/v1/server", "")
	if b := decode[api.Server](t, body).Backups; b == nil || b.Destination != api.BackupDestinationNone {
		t.Fatalf("unexpected status: %+v", b)
	}
}
