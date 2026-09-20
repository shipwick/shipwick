// Package store persists applications, deployments and events in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, static binaries
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a deployment is not in the state the
	// caller expected, i.e. a state transition lost a race.
	ErrConflict = errors.New("state conflict")
)

type Store struct {
	db *sql.DB
}

// Open opens (and creates, if needed) the database at path and applies any
// pending migrations. Use ":memory:" for an ephemeral database in tests.
func Open(ctx context.Context, path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	// A plain path (no "file:" prefix) keeps the driver from URI-decoding it;
	// the driver still strips and applies the query parameters.
	dsn := path
	pragmas := url.Values{}
	pragmas.Add("_pragma", "foreign_keys(1)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	if path != ":memory:" {
		pragmas.Add("_pragma", "journal_mode(WAL)")
		pragmas.Add("_pragma", "synchronous(NORMAL)")
	}
	dsn += "?" + pragmas.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection serializes all access. The agent's write volume is tiny,
	// and this rules out SQLITE_BUSY and lock-upgrade deadlocks entirely.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// migrations are applied in order; the index of the last applied one is kept
// in PRAGMA user_version. Never edit an entry once released: append a new one.
var migrations = []string{
	`
	CREATE TABLE applications (
		id                   INTEGER PRIMARY KEY,
		name                 TEXT NOT NULL UNIQUE,
		desired_state        TEXT NOT NULL DEFAULT 'running',
		active_deployment_id INTEGER REFERENCES deployments(id) ON DELETE SET NULL,
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL
	);

	CREATE TABLE deployments (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		sequence       INTEGER NOT NULL,
		version        TEXT NOT NULL,
		image          TEXT NOT NULL,
		spec           TEXT NOT NULL,
		status         TEXT NOT NULL,
		error          TEXT NOT NULL DEFAULT '',
		started_at     TEXT NOT NULL,
		completed_at   TEXT,
		UNIQUE (application_id, sequence)
	);
	CREATE INDEX deployments_status ON deployments(status);

	CREATE TABLE deployment_replicas (
		id             INTEGER PRIMARY KEY,
		deployment_id  INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
		replica_index  INTEGER NOT NULL,
		container_id   TEXT NOT NULL,
		container_name TEXT NOT NULL,
		created_at     TEXT NOT NULL,
		removed_at     TEXT
	);
	CREATE INDEX deployment_replicas_deployment ON deployment_replicas(deployment_id);

	CREATE TABLE events (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		deployment_id  INTEGER REFERENCES deployments(id) ON DELETE CASCADE,
		level          TEXT NOT NULL,
		type           TEXT NOT NULL,
		message        TEXT NOT NULL,
		created_at     TEXT NOT NULL
	);
	CREATE INDEX events_deployment ON events(deployment_id);
	CREATE INDEX events_application ON events(application_id);
	`,
	// 2: the supervisor counts how often it had to restart each replica.
	`ALTER TABLE deployment_replicas ADD COLUMN restart_count INTEGER NOT NULL DEFAULT 0;`,
	// 3: how a deployment came to be. A rollback or redeploy re-uses the spec
	// stored with source_deployment_id.
	`ALTER TABLE deployments ADD COLUMN kind TEXT NOT NULL DEFAULT 'deploy';
	 ALTER TABLE deployments ADD COLUMN source_deployment_id INTEGER REFERENCES deployments(id) ON DELETE SET NULL;`,
}

func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this agent supports (%d); upgrade the agent", current, len(migrations))
	}
	for i := current; i < len(migrations); i++ {
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return err
			}
			// PRAGMA does not accept bind parameters; i is a trusted integer.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Timestamps are stored as fixed-width UTC text so they sort lexicographically.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(timeLayout, s)
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
