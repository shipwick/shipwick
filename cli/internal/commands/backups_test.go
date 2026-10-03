package commands

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

func init() { backupfile.Iterations = 1000 }

// backupAgent answers the backup endpoints in front of the fake agent, which
// keeps serving everything else. What a started backup, verification or
// restore ends as is decided by the test; every poll but the first sees it
// ended, so that the commands' waiting is exercised.
type backupAgent struct {
	*fakeAgent

	mu     sync.Mutex
	runs   []api.BackupRunDetail // newest first, like the agent lists them
	state  []api.BackupRun
	polls  map[int64]int
	calls  []string
	nextID int64

	backupError  string // a backup that is started fails with this
	verifyError  string // a verification that is started fails with this
	restoreError string
	startStatus  int // non-zero: starting anything fails with this status and startError
	startError   api.Error
}

func newBackupAgent(t *testing.T, runs ...api.BackupRunDetail) *backupAgent {
	t.Helper()
	b := &backupAgent{fakeAgent: newFakeAgent(t), runs: runs, polls: map[int64]int{}, nextID: 100}
	upstream, _ := url.Parse(b.srv.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)

	find := func(r *http.Request) *api.BackupRunDetail {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		for i := range b.runs {
			if b.runs[i].ID == id || (r.PathValue("id") == "latest" && b.runs[i].Status == api.BackupSucceeded) {
				return &b.runs[i]
			}
		}
		return nil
	}
	refuse := func(w http.ResponseWriter) bool {
		if b.startStatus != 0 {
			respondError(w, b.startStatus, b.startError)
			return true
		}
		return false
	}
	record := func(r *http.Request) {
		b.calls = append(b.calls, r.Method+" "+r.URL.Path)
	}
	now := fixedNow

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/applications/{name}/backups", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		out := make([]api.BackupRun, 0, len(b.runs))
		for _, run := range b.runs {
			out = append(out, run.BackupRun)
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("POST /api/v1/applications/{name}/backups", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		record(r)
		if refuse(w) {
			return
		}
		b.nextID++
		run := api.BackupRunDetail{BackupRun: api.BackupRun{ID: b.nextID, Trigger: api.BackupTriggerManual, Status: api.BackupRunning,
			StartedAt: now, Volumes: []api.BackupVolume{}, Destinations: []string{}}}
		b.runs = append([]api.BackupRunDetail{run}, b.runs...)
		respond(w, 202, run.BackupRun)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/backups/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		run := find(r)
		if run == nil {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		// The first poll sees it in progress, the next ones see how it ended.
		if b.polls[run.ID]++; b.polls[run.ID] > 1 {
			switch {
			case run.Status == api.BackupRunning && b.backupError != "":
				run.Status, run.Error, run.CompletedAt = api.BackupFailed, b.backupError, &now
			case run.Status == api.BackupRunning:
				run.Status, run.CompletedAt = api.BackupSucceeded, &now
				run.Volumes = []api.BackupVolume{{Volume: "data", SizeBytes: 3 << 20}}
				run.Destinations, run.Encrypted = []string{"local", "s3"}, true
			case run.Activity == api.BackupActivityVerify && b.verifyError != "":
				run.Activity, run.VerifiedAt, run.VerifyError, run.VerifyOutput = "", nil, b.verifyError, "FATAL: database files are incompatible with server"
			case run.Activity == api.BackupActivityVerify:
				run.Activity, run.VerifiedAt, run.VerifyError, run.VerifyOutput = "", &now, "", "database system is ready to accept connections"
			case run.Activity == api.BackupActivityRestore && b.restoreError != "":
				run.Activity, run.RestoredAt, run.RestoreError = "", nil, b.restoreError
			case run.Activity == api.BackupActivityRestore:
				run.Activity, run.RestoredAt = "", &now
			}
		}
		respond(w, 200, *run)
	})
	for _, activity := range []string{api.BackupActivityVerify, api.BackupActivityRestore} {
		mux.HandleFunc("POST /api/v1/applications/{name}/backups/{id}/"+activity, func(w http.ResponseWriter, r *http.Request) {
			b.mu.Lock()
			defer b.mu.Unlock()
			record(r)
			if refuse(w) {
				return
			}
			run := find(r)
			if run == nil {
				respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "there is no successful backup yet; take one with: shipwick backups run"})
				return
			}
			run.Activity = activity
			b.polls[run.ID] = 0
			respond(w, 202, run.BackupRun)
		})
	}
	mux.HandleFunc("DELETE /api/v1/applications/{name}/backups/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		record(r)
		if find(r) == nil {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/applications/{name}/backups/{id}/volumes/{volume}/archive", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		record(r)
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(tarOf(t, map[string]string{r.PathValue("volume") + ".txt": "from backup " + r.PathValue("id")}))
	})
	mux.HandleFunc("POST /api/v1/server/backups", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		record(r)
		if refuse(w) {
			return
		}
		b.state = []api.BackupRun{{ID: 7, Trigger: api.BackupTriggerManual, Status: api.BackupRunning, StartedAt: now}}
		respond(w, 202, b.state[0])
	})
	mux.HandleFunc("GET /api/v1/server/backups/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		run := &b.state[0]
		if b.polls[-1]++; b.polls[-1] > 1 {
			if b.backupError != "" {
				run.Status, run.Error, run.CompletedAt = api.BackupFailed, b.backupError, &now
			} else {
				run.Status, run.CompletedAt, run.Encrypted, run.Destinations = api.BackupSucceeded, &now, true, []string{"local", "s3"}
				run.Volumes = []api.BackupVolume{{Volume: "shipwick.db", SizeBytes: 200 << 10}, {Volume: "encryption.key", SizeBytes: 65}}
			}
		}
		respond(w, 200, *run)
	})
	mux.Handle("/", proxy)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" && r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 401, api.Error{Code: api.CodeUnauthorized, Message: "missing or invalid API token"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	b.srv = srv
	return b
}

