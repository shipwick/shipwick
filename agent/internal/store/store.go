// Package store persists applications, deployments and events in SQLite.
package store

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
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
	// aead encrypts environment values on their way in and out (crypto.go);
	// nil only for an in-memory database, which never outlives the process.
	aead cipher.AEAD

	// What key rotation needs (rotation.go). keyMu is held shared by whoever
	// seals a value and writes it, and exclusively by a rotation, so that
	// nothing sealed under the old key is written after the rotation has gone
	// over the database.
	keyMu sync.RWMutex
	// rawKey is the key aead is made of right now, kept for Snapshot.
	rawKey     []byte
	keyFile    string
	keyFromEnv bool
	inMemory   bool
	log        *slog.Logger
}

// Options configure Open.
type Options struct {
	// EncryptionKey is the 32-byte key environment values are encrypted with
	// before they are written. It may be left empty for ":memory:" only.
	EncryptionKey []byte
	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// KeyFile is where the key is kept in the data directory. A rotation
	// writes the new key there, and Open settles what an interrupted one left
	// next to it. Empty: the key cannot be rotated, except in memory.
	KeyFile string
	// KeyFromEnvironment says that EncryptionKey was given in the environment
	// and KeyFile is not where the agent reads it from: a rotation cannot put
	// the new key where the next start will look for it.
	KeyFromEnvironment bool
}

