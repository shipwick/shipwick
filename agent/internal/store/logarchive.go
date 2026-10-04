package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LogArchive is the record of one ended run of a container whose output was
// kept. The output itself is a file in the data directory, named after ID;
// the record says what it is the output of, and is what a search narrows
// down by before it opens a file.
type LogArchive struct {
	ID            int64
	ApplicationID int64
	Kind          string // api.LogKindReplica or api.LogKindRun
	// DeploymentID is nil once the deployment's record is gone. Sequence and
	// Version are read from it, and are not stored.
	DeploymentID  *int64
	Sequence      int
	Version       string
	Replica       int
	RunID         *int64
	Job           string
	ContainerID   string
	ContainerName string
	Reason        string
	ExitCode      *int
	OOMKilled     bool
	EndedAt       time.Time
	// FirstAt and LastAt are the times of the first and last line kept; nil
	// for an entry without lines.
	FirstAt     *time.Time
	LastAt      *time.Time
	Lines       int
	Bytes       int64
	StoredBytes int64
	Truncated   bool
	CreatedAt   time.Time
}

// LogArchiveFile names the file of an entry: the two numbers its path is
// made of.
type LogArchiveFile struct {
	ID            int64
	ApplicationID int64
}

// AddLogArchive records an entry and returns its id. An application or a run
// that has been deleted in the meantime is ErrNotFound: there is nothing left
// to keep the output for.
func (s *Store) AddLogArchive(ctx context.Context, a LogArchive, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO log_archives (application_id, kind, deployment_id, replica, run_id, job, container_id, container_name,
			reason, exit_code, oom_killed, ended_at, first_at, last_at, lines, bytes, stored_bytes, truncated, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ApplicationID, a.Kind, a.DeploymentID, a.Replica, a.RunID, a.Job, a.ContainerID, a.ContainerName,
		a.Reason, a.ExitCode, a.OOMKilled, formatTime(a.EndedAt), formatNullTime(a.FirstAt), formatNullTime(a.LastAt),
		a.Lines, a.Bytes, a.StoredBytes, a.Truncated, formatTime(now))
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("record log archive: %w", err)
	}
	return res.LastInsertId()
}

func formatNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

const logArchiveSelect = `
	SELECT l.id, l.application_id, l.kind, l.deployment_id, COALESCE(d.sequence, 0), COALESCE(d.version, ''), l.replica, l.run_id, l.job,
		l.container_id, l.container_name, l.reason, l.exit_code, l.oom_killed, l.ended_at, l.first_at, l.last_at,
		l.lines, l.bytes, l.stored_bytes, l.truncated, l.created_at
	FROM log_archives l LEFT JOIN deployments d ON d.id = l.deployment_id`

func scanLogArchive(row rowScanner) (LogArchive, error) {
	var (
		a                 LogArchive
		deployment, run   sql.NullInt64
		exitCode          sql.NullInt64
		ended, created    string
		firstAt, lastAt   sql.NullString
		oomKilled, cutOff bool
	)
	err := row.Scan(&a.ID, &a.ApplicationID, &a.Kind, &deployment, &a.Sequence, &a.Version, &a.Replica, &run, &a.Job,
		&a.ContainerID, &a.ContainerName, &a.Reason, &exitCode, &oomKilled, &ended, &firstAt, &lastAt,
		&a.Lines, &a.Bytes, &a.StoredBytes, &cutOff, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return LogArchive{}, ErrNotFound
	} else if err != nil {
		return LogArchive{}, err
	}
	a.OOMKilled, a.Truncated = oomKilled, cutOff
	if deployment.Valid {
		a.DeploymentID = &deployment.Int64
	}
	if run.Valid {
		a.RunID = &run.Int64
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		a.ExitCode = &code
	}
	if a.EndedAt, err = parseTime(ended); err != nil {
		return LogArchive{}, err
	}
	if a.FirstAt, err = parseNullTime(firstAt); err != nil {
		return LogArchive{}, err
	}
	if a.LastAt, err = parseNullTime(lastAt); err != nil {
		return LogArchive{}, err
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return LogArchive{}, err
	}
	return a, nil
}

// GetLogArchive returns one entry.
func (s *Store) GetLogArchive(ctx context.Context, id int64) (LogArchive, error) {
	return scanLogArchive(s.db.QueryRowContext(ctx, logArchiveSelect+` WHERE l.id = ?`, id))
}

// LogArchiveFilter narrows ListLogArchives. The zero value of a field does
// not narrow.
type LogArchiveFilter struct {
	ApplicationID int64
	Kind          string
	DeploymentID  int64
	Replica       int
	RunID         int64
	// Before lists entries with a smaller id: the page after the one that
	// ended there.
	Before int64
	// Since and Until keep the entries that have lines in that span of
	// time, or ended in it.
	Since, Until time.Time
	Limit        int
}