func (b *backupAgent) called() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.calls, "\n")
}

func backupRun(id int64, status api.BackupRunStatus, age time.Duration, mutate ...func(*api.BackupRun)) api.BackupRunDetail {
	started := fixedNow.Add(-age)
	run := api.BackupRun{ID: id, Trigger: api.BackupTriggerSchedule, Status: status, StartedAt: started, CompletedAt: &started,
		Volumes: []api.BackupVolume{}, Destinations: []string{}}
	if status == api.BackupSucceeded {
		run.Volumes = []api.BackupVolume{{Volume: "data", SizeBytes: 2 << 30}, {Volume: "uploads", SizeBytes: 100 << 20}}
		run.Destinations, run.Encrypted = []string{"local", "s3"}, true
	}
	for _, m := range mutate {
		m(&run)
	}
	return api.BackupRunDetail{BackupRun: run}
}

func TestBackupsListsWhatTheServerKeeps(t *testing.T) {
	verified := fixedNow.Add(-time.Hour)
	f := newBackupAgent(t,
		backupRun(14, api.BackupFailed, 5*time.Hour, func(r *api.BackupRun) { r.Error = "backups.before exited 1: pg_dump: connection refused" }),
		backupRun(13, api.BackupSucceeded, 29*time.Hour, func(r *api.BackupRun) { r.VerifiedAt = &verified }),
		backupRun(12, api.BackupSucceeded, 53*time.Hour, func(r *api.BackupRun) {
			r.Trigger, r.VerifyError, r.Encrypted, r.Destinations = api.BackupTriggerManual, "exited with code 1", false, []string{"local"}
		}),
	)
	out, _, err := f.run(t.TempDir(), "backups", "db")
	if err != nil {
		t.Fatalf("backups: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"ID", "WHEN", "TRIGGER", "SIZE", "WHERE", "STATUS", "VERIFIED",
		"14", "5h ago", "schedule", "-", "-", "failed", "-",
		"13", "1d ago", "schedule", "2.1 GB", "local, s3 (encrypted)", "succeeded", "1h ago",
		"12", "2d ago", "manual", "2.1 GB", "local", "succeeded", "failed",
		"Backup #14 failed: backups.before exited 1: pg_dump: connection refused",
	})
}

func TestBackupsSaysWhenThereAreNone(t *testing.T) {
	f := newBackupAgent(t)
	out, _, err := f.run(t.TempDir(), "backups", "db")
	if err != nil || !strings.Contains(out, "The server keeps no backups of db. Take one with: shipwick backups run db") {
		t.Fatalf("got %v:\n%s", err, out)
	}
}