// Open opens (and creates, if needed) the database at path and applies any
// pending migrations. Use ":memory:" for an ephemeral database in tests.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path != ":memory:" {
		if len(opts.EncryptionKey) == 0 {
			return nil, fmt.Errorf("refusing to open %s without an encryption key", path)
		}
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

	s := &Store{db: db, keyFile: opts.KeyFile, keyFromEnv: opts.KeyFromEnvironment, log: opts.Logger, inMemory: path == ":memory:"}
	if s.log == nil {
		s.log = slog.Default()
	}
	if len(opts.EncryptionKey) > 0 {
		aead, err := newAEAD(opts.EncryptionKey)
		if err != nil {
			db.Close()
			return nil, err
		}
		s.aead = &keyring{current: aead}
		s.rawKey = opts.EncryptionKey
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.settleRotation(ctx); err != nil {
		db.Close()
		return nil, err
	}
	n, err := s.encryptLegacyEnv(ctx, path)
	if err != nil {
		db.Close()
		return nil, err
	}
	if n > 0 {
		log := opts.Logger
		if log == nil {
			log = slog.Default()
		}
		log.Info("encrypted the environment values of deployments written before encryption existed", "deployments", n)
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
	// 4: API tokens with roles. The token the agent is configured with is not
	// in here: it is checked before the table is consulted.
	`CREATE TABLE tokens (
		id           INTEGER PRIMARY KEY,
		name         TEXT NOT NULL UNIQUE,
		role         TEXT NOT NULL,
		hash         BLOB NOT NULL UNIQUE,
		created_at   TEXT NOT NULL,
		last_used_at TEXT
	);`,
	// 5: the name of the token that started each deployment; '' for
	// deployments recorded before tokens had names.
	`ALTER TABLE deployments ADD COLUMN actor TEXT NOT NULL DEFAULT '';`,
	// 6: resource usage of every running replica, sampled periodically for
	// the metrics history and pruned after the retention period.
	`CREATE TABLE metric_samples (
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		replica        INTEGER NOT NULL,
		at             TEXT NOT NULL,
		cpu_percent    REAL NOT NULL,
		memory_bytes   INTEGER NOT NULL
	);
	CREATE INDEX metric_samples_application_at ON metric_samples(application_id, at);`,
	// 7: runs of one-off containers — the pre-deploy hook, scheduled jobs,
	// `shipwick run`. AUTOINCREMENT: the run id is part of the container's
	// name, and a pruned run's id must not come back for a container that
	// may still be around.
	`CREATE TABLE job_runs (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		deployment_id  INTEGER REFERENCES deployments(id) ON DELETE SET NULL,
		job            TEXT NOT NULL,
		kind           TEXT NOT NULL,
		command        TEXT NOT NULL,
		status         TEXT NOT NULL,
		exit_code      INTEGER,
		output         TEXT NOT NULL DEFAULT '',
		started_at     TEXT NOT NULL,
		finished_at    TEXT
	);
	CREATE INDEX job_runs_job ON job_runs(application_id, job, id);`,
	// 8: secrets kept on the server, for ${NAME} in env values. The value is
	// encrypted like an env value is (crypto.go), with the name bound to it.
	`CREATE TABLE secrets (
		name       TEXT PRIMARY KEY,
		value      BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// 9: what a static application's deployment serves: the digest of the
	// uploaded archive, which names its directory in the proxy, and the count
	// and size of the files in it. Empty and zero for deployments that run
	// containers.
	`ALTER TABLE deployments ADD COLUMN static_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE deployments ADD COLUMN static_files INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE deployments ADD COLUMN static_bytes INTEGER NOT NULL DEFAULT 0;`,
	// 10: credentials for the registries images are pulled from. The password
	// is encrypted like a secret is, bound to the registry it is for.
	`CREATE TABLE registries (
		registry   TEXT PRIMARY KEY,
		username   TEXT NOT NULL,
		password   BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	// 11: certificates the operator supplied, by the hostname each is stored
	// under. The chain is public; the key is encrypted like a secret's value
	// (crypto.go), with the hostname bound to it.
	`CREATE TABLE certificates (
		hostname    TEXT PRIMARY KEY,
		certificate TEXT NOT NULL,
		key         BLOB NOT NULL,
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	);`,
	// 12: what the proxy's access log says about each application, one row
	// per application and minute with traffic, pruned after the retention
	// period like the metric samples. latency is a histogram: the counts of
	// its buckets, comma separated (see deploy/traffic.go).
	`CREATE TABLE traffic_samples (
		application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		at             TEXT NOT NULL,
		requests       INTEGER NOT NULL,
		status_2xx     INTEGER NOT NULL,
		status_3xx     INTEGER NOT NULL,
		status_4xx     INTEGER NOT NULL,
		status_5xx     INTEGER NOT NULL,
		bytes          INTEGER NOT NULL,
		latency        TEXT NOT NULL
	);
	CREATE INDEX traffic_samples_application_at ON traffic_samples(application_id, at);`,
	// 13: backups taken by the agent: of an application's volumes, or of the
	// agent's own state (application '_agent'). The application is kept by
	// name, not by reference: like its volumes, an application's backups
	// outlive its deletion. AUTOINCREMENT: the run id names the backup's
	// directory, and a removed run's id must not come back for another's
	// files.
	`CREATE TABLE backup_runs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		application   TEXT NOT NULL,
		trigger       TEXT NOT NULL,
		status        TEXT NOT NULL,
		volumes       TEXT NOT NULL DEFAULT '[]',
		destinations  TEXT NOT NULL DEFAULT '[]',
		encrypted     INTEGER NOT NULL DEFAULT 0,
		error         TEXT NOT NULL DEFAULT '',
		started_at    TEXT NOT NULL,
		completed_at  TEXT,
		activity      TEXT NOT NULL DEFAULT '',
		verified_at   TEXT,
		verify_error  TEXT NOT NULL DEFAULT '',
		verify_output TEXT NOT NULL DEFAULT '',
		restored_at   TEXT,
		restore_error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX backup_runs_application ON backup_runs(application, id);

	-- One row: the name this installation marks its bucket with. It lives
	-- here so that it comes back with a restored database.
	CREATE TABLE backup_installation (
		id TEXT NOT NULL
	);`,
	// 14: what the agent must remember across its own restarts about imports
	// and a promotion: the import that ran last, the export a standby imported
	// last, the promotion that runs or ran last. One row each, as JSON: they
	// are read whole and by nothing but the agent (see standby.go). And the
	// deployments that are to exist without being started, which an agent
	// that resumes one has to know.
	`CREATE TABLE transfer_state (
		name       TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	ALTER TABLE deployments ADD COLUMN dormant INTEGER NOT NULL DEFAULT 0;`,
	// 15: a token may be limited to applications (a JSON list of names; '[]':
	// not limited) and may expire. Tokens from before have neither.
	`ALTER TABLE tokens ADD COLUMN applications TEXT NOT NULL DEFAULT '[]';
	 ALTER TABLE tokens ADD COLUMN expires_at TEXT;`,
	// 16: the audit trail: one row per request that changes something, by
	// whom, from where, on what and how it was answered. The application
	// and the actor are kept by name, not by reference: the trail outlives
	// both. Pruned by age and by count (audit.go).
	`CREATE TABLE audit_log (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		at            TEXT NOT NULL,
		actor_kind    TEXT NOT NULL,
		actor         TEXT NOT NULL,
		address       TEXT NOT NULL,
		forwarded_for TEXT NOT NULL DEFAULT '',
		action        TEXT NOT NULL,
		application   TEXT NOT NULL DEFAULT '',
		target        TEXT NOT NULL DEFAULT '',
		outcome       TEXT NOT NULL,
		status        INTEGER NOT NULL,
		code          TEXT NOT NULL DEFAULT '',
		detail        TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_log_application ON audit_log(application, id);
	CREATE INDEX audit_log_actor ON audit_log(actor, id);
	CREATE INDEX audit_log_at ON audit_log(at);`,
	// 17: who may sign in, and as what: a role for an e-mail address, a
	// group of the provider's or a whole domain. applications is a JSON list
	// as on tokens. created_by is a name, not a reference: the rule outlives
	// the token or the person that made it.
	`CREATE TABLE access_rules (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		kind         TEXT NOT NULL,
		subject      TEXT NOT NULL,
		role         TEXT NOT NULL,
		applications TEXT NOT NULL DEFAULT '[]',
		created_at   TEXT NOT NULL,
		created_by   TEXT NOT NULL DEFAULT '',
		UNIQUE (kind, subject)
	);`,
	// 18: the sessions of people who signed in. hash is the SHA-256 of the
	// session's value in hex, as tokens keep theirs; the value is not stored.
	// groups, role and applications are what the provider said and what the
	// rules gave at the sign-in: each request compares them with what the
	// rules give now. A session that was ended keeps its row until it would
	// have expired, so that its holder is told why.
	`CREATE TABLE sessions (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		hash         TEXT NOT NULL UNIQUE,
		email        TEXT NOT NULL,
		groups       TEXT NOT NULL DEFAULT '[]',
		role         TEXT NOT NULL,
		applications TEXT NOT NULL DEFAULT '[]',
		created_at   TEXT NOT NULL,
		expires_at   TEXT NOT NULL,
		last_used_at TEXT,
		ended_at     TEXT,
		ended_reason TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX sessions_email ON sessions(email);
	CREATE INDEX sessions_expires_at ON sessions(expires_at);`,
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
