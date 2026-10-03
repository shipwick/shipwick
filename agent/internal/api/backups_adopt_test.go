package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestAdoptRecordsForgottenBackupsAndSaysWhich(t *testing.T) {
	f, _ := newBackupFixture(t, backupPassphrase)
	f.deployBackedUp(map[string]string{"a.txt": "alpha"})
	taken := f.takeBackup()
	// The database is restored from a backup of it that is older than this.
	if err := f.store.DeleteBackupRun(context.Background(), taken.ID); err != nil {
		t.Fatal(err)
	}

	status, body := f.do("POST", "/api/v1/server/backups/adopt", "")
	if status != http.StatusOK {
		t.Fatalf("adopt: %d %s", status, body)
	}
	adoption := decode[api.BackupAdoption](t, body)
	if len(adoption.Adopted) != 1 || len(adoption.Skipped) != 0 {
		t.Fatalf("unexpected adoption: %s", body)
	}
	a := adoption.Adopted[0]
	if a.Kind != api.BackupKindApplication || a.Application != "db" || a.Backup.ID != taken.ID || a.Backup.Trigger != api.BackupTriggerAdopted ||
		a.Backup.Status != api.BackupSucceeded || !a.Backup.Encrypted || a.Backup.Volumes[0] != taken.Volumes[0] {
		t.Fatalf("unexpected adopted backup: %s", body)
	}
	if got := f.getBackup(taken.ID); got.Trigger != api.BackupTriggerAdopted {
		t.Fatalf("the backup is not listed as adopted: %+v", got)
	}

	// The shape clients rely on: lists, never null.
	status, body = f.do("POST", "/api/v1/server/backups/adopt", `{"application": "db"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"adopted":[]`) || !strings.Contains(string(body), `"skipped":[]`) {
		t.Fatalf("a second adoption: %d %s", status, body)
	}
}

func TestAdoptValidatesItsBodyAndTakesAdmin(t *testing.T) {
	f, _ := newBackupFixture(t, "")
	for _, body := range []string{`{"application": "../etc"}`, `{"application": "Db"}`, `not json`} {
		status, answer := f.do("POST", "/api/v1/server/backups/adopt", body)
		if status != http.StatusBadRequest || decodeError(t, answer).Code != api.CodeInvalidRequest {
			t.Errorf("body %s: %d %s", body, status, answer)
		}
	}
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token
	if status, body := f.doWithAuth("POST", "/api/v1/server/backups/adopt", "", deployer); status != http.StatusForbidden {
		t.Errorf("a deploy token adopted backups: %d %s", status, body)
	}

	// An agent with nowhere to keep backups has nothing to adopt from.
	plain := newFixture(t)
	status, body := plain.do("POST", "/api/v1/server/backups/adopt", "")
	if status != http.StatusConflict || decodeError(t, body).Code != api.CodeBackupNotUsable {
		t.Errorf("without backups: %d %s", status, body)
	}
}