func TestBackupsRunWaitsForTheBackup(t *testing.T) {
	f := newBackupAgent(t)
	out, _, err := f.run(t.TempDir(), "backups", "run", "db")
	if err != nil {
		t.Fatalf("backups run: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Backup #101 of db: 3 MB to local, s3 (encrypted)", "shipwick backups verify db"})

	f.backupError = "volume data: the bucket answered HTTP 403 AccessDenied: Access Denied"
	out, _, err = f.run(t.TempDir(), "backups", "run", "db")
	if !errors.Is(err, ErrReported) || !strings.Contains(out, "✗ Backup #102 of db failed: volume data: the bucket answered HTTP 403 AccessDenied") {
		t.Fatalf("a failed backup: %v\n%s", err, out)
	}
}

func TestBackupsRunExplainsRefusals(t *testing.T) {
	f := newBackupAgent(t)
	for code, want := range map[string]string{
		api.CodeNoVolumes:            "This application has no volumes, so there is nothing to back up.",
		api.CodeDeploymentInProgress: "Another operation is already in progress",
		api.CodeEndpointNotFound:     "it is probably older than this shipwick",
	} {
		f.startStatus, f.startError = 409, api.Error{Code: code, Message: "the agent's words"}
		_, _, err := f.run(t.TempDir(), "backups", "run", "db")
		if got := Render(err); !strings.Contains(got, want) {
			t.Errorf("%s renders as %q, want it to contain %q", code, got, want)
		}
	}
}

func TestBackupsVerifyDefaultsToTheLatestAndReportsTheVerdict(t *testing.T) {
	f := newBackupAgent(t, backupRun(13, api.BackupSucceeded, time.Hour), backupRun(12, api.BackupSucceeded, 25*time.Hour))
	out, _, err := f.run(t.TempDir(), "backups", "verify", "db")
	if err != nil {
		t.Fatalf("backups verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ Backup #13 of db restores: a container of the current version came up on its data") {
		t.Errorf("output:\n%s", out)
	}
	if got := f.called(); got != "POST /api/v1/applications/db/backups/latest/verify" {
		t.Errorf("called:\n%s", got)
	}

	f.verifyError = "the container exited with code 1 on the restored data"
	out, _, err = f.run(t.TempDir(), "backups", "verify", "db", "12")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("a backup that does not verify must exit non-zero: %v", err)
	}
	if !strings.Contains(out, "FATAL: database files are incompatible with server") ||
		!strings.Contains(out, "✗ Backup #12 of db did not verify: the container exited with code 1 on the restored data") {
		t.Errorf("output:\n%s", out)
	}
	if !strings.HasSuffix(f.called(), "POST /api/v1/applications/db/backups/12/verify") {
		t.Errorf("called:\n%s", f.called())
	}
}

func TestBackupsRestoreAsksForTheNameAndWaits(t *testing.T) {
	f := newBackupAgent(t, backupRun(13, api.BackupSucceeded, time.Hour))
	if _, _, err := f.run(t.TempDir(), "backups", "restore", "db", "13"); err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("without a terminal and without --yes: %v", err)
	}
	if f.called() != "" {
		t.Fatalf("the agent was called before the confirmation:\n%s", f.called())
	}

	out, _, err := f.run(t.TempDir(), "backups", "restore", "db", "13", "--yes")
	if err != nil {
		t.Fatalf("backups restore: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Restored backup #13 into db", "Start it with: shipwick start db"})

	f.restoreError = "volume data: the passphrase does not match this backup, or the file is damaged"
	out, _, err = f.run(t.TempDir(), "backups", "restore", "db", "#13", "-y")
	if !errors.Is(err, ErrReported) || !strings.Contains(out, "✗ Backup #13 was not restored into db: volume data: the passphrase does not match") {
		t.Fatalf("a failed restore: %v\n%s", err, out)
	}

	f.startStatus, f.startError = 409, api.Error{Code: api.CodeApplicationRunning, Message: "the application is running; stop it first with: shipwick stop"}
	_, _, err = f.run(t.TempDir(), "backups", "restore", "db", "13", "--yes")
	if got := Render(err); !strings.Contains(got, "Stop it first with: shipwick stop") {
		t.Errorf("a running application renders as %q", got)
	}
	if _, _, err := f.run(t.TempDir(), "backups", "restore", "db", "latest", "--yes"); err == nil || !strings.Contains(err.Error(), "is not a backup id") {
		t.Errorf("a restore without an explicit id: %v", err)
	}
}

