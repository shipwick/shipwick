package store

import (
	"context"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestAdoptBackupRunRecordsABackupUnderTheIDItsFilesCarry(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	written := time.Date(2026, 9, 30, 3, 0, 12, 0, time.UTC)
	// What the restored database knows, and how far its ids were moved on.
	known, err := s.CreateBackupRun(ctx, "db", api.BackupTriggerSchedule, written.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveBackupRunIDs(ctx, 41); err != nil {
		t.Fatal(err)
	}

	found := BackupRun{ID: 40, Application: "db", Trigger: api.BackupTriggerAdopted, Status: api.BackupSucceeded,
		Volumes: []api.BackupVolume{{Volume: "data", SizeBytes: 2048}}, Destinations: []string{"s3"}, Encrypted: true, StartedAt: written}
	adopted, err := s.AdoptBackupRun(ctx, found)
	if err != nil || !adopted {
		t.Fatalf("AdoptBackupRun: %v, %v", adopted, err)
	}
	got, err := s.GetBackupRun(ctx, "db", 40)
	if err != nil {
		t.Fatalf("GetBackupRun: %v", err)
	}
	if got.Trigger != api.BackupTriggerAdopted || got.Status != api.BackupSucceeded || !got.Encrypted || got.Activity != "" ||
		len(got.Volumes) != 1 || got.Volumes[0].SizeBytes != 2048 || len(got.Destinations) != 1 ||
		!got.StartedAt.Equal(written) || got.CompletedAt == nil || !got.CompletedAt.Equal(written) {
		t.Fatalf("unexpected run: %+v", got)
	}
	// It can be claimed like one that was taken here.
	if err := s.ClaimBackupRun(ctx, 40, api.BackupActivityVerify); err != nil {
		t.Fatalf("ClaimBackupRun: %v", err)
	}

	// A backup the database knows is not replaced by what was found under
	// its id.
	found.ID, found.Application = known.ID, "other"
	if adopted, err := s.AdoptBackupRun(ctx, found); err != nil || adopted {
		t.Fatalf("adopting over a known backup: %v, %v", adopted, err)
	}
	if kept, err := s.GetBackupRun(ctx, "db", known.ID); err != nil || kept.Status != api.BackupRunning {
		t.Fatalf("the known backup was changed: %+v, %v", kept, err)
	}

	ids, err := s.BackupRunIDs(ctx)
	if err != nil || len(ids) != 2 || !ids[known.ID] || !ids[40] {
		t.Fatalf("BackupRunIDs: %v, %v", ids, err)
	}
	// The ids handed out next are still past everything the destinations hold.
	next, err := s.CreateBackupRun(ctx, "db", api.BackupTriggerManual, written)
	if err != nil || next.ID != 42 {
		t.Fatalf("the next backup is %d (%v), want 42", next.ID, err)
	}
}
