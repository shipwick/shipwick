package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// BackupRun is one backup: of an application's volumes, or of the agent's own
// state. Its outcome never changes once it has completed; what is recorded
// afterwards is what was done with it — a verification, a restore.
type BackupRun struct {
	ID int64
	// Application is the application's name, or the owner the agent's own
	// state is kept under. A name, not a reference: backups outlive the
	// application's deletion, as its volumes do.
	Application  string
	Trigger      string // api.BackupTriggerSchedule or BackupTriggerManual
	Status       api.BackupRunStatus
	Volumes      []api.BackupVolume
	Destinations []string
	Encrypted    bool
	Error        string
	StartedAt    time.Time
	CompletedAt  *time.Time
	// Activity is api.BackupActivityVerify or BackupActivityRestore while one
	// is in progress.
	Activity     string
	VerifiedAt   *time.Time
	VerifyError  string
	VerifyOutput string
	RestoredAt   *time.Time
	RestoreError string
}

// ErrBackupBusy means the backup is not in the state the caller expected: it
// is being verified or restored, or still being taken.
var ErrBackupBusy = errors.New("the backup is in use")

// CreateBackupRun records a backup that is starting now.
func (s *Store) CreateBackupRun(ctx context.Context, application, trigger string, now time.Time) (BackupRun, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_runs (application, trigger, status, started_at) VALUES (?, ?, ?, ?)`,
		application, trigger, string(api.BackupRunning), formatTime(now))
	if err != nil {
		return BackupRun{}, fmt.Errorf("record backup: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return BackupRun{}, fmt.Errorf("record backup: %w", err)
	}
	return BackupRun{ID: id, Application: application, Trigger: trigger, Status: api.BackupRunning,
		Volumes: []api.BackupVolume{}, Destinations: []string{}, StartedAt: now.UTC()}, nil
}

// FinishBackupRun records how a backup ended and what it left where.
func (s *Store) FinishBackupRun(ctx context.Context, run BackupRun, now time.Time) error {
	volumes, err := json.Marshal(run.Volumes)
	if err != nil {
		return fmt.Errorf("encode volumes: %w", err)
	}
	destinations, err := json.Marshal(run.Destinations)
	if err != nil {
		return fmt.Errorf("encode destinations: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE backup_runs SET status = ?, volumes = ?, destinations = ?, encrypted = ?, error = ?, completed_at = ?
		 WHERE id = ? AND status = ?`,
		string(run.Status), string(volumes), string(destinations), run.Encrypted, run.Error, formatTime(now), run.ID, string(api.BackupRunning))
	if err != nil {
		return fmt.Errorf("finish backup %d: %w", run.ID, err)
	}
	return nil
}

// ClaimBackupRun marks a completed backup as being verified or restored. It
// fails with ErrBackupBusy when something is already being done with it.
func (s *Store) ClaimBackupRun(ctx context.Context, id int64, activity string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE backup_runs SET activity = ? WHERE id = ? AND activity = '' AND status != ?`,
		activity, id, string(api.BackupRunning))
	if err != nil {
		return fmt.Errorf("claim backup %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrBackupBusy
	}
	return nil
}

// ReleaseBackupRun gives up a claim that came to nothing.
func (s *Store) ReleaseBackupRun(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET activity = '' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("release backup %d: %w", id, err)
	}
	return nil
}

// FinishBackupVerify records the outcome of a verification: failure is the
// reason it failed, empty when the backup restored into a container that came
// up; output is the last of what that container wrote.
func (s *Store) FinishBackupVerify(ctx context.Context, id int64, failure, output string, now time.Time) error {
	var verifiedAt any
	if failure == "" {
		verifiedAt = formatTime(now)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_runs SET activity = '', verified_at = ?, verify_error = ?, verify_output = ? WHERE id = ?`,
		verifiedAt, failure, output, id)
	if err != nil {
		return fmt.Errorf("record verification of backup %d: %w", id, err)
	}
	return nil
}

// FinishBackupRestore records the outcome of a restore, like
// FinishBackupVerify.
func (s *Store) FinishBackupRestore(ctx context.Context, id int64, failure string, now time.Time) error {
	var restoredAt any
	if failure == "" {
		restoredAt = formatTime(now)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_runs SET activity = '', restored_at = ?, restore_error = ? WHERE id = ?`,
		restoredAt, failure, id)
	if err != nil {
		return fmt.Errorf("record restore of backup %d: %w", id, err)
	}
	return nil
}

// InterruptBackupRuns settles what an agent that went away left unfinished:
// backups still marked running have failed, and verifications and restores in
// progress have failed too. It returns the backups that were running, whose
// files are incomplete. Only meaningful at startup.
func (s *Store) InterruptBackupRuns(ctx context.Context, now time.Time) ([]BackupRun, error) {
	const reason = "the agent was restarted while this was in progress"
	running, err := s.listBackupRuns(ctx, backupRunSelect+` WHERE status = ?`, string(api.BackupRunning))
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE backup_runs SET status = ?, error = ?, completed_at = ? WHERE status = ?`,
			string(api.BackupFailed), reason, formatTime(now), string(api.BackupRunning)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE backup_runs SET activity = '', verified_at = NULL, verify_error = ? WHERE activity = ?`,
			reason, api.BackupActivityVerify); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE backup_runs SET activity = '', restored_at = NULL, restore_error = ? WHERE activity = ?`,
			reason, api.BackupActivityRestore); err != nil {
			return err
		}
		// Whatever else was claimed: a removal that did not get to the record.
		_, err := tx.ExecContext(ctx, `UPDATE backup_runs SET activity = '' WHERE activity != ''`)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("settle interrupted backups: %w", err)
	}
	return running, nil
}