func TestBackupsDownloadWritesOneArchivePerVolume(t *testing.T) {
	f := newBackupAgent(t, backupRun(13, api.BackupSucceeded, time.Hour), backupRun(12, api.BackupFailed, 2*time.Hour))
	dir := t.TempDir()
	out, _, err := f.run(t.TempDir(), "backups", "download", "db", "13", "-o", dir)
	if err != nil {
		t.Fatalf("backups download: %v\n%s", err, out)
	}
	for _, volume := range []string{"data", "uploads"} {
		name := "db-" + volume + "-backup-13.tar"
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, tarOf(t, map[string]string{volume + ".txt": "from backup 13"})) {
			t.Errorf("%s: %v", name, err)
		}
		if !strings.Contains(out, name) {
			t.Errorf("output does not name %s:\n%s", name, out)
		}
	}
	// Never over what is there.
	if _, _, err := f.run(t.TempDir(), "backups", "download", "db", "13", "-o", dir); err == nil {
		t.Error("a second download overwrote the first")
	}
	if _, _, err := f.run(t.TempDir(), "backups", "download", "db", "12", "-o", dir); err == nil || !strings.Contains(err.Error(), "nothing to download") {
		t.Errorf("downloading a failed backup: %v", err)
	}
}

func TestBackupsRm(t *testing.T) {
	f := newBackupAgent(t, backupRun(13, api.BackupSucceeded, time.Hour))
	out, _, err := f.run(t.TempDir(), "backups", "rm", "db", "13")
	if err != nil || !strings.Contains(out, "✓ Removed backup #13 of db") || f.called() != "DELETE /api/v1/applications/db/backups/13" {
		t.Fatalf("backups rm: %v\n%s\n%s", err, out, f.called())
	}
	if _, _, err := f.run(t.TempDir(), "backups", "rm", "db", "thirteen"); err == nil {
		t.Error("an id that is not a number was accepted")
	}
}

func sealedFile(t *testing.T, name, content, passphrase string) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := backupfile.NewWriter(&buf, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, content)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupsDecryptWorksOnThisMachineAlone(t *testing.T) {
	f := newFakeAgent(t)
	path := sealedFile(t, "encryption.key.enc", "00ff00ff\n", "correct horse battery staple")
	f.env = map[string]string{envBackupPassphrase: "correct horse battery staple"}

	out, _, err := f.run(t.TempDir(), "backups", "decrypt", path)
	if err != nil {
		t.Fatalf("backups decrypt: %v\n%s", err, out)
	}
	plain := strings.TrimSuffix(path, ".enc")
	if data, err := os.ReadFile(plain); err != nil || string(data) != "00ff00ff\n" {
		t.Fatalf("decrypted file: %q, %v", data, err)
	}
	if !strings.Contains(out, plain) {
		t.Errorf("output does not name the file:\n%s", out)
	}
	if len(f.requests) != 0 {
		t.Errorf("decrypting talked to the agent: %v", f.requests)
	}
	// An existing file is not overwritten.
	if _, _, err := f.run(t.TempDir(), "backups", "decrypt", path); err == nil {
		t.Error("a second decrypt overwrote the first")
	}
}

