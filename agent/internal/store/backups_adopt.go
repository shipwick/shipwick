package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// BackupRunIDs returns the ids of all backups the database knows, whoever
// they belong to and however they ended.
func (s *Store) BackupRunIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM backup_runs`)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list backups: %w", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// AdoptBackupRun records a backup that was found in the destinations, complete
// and under the id its files carry: the one record that is not created as
// running and finished later. It reports false, and changes nothing, when the
// database has a backup of that id after all.
func (s *Store) AdoptBackupRun(ctx context.Context, run BackupRun) (bool, error) {
	volumes, err := json.Marshal(run.Volumes)
	if err != nil {
		return false, fmt.Errorf("encode volumes: %w", err)
	}
	destinations, err := json.Marshal(run.Destinations)
	if err != nil {
		return false, fmt.Errorf("encode destinations: %w", err)
	}
	completed := run.StartedAt
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO backup_runs (id, application, trigger, status, volumes, destinations, encrypted, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Application, run.Trigger, string(run.Status), string(volumes), string(destinations), run.Encrypted,
		formatTime(run.StartedAt), formatTime(completed))
	if err != nil {
		return false, fmt.Errorf("adopt backup %d: %w", run.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("adopt backup %d: %w", run.ID, err)
	}
	return n == 1, nil
}