const backupRunSelect = `
	SELECT id, application, trigger, status, volumes, destinations, encrypted, error, started_at, completed_at,
	       activity, verified_at, verify_error, verify_output, restored_at, restore_error
	FROM backup_runs`

func scanBackupRun(row rowScanner) (BackupRun, error) {
	var (
		r                     BackupRun
		status                string
		volumes, destinations string
		started               string
		completed, verified   sql.NullString
		restored              sql.NullString
	)
	err := row.Scan(&r.ID, &r.Application, &r.Trigger, &status, &volumes, &destinations, &r.Encrypted, &r.Error, &started, &completed,
		&r.Activity, &verified, &r.VerifyError, &r.VerifyOutput, &restored, &r.RestoreError)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRun{}, ErrNotFound
	} else if err != nil {
		return BackupRun{}, err
	}
	r.Status = api.BackupRunStatus(status)
	if err := json.Unmarshal([]byte(volumes), &r.Volumes); err != nil {
		return BackupRun{}, fmt.Errorf("decode volumes of backup %d: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(destinations), &r.Destinations); err != nil {
		return BackupRun{}, fmt.Errorf("decode destinations of backup %d: %w", r.ID, err)
	}
	if r.StartedAt, err = parseTime(started); err != nil {
		return BackupRun{}, err
	}
	if r.CompletedAt, err = parseNullTime(completed); err != nil {
		return BackupRun{}, err
	}
	if r.VerifiedAt, err = parseNullTime(verified); err != nil {
		return BackupRun{}, err
	}
	if r.RestoredAt, err = parseNullTime(restored); err != nil {
		return BackupRun{}, err
	}
	return r, nil
}

// GetBackupRun returns one backup of an application.
func (s *Store) GetBackupRun(ctx context.Context, application string, id int64) (BackupRun, error) {
	return scanBackupRun(s.db.QueryRowContext(ctx, backupRunSelect+` WHERE id = ? AND application = ?`, id, application))
}

// ListBackupRuns returns the backups of an application, newest first; limit 0
// means all of them.
func (s *Store) ListBackupRuns(ctx context.Context, application string, limit int) ([]BackupRun, error) {
	query := backupRunSelect + ` WHERE application = ? ORDER BY id DESC`
	args := []any{application}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.listBackupRuns(ctx, query, args...)
}

// LastBackupRun returns the newest backup of an application with the given
// status, or ErrNotFound.
func (s *Store) LastBackupRun(ctx context.Context, application string, status api.BackupRunStatus) (BackupRun, error) {
	return scanBackupRun(s.db.QueryRowContext(ctx,
		backupRunSelect+` WHERE application = ? AND status = ? ORDER BY id DESC LIMIT 1`, application, string(status)))
}

// DeleteBackupRun removes the record of a backup; its files are the caller's
// to remove.
func (s *Store) DeleteBackupRun(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM backup_runs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete backup %d: %w", id, err)
	}
	return nil
}

func (s *Store) listBackupRuns(ctx context.Context, query string, args ...any) ([]BackupRun, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()

	out := []BackupRun{}
	for rows.Next() {
		r, err := scanBackupRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Snapshot writes a consistent copy of the database to path, which must not
// exist yet. VACUUM INTO reads through a transaction of its own: the copy is
// the database as of one moment, which a copy of the file — and of the
// write-ahead log next to it — taken while the agent runs is not.
//
// key is the encryption key the copy's sealed values are under, nil for a
// database that is not encrypted. The two are taken together, with rotation
// kept out: a copy from before a rotation with the key from after it would be
// a backup nobody can read.
func (s *Store) Snapshot(ctx context.Context, path string) (key []byte, err error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return nil, fmt.Errorf("copy the database: %w", err)
	}
	return append([]byte(nil), s.rawKey...), nil
}

// BackupInstallation returns the name this installation marks its backups
// with, making one up the first time it is asked. It is not a secret: it
// tells one installation's database from another's, so that a server set up
// afresh does not write over the backups of the one it replaces.
func (s *Store) BackupInstallation(ctx context.Context) (string, error) {
	var id string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM backup_installation LIMIT 1`).Scan(&id)
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		id = hex.EncodeToString(raw)
		_, err = tx.ExecContext(ctx, `INSERT INTO backup_installation (id) VALUES (?)`, id)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("read installation id: %w", err)
	}
	return id, nil
}

// ReserveBackupRunIDs makes the next backup's id larger than highest. The
// database hands out ids from its own count, which a restored database, or
// a new one on a reinstalled server, has lost; the files of the runs it has
// forgotten are still where their ids put them.
func (s *Store) ReserveBackupRunIDs(ctx context.Context, highest int64) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// The row appears with the table's first insert; before that it has
		// to be made.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sqlite_sequence (name, seq) SELECT 'backup_runs', 0
			 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'backup_runs')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'backup_runs'`, highest)
		return err
	})
	if err != nil {
		return fmt.Errorf("reserve backup ids: %w", err)
	}
	return nil
}