func TestBackupsDecryptLeavesNothingBehindWhenItFails(t *testing.T) {
	f := newFakeAgent(t)
	path := sealedFile(t, "data.tar.enc", strings.Repeat("archive ", 20_000), "the right passphrase")
	out := filepath.Join(t.TempDir(), "data.tar")

	f.env = map[string]string{envBackupPassphrase: "the wrong passphrase"}
	_, _, err := f.run(t.TempDir(), "backups", "decrypt", path, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "passphrase does not match") {
		t.Fatalf("a wrong passphrase: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file was left behind: %v", err)
	}

	// Cut short: the chunks that are whole decrypt, the file as a whole must not.
	data, _ := os.ReadFile(path)
	os.WriteFile(path, data[:len(data)-100], 0o600)
	f.env = map[string]string{envBackupPassphrase: "the right passphrase"}
	_, _, err = f.run(t.TempDir(), "backups", "decrypt", path, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "truncated or damaged") {
		t.Fatalf("a truncated file: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part of a truncated backup was left behind: %v", err)
	}

	f.env = nil
	if _, _, err := f.run(t.TempDir(), "backups", "decrypt", path, "-o", out); err == nil || !strings.Contains(err.Error(), envBackupPassphrase) {
		t.Errorf("without a passphrase or a terminal: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "data.tar")
	os.WriteFile(plain, tarOf(t, map[string]string{"a.txt": "alpha"}), 0o600)
	if _, _, err := f.run(t.TempDir(), "backups", "decrypt", plain, "-o", out); err == nil || !strings.Contains(err.Error(), "not an encrypted Shipwick backup") {
		t.Errorf("a plain archive: %v", err)
	}
}

func TestServerBackupWaitsForTheStateBackup(t *testing.T) {
	f := newBackupAgent(t)
	out, _, err := f.run(t.TempDir(), "server", "backup")
	if err != nil || !strings.Contains(out, "✓ Agent state backed up: 200.1 KB to local, s3 (encrypted) (backup #7)") {
		t.Fatalf("server backup: %v\n%s", err, out)
	}

	f.startStatus, f.startError = 409, api.Error{Code: api.CodeBackupsNotEncrypted,
		Message: "the agent's state is not backed up: SHIPWICK_BACKUP_PASSPHRASE is not set, and the encryption key is never written anywhere unencrypted"}
	_, _, err = f.run(t.TempDir(), "server", "backup")
	if got := Render(err); !strings.Contains(got, "SHIPWICK_BACKUP_PASSPHRASE is not set") || !strings.Contains(got, "/opt/shipwick/.env") {
		t.Errorf("without a passphrase it renders as %q", got)
	}
}

func TestStatusShowsTheBackupsOfAnApplicationWithVolumes(t *testing.T) {
	f := newBackupAgent(t, backupRun(13, api.BackupSucceeded, 5*time.Hour), backupRun(12, api.BackupSucceeded, 29*time.Hour))
	started := fixedNow.Add(-2 * time.Hour)
	f.app = api.ApplicationDetail{
		Application: api.Application{Name: "db", Status: api.AppHealthy, Replicas: api.ReplicaCount{Desired: 1, Running: 1, Healthy: 1}},
		Spec: &spec.App{Volumes: []spec.Volume{{Name: "data", Path: "/var/lib/data"}},
			Backups: &spec.Backups{Schedule: "0 3 * * *", Keep: 7}},
		ActiveDeployment: &api.Deployment{Sequence: 1, Version: "17", Image: "postgres:17", StartedAt: started, CompletedAt: &started},
	}
	out, _, err := f.run(t.TempDir(), "status", "db")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Limits", "Backups", "daily at 03:00 UTC, last 5h ago (2.1 GB), 2 kept"})

	// An application without volumes has nothing to say about backups.
	f.app.Spec = &spec.App{}
	out, _, _ = f.run(t.TempDir(), "status", "db")
	if strings.Contains(out, "Backups") {
		t.Errorf("a Backups line for an application without volumes:\n%s", out)
	}
}

func TestDescribeBackups(t *testing.T) {
	c, _ := newRoot(Options{Out: io.Discard, Err: io.Discard, Getenv: func(string) string { return "" }})
	daily := &spec.Backups{Schedule: "30 2 * * *", Keep: 7}
	runs := func(details ...api.BackupRunDetail) []api.BackupRun {
		var out []api.BackupRun
		for _, d := range details {
			out = append(out, d.BackupRun)
		}
		return out
	}
	failed := backupRun(3, api.BackupFailed, 5*time.Hour, func(r *api.BackupRun) { r.Error = "volume data: the bucket answered HTTP 403" })
	good := backupRun(2, api.BackupSucceeded, 29*time.Hour)

	tests := map[string]struct {
		b    *spec.Backups
		runs []api.BackupRun
		want string
	}{
		"scheduled, none yet":   {daily, nil, "daily at 02:30 UTC, none taken yet"},
		"scheduled, last good":  {daily, runs(good), "daily at 02:30 UTC, last 1d ago (2.1 GB), 1 kept"},
		"last one failed":       {daily, runs(failed, good), "daily at 02:30 UTC, last one failed 5h ago: volume data: the bucket answered HTTP 403 (last good one 1d ago)"},
		"only failures":         {daily, runs(failed), "daily at 02:30 UTC, last one failed 5h ago: volume data: the bucket answered HTTP 403"},
		"by hand only":          {nil, runs(good), "none scheduled, last 1d ago (2.1 GB), 1 kept"},
		"nothing at all":        {nil, nil, "none  (add `backups` to deploy.yaml, or take one with: shipwick backups run db)"},
		"another schedule":      {&spec.Backups{Schedule: "0 */6 * * *"}, nil, "on 0 */6 * * * (UTC), none taken yet"},
		"hourly":                {&spec.Backups{Schedule: "15 * * * *"}, nil, "hourly at :15, none taken yet"},
		"one still being taken": {daily, runs(backupRun(4, api.BackupRunning, time.Minute), good), "daily at 02:30 UTC, last 1d ago (2.1 GB), 1 kept"},
	}
	for name, tc := range tests {
		if got := c.describeBackups("db", tc.b, tc.runs, fixedNow); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}

func TestDoctorReportsTheAgentsStateBackup(t *testing.T) {
	last := fixedNow.Add(-3 * time.Hour)
	tests := map[string]struct {
		status *api.BackupStatus
		want   string
		hints  int
	}{
		"an older agent": {nil, "", 0},
		"no passphrase": {&api.BackupStatus{Destination: "local", StateError: "not set"},
			"! The encryption key exists only on this server. Set SHIPWICK_BACKUP_PASSPHRASE (and an S3 bucket) in /opt/shipwick/.env to back it up; losing it loses every secret", 1},
		"backed up to a bucket": {&api.BackupStatus{Destination: "s3", Encrypted: true, StateLastAt: &last},
			"✓ Agent state backed up 3h ago to s3", 0},
		"backed up locally": {&api.BackupStatus{Destination: "local", Encrypted: true, StateLastAt: &last},
			"✓ Agent state backed up 3h ago to the server's own disk", 0},
		"last attempt failed": {&api.BackupStatus{Destination: "s3", Encrypted: true, StateLastAt: &last, StateError: "the bucket answered HTTP 403"},
			"! The last backup of the agent's state failed: the bucket answered HTTP 403. The last good one is from 3h ago", 1},
		"never succeeded": {&api.BackupStatus{Destination: "s3", Encrypted: true, StateError: "the bucket answered HTTP 403"},
			"! The agent's state has never been backed up: the bucket answered HTTP 403", 1},
		"not yet": {&api.BackupStatus{Destination: "s3", Encrypted: true}, "! The agent's state has not been backed up yet", 1},
	}
	for name, tc := range tests {
		var out bytes.Buffer
		c, _ := newRoot(Options{Out: &out, Err: &out, Getenv: func(string) string { return "" }})
		r := &report{c: c}
		r.checkStateBackup(tc.status, fixedNow)
		if got := strings.TrimSpace(out.String()); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
		if r.hints != tc.hints || r.problems != 0 {
			t.Errorf("%s: %d hints and %d problems, want %d and 0", name, r.hints, r.problems, tc.hints)
		}
	}
}

func TestDoctorHasALineForTheAgentsState(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	f.server.Backups = &api.BackupStatus{Destination: api.BackupDestinationLocal, StateError: "not set"}
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Proxy serving 2 routes", "! The encryption key exists only on this server.", "No problems; 3 things worth a look."})
}

func TestValidateShowsWhatTheBackupsBlockWillDo(t *testing.T) {
	f := newFakeAgent(t)
	dir := writeConfig(t, `name: db
image: postgres:17
volumes:
  - name: data
    path: /var/lib/postgresql/data
deploy:
  strategy: recreate
backups:
  schedule: "0 3 * * *"
  before: ["pg_dump", "-f", "/var/lib/postgresql/data/backup.sql", "app"]
  stop: true
`)
	out, _, err := f.run(dir, "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Backups", "daily at 03:00 UTC, 7 kept, after pg_dump -f /var/lib/postgresql/data/backup.sql app, with the application stopped"})

	_, _, err = f.run(writeConfig(t, "name: api\nimage: nginx:1.27\nbackups:\n  schedule: \"0 3 * * *\"\n"), "validate")
	if got := Render(err); !strings.Contains(got, "backups:") || !strings.Contains(got, "needs volumes") {
		t.Errorf("backups without volumes renders as:\n%s", got)
	}
}
