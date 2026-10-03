package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// adoptAgent answers POST /server/backups/adopt with what the test gives it,
// and remembers the body it was sent.
func adoptAgent(t *testing.T, status int, answer any) (*fakeAgent, *string) {
	t.Helper()
	f := newFakeAgent(t)
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/server/backups/adopt" || r.Header.Get("Authorization") != "Bearer "+testToken {
			respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint"})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		switch a := answer.(type) {
		case api.Error:
			respondError(w, status, a)
		case api.BackupAdoption:
			respond(w, status, a)
		}
	}))
	t.Cleanup(srv.Close)
	f.srv = srv
	return f, &body
}

func adopted(kind, application string, id int64, age time.Duration, bytes int64) api.AdoptedBackup {
	at := fixedNow.Add(-age)
	volumes := []api.BackupVolume{{Volume: "data", SizeBytes: bytes}}
	return api.AdoptedBackup{Kind: kind, Application: application, Backup: api.BackupRun{ID: id, Trigger: api.BackupTriggerAdopted,
		Status: api.BackupSucceeded, StartedAt: at, CompletedAt: &at, Volumes: volumes, Destinations: []string{"local", "s3"}, Encrypted: true}}
}

func TestBackupsAdoptListsWhatWasAdoptedAndWhatWasLeftAlone(t *testing.T) {
	f, body := adoptAgent(t, 200, api.BackupAdoption{
		Adopted: []api.AdoptedBackup{
			adopted(api.BackupKindApplication, "postgres", 14, 5*time.Hour, 39<<20),
			adopted(api.BackupKindState, "", 15, 4*time.Hour, 200<<10),
			adopted(api.BackupKindExport, "", 16, 3*time.Hour, 1<<30),
		},
		Skipped: []api.SkippedBackup{{Kind: api.BackupKindState, ID: 17, Reason: "encryption.key.enc is missing: the backup was not finished"}},
	})
	out, errOut, err := f.run(t.TempDir(), "backups", "adopt")
	if err != nil {
		t.Fatalf("backups adopt: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"BACKUP OF", "ID", "WHEN", "SIZE", "WHERE",
		"postgres", "14", "5h ago", "39 MB", "local, s3 (encrypted)",
		"the agent's state", "15", "4h ago",
		"an export", "16", "3h ago", "1 GB",
		"✓ Adopted 3 backups",
		"shipwick backups <app>",
	})
	if !strings.Contains(errOut, "Backup #17 of the agent's state was left alone: encryption.key.enc is missing") {
		t.Errorf("the run that was skipped is not mentioned:\n%s", errOut)
	}
	var sent api.BackupAdoptRequest
	if err := json.Unmarshal([]byte(*body), &sent); err != nil || sent.Application != "" {
		t.Errorf("the agent was sent %q", *body)
	}
}

func TestBackupsAdoptOfOneApplicationNamesIt(t *testing.T) {
	f, body := adoptAgent(t, 200, api.BackupAdoption{Adopted: []api.AdoptedBackup{adopted(api.BackupKindApplication, "postgres", 14, time.Hour, 39<<20)}})
	out, _, err := f.run(t.TempDir(), "backups", "adopt", "postgres")
	if err != nil || !strings.Contains(out, "✓ Adopted 1 backup of postgres") {
		t.Fatalf("got %v:\n%s", err, out)
	}
	if !strings.Contains(*body, `"application":"postgres"`) {
		t.Errorf("the agent was sent %q", *body)
	}
	if _, _, err := f.run(t.TempDir(), "backups", "adopt", "../etc"); err == nil {
		t.Error("a name that is no application's was sent to the agent")
	}
}

func TestBackupsAdoptSaysWhenThereIsNothingToAdopt(t *testing.T) {
	f, _ := adoptAgent(t, 200, api.BackupAdoption{Adopted: []api.AdoptedBackup{}, Skipped: []api.SkippedBackup{}})
	out, _, err := f.run(t.TempDir(), "backups", "adopt")
	if err != nil || !strings.Contains(out, "Nothing to adopt: the server knows every backup that its directory and bucket hold.") {
		t.Fatalf("got %v:\n%s", err, out)
	}
}

func TestBackupsAdoptExplainsARefusalAndAnOlderAgent(t *testing.T) {
	f, _ := adoptAgent(t, 409, api.Error{Code: api.CodeForeignBucket,
		Message: "the bucket holds the backups of another Shipwick installation under this prefix: to carry on as that installation, restore its state on this server"})
	_, _, err := f.run(t.TempDir(), "backups", "adopt")
	if msg := Render(err); !strings.Contains(msg, "another Shipwick installation") || !strings.Contains(msg, "restore its state") {
		t.Errorf("a foreign bucket: %q", msg)
	}

	// An agent from before the command existed.
	old := newFakeAgent(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondError(w, 404, api.Error{Code: api.CodeEndpointNotFound, Message: "no such endpoint: POST /api/v1/server/backups/adopt"})
	}))
	t.Cleanup(srv.Close)
	old.srv = srv
	_, _, err = old.run(t.TempDir(), "backups", "adopt")
	if msg := Render(err); !strings.Contains(msg, "older than this shipwick") {
		t.Errorf("an older agent: %q", msg)
	}
}

func TestValidateMentionsABeforeTimeoutSomebodyChose(t *testing.T) {
	plan := describeBackupPlan(mustBackups(t, "  before: [pg_dump, app]\n  before_timeout: 2h\n"))
	if !strings.Contains(plan, "after pg_dump app (2h at most)") {
		t.Errorf("plan = %q", plan)
	}
	plan = describeBackupPlan(mustBackups(t, "  before: [pg_dump, app]\n"))
	if strings.Contains(plan, "at most") {
		t.Errorf("the default limit is spelled out: %q", plan)
	}
}

// mustBackups parses a deploy.yaml with the given lines in its backups block.
func mustBackups(t *testing.T, lines string) spec.Backups {
	t.Helper()
	app, err := spec.Parse([]byte("name: db\nimage: postgres:17\nvolumes:\n  - name: data\n    path: /var/lib/postgresql/data\n" +
		"deploy:\n  strategy: recreate\nbackups:\n  schedule: \"0 3 * * *\"\n" + lines))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return *app.Backups
}