// ListLogArchives returns the entries matching f, newest first.
func (s *Store) ListLogArchives(ctx context.Context, f LogArchiveFilter) ([]LogArchive, error) {
	var where []string
	var args []any
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if f.ApplicationID != 0 {
		add(`l.application_id = ?`, f.ApplicationID)
	}
	if f.Kind != "" {
		add(`l.kind = ?`, f.Kind)
	}
	if f.DeploymentID != 0 {
		add(`l.deployment_id = ?`, f.DeploymentID)
	}
	if f.Replica != 0 {
		add(`l.replica = ?`, f.Replica)
	}
	if f.RunID != 0 {
		add(`l.run_id = ?`, f.RunID)
	}
	if f.Before != 0 {
		add(`l.id < ?`, f.Before)
	}
	if !f.Since.IsZero() {
		add(`COALESCE(l.last_at, l.ended_at) >= ?`, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		add(`COALESCE(l.first_at, l.ended_at) <= ?`, formatTime(f.Until))
	}
	query := logArchiveSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY l.id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list log archives: %w", err)
	}
	defer rows.Close()

	out := []LogArchive{}
	for rows.Next() {
		a, err := scanLogArchive(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LogArchivedThrough is the moment up to which the container's output has
// been archived: the later of when its last archived run ended and of its
// last archived line. The zero time: nothing of it has been.
func (s *Store) LogArchivedThrough(ctx context.Context, containerID string) (time.Time, error) {
	var ended string
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT ended_at, last_at FROM log_archives WHERE container_id = ? ORDER BY id DESC LIMIT 1`, containerID).Scan(&ended, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	} else if err != nil {
		return time.Time{}, fmt.Errorf("read log archive: %w", err)
	}
	through, err := parseTime(ended)
	if err != nil {
		return time.Time{}, err
	}
	if lastAt, err := parseNullTime(last); err != nil {
		return time.Time{}, err
	} else if lastAt != nil && lastAt.After(through) {
		through = *lastAt
	}
	return through, nil
}

// LogArchiveUsage is how many entries the archive has and how much their
// files take.
func (s *Store) LogArchiveUsage(ctx context.Context) (entries int, storedBytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(stored_bytes), 0) FROM log_archives`).Scan(&entries, &storedBytes)
	if err != nil {
		return 0, 0, fmt.Errorf("measure log archive: %w", err)
	}
	return entries, storedBytes, nil
}

// PruneLogArchivesBefore removes the entries of runs that ended before the
// given time and returns their files, which are the caller's to remove.
func (s *Store) PruneLogArchivesBefore(ctx context.Context, before time.Time) ([]LogArchiveFile, error) {
	return s.queryLogArchiveFiles(ctx, `DELETE FROM log_archives WHERE ended_at < ? RETURNING id, application_id`, formatTime(before))
}

// PruneLogArchivesTo removes entries until what is left takes at most
// maxBytes, and returns their files. The oldest entries of the application
// that holds the most go first: an application that fills the archive makes
// room out of its own output, and what a quiet application printed when it
// crashed last week stays.
func (s *Store) PruneLogArchivesTo(ctx context.Context, maxBytes int64) ([]LogArchiveFile, error) {
	var removed []LogArchiveFile
	for {
		_, total, err := s.LogArchiveUsage(ctx)
		if err != nil || total <= maxBytes {
			return removed, err
		}
		// The application that holds the most, and how much the next one
		// holds: the first gives until it is no longer the first.
		var appID, most, next int64
		rows, err := s.db.QueryContext(ctx,
			`SELECT application_id, SUM(stored_bytes) FROM log_archives GROUP BY application_id ORDER BY 2 DESC, 1 LIMIT 2`)
		if err != nil {
			return removed, fmt.Errorf("prune log archive: %w", err)
		}
		for i := 0; rows.Next(); i++ {
			var id, sum int64
			if err := rows.Scan(&id, &sum); err != nil {
				rows.Close()
				return removed, err
			}
			if i == 0 {
				appID, most = id, sum
			} else {
				next = sum
			}
		}
		if err := rows.Close(); err != nil {
			return removed, err
		}
		if most == 0 {
			return removed, nil // entries without lines take no space
		}

		rows, err = s.db.QueryContext(ctx,
			`SELECT id, stored_bytes FROM log_archives WHERE application_id = ? AND stored_bytes > 0 ORDER BY id`, appID)
		if err != nil {
			return removed, fmt.Errorf("prune log archive: %w", err)
		}
		var through int64
		for first := true; total > maxBytes && (first || most >= next) && rows.Next(); first = false {
			var size int64
			if err := rows.Scan(&through, &size); err != nil {
				rows.Close()
				return removed, err
			}
			total, most = total-size, most-size
		}
		if err := rows.Close(); err != nil {
			return removed, err
		}
		gone, err := s.queryLogArchiveFiles(ctx,
			`DELETE FROM log_archives WHERE application_id = ? AND stored_bytes > 0 AND id <= ? RETURNING id, application_id`, appID, through)
		if err != nil {
			return removed, err
		}
		removed = append(removed, gone...)
	}
}

// DeleteLogArchive removes one entry: one whose file is gone.
func (s *Store) DeleteLogArchive(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM log_archives WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete log archive %d: %w", id, err)
	}
	return nil
}

// LogArchiveFiles lists the files the archive's entries have: every entry
// that kept lines, of one application or, with appID 0, of all.
func (s *Store) LogArchiveFiles(ctx context.Context, appID int64) ([]LogArchiveFile, error) {
	return s.queryLogArchiveFiles(ctx,
		`SELECT id, application_id FROM log_archives WHERE stored_bytes > 0 AND (? = 0 OR application_id = ?) ORDER BY id`, appID, appID)
}

// queryLogArchiveFiles runs a statement that returns the files of entries:
// the ones it lists, or the ones it removes.
func (s *Store) queryLogArchiveFiles(ctx context.Context, query string, args ...any) ([]LogArchiveFile, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("prune log archive: %w", err)
	}
	defer rows.Close()
	var out []LogArchiveFile
	for rows.Next() {
		var f LogArchiveFile
		if err := rows.Scan(&f.ID, &f.ApplicationID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
