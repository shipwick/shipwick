package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrTooManyAccessRules is returned by GrantAccess when a new rule would
// exceed api.MaxAccessRules.
var ErrTooManyAccessRules = fmt.Errorf("at most %d access rules can be stored; a group or a domain covers many people with one", api.MaxAccessRules)

// GrantAccess stores a rule, replacing the one for the same kind and subject.
// replaced reports whether there was one.
func (s *Store) GrantAccess(ctx context.Context, r api.AccessRule) (rule api.AccessRule, replaced bool, err error) {
	r.CreatedAt = r.CreatedAt.UTC()
	if r.Applications == nil {
		r.Applications = []string{}
	}
	applications, err := json.Marshal(r.Applications)
	if err != nil {
		return api.AccessRule{}, false, fmt.Errorf("grant access: %w", err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM access_rules WHERE kind = ? AND subject = ?`, r.Kind, r.Subject).Scan(&r.ID)
		switch {
		case err == nil:
			replaced = true
			_, err = tx.ExecContext(ctx, `UPDATE access_rules SET role = ?, applications = ?, created_at = ?, created_by = ? WHERE id = ?`,
				string(r.Role), string(applications), formatTime(r.CreatedAt), r.CreatedBy, r.ID)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_rules`).Scan(&n); err != nil {
			return err
		}
		if n >= api.MaxAccessRules {
			return ErrTooManyAccessRules
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO access_rules (kind, subject, role, applications, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
			r.Kind, r.Subject, string(r.Role), string(applications), formatTime(r.CreatedAt), r.CreatedBy)
		if err != nil {
			return err
		}
		r.ID, err = res.LastInsertId()
		return err
	})
	if errors.Is(err, ErrTooManyAccessRules) {
		return api.AccessRule{}, false, err
	} else if err != nil {
		return api.AccessRule{}, false, fmt.Errorf("grant access: %w", err)
	}
	return r, replaced, nil
}

// AccessRules returns every rule, oldest first.
func (s *Store) AccessRules(ctx context.Context) ([]api.AccessRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, subject, role, applications, created_at, created_by FROM access_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list access rules: %w", err)
	}
	defer rows.Close()

	out := []api.AccessRule{}
	for rows.Next() {
		var (
			r              api.AccessRule
			role, apps, at string
		)
		if err := rows.Scan(&r.ID, &r.Kind, &r.Subject, &role, &apps, &at, &r.CreatedBy); err != nil {
			return nil, err
		}
		r.Role = api.Role(role)
		if err := json.Unmarshal([]byte(apps), &r.Applications); err != nil {
			return nil, fmt.Errorf("access rule %d: applications: %w", r.ID, err)
		}
		if r.CreatedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RevokeAccess removes a rule and returns what it was.
func (s *Store) RevokeAccess(ctx context.Context, id int64) (api.AccessRule, error) {
	var r api.AccessRule
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var role string
		err := tx.QueryRowContext(ctx, `SELECT id, kind, subject, role FROM access_rules WHERE id = ?`, id).Scan(&r.ID, &r.Kind, &r.Subject, &role)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		r.Role = api.Role(role)
		_, err = tx.ExecContext(ctx, `DELETE FROM access_rules WHERE id = ?`, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return api.AccessRule{}, err
	} else if err != nil {
		return api.AccessRule{}, fmt.Errorf("revoke access: %w", err)
	}
	return r, nil
}

// Session is a stored session. Hash is the SHA-256 of the session's value;
// the value itself is never stored.
type Session struct {
	ID    int64
	Hash  []byte
	Email string
	// Groups are the ones the provider named at the sign-in; Role and
	// Applications what the rules gave then.
	Groups       []string
	Role         api.Role
	Applications []string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	LastUsedAt   *time.Time
	// EndedAt is set for a session that was ended before its time, and
	// EndedReason says why: one of the api.SessionEnded* reasons.
	EndedAt     *time.Time
	EndedReason string
}

// sessionsKeptFor is how long a session's row outlives the session: long
// enough that whoever still holds it is told it expired rather than that it
// is unknown.
const sessionsKeptFor = 24 * time.Hour

// CreateSession stores a session, and forgets the ones long over.
func (s *Store) CreateSession(ctx context.Context, sess Session) (Session, error) {
	sess.CreatedAt, sess.ExpiresAt = sess.CreatedAt.UTC(), sess.ExpiresAt.UTC()
	if sess.Groups == nil {
		sess.Groups = []string{}
	}
	if sess.Applications == nil {
		sess.Applications = []string{}
	}
	groups, err := json.Marshal(sess.Groups)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	applications, err := json.Marshal(sess.Applications)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (hash, email, groups, role, applications, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			hex.EncodeToString(sess.Hash), sess.Email, string(groups), string(sess.Role), string(applications), formatTime(sess.CreatedAt), formatTime(sess.ExpiresAt))
		if err != nil {
			return err
		}
		if sess.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, formatTime(sess.CreatedAt.Add(-sessionsKeptFor)))
		return err
	})
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

const sessionSelect = `SELECT id, hash, email, groups, role, applications, created_at, expires_at, last_used_at, ended_at, ended_reason FROM sessions`

func scanSession(row rowScanner) (Session, error) {
	var (
		sess                     Session
		hash, groups, role, apps string
		created, expires         string
		lastUsed, ended          sql.NullString
	)
	err := row.Scan(&sess.ID, &hash, &sess.Email, &groups, &role, &apps, &created, &expires, &lastUsed, &ended, &sess.EndedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	} else if err != nil {
		return Session{}, err
	}
	sess.Role = api.Role(role)
	if sess.Hash, err = hex.DecodeString(hash); err != nil {
		return Session{}, fmt.Errorf("session %d: hash: %w", sess.ID, err)
	}
	if err := json.Unmarshal([]byte(groups), &sess.Groups); err != nil {
		return Session{}, fmt.Errorf("session %d: groups: %w", sess.ID, err)
	}
	if err := json.Unmarshal([]byte(apps), &sess.Applications); err != nil {
		return Session{}, fmt.Errorf("session %d: applications: %w", sess.ID, err)
	}
	if sess.CreatedAt, err = parseTime(created); err != nil {
		return Session{}, err
	}
	if sess.ExpiresAt, err = parseTime(expires); err != nil {
		return Session{}, err
	}
	if sess.LastUsedAt, err = parseNullTime(lastUsed); err != nil {
		return Session{}, err
	}
	if sess.EndedAt, err = parseNullTime(ended); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// GetSessionByHash looks a presented session up by the hash of its value,
// as GetTokenByHash does for a token.
func (s *Store) GetSessionByHash(ctx context.Context, hash []byte) (Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE hash = ?`, hex.EncodeToString(hash)))
}

// ActiveSessions returns the sessions that work at now, oldest first.
func (s *Store) ActiveSessions(ctx context.Context, now time.Time) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE ended_at IS NULL AND expires_at > ? ORDER BY id`, formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// EndSession ends one session, unless it was ended before: the first reason
// stands.
func (s *Store) EndSession(ctx context.Context, id int64, reason string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, ended_reason = ? WHERE id = ? AND ended_at IS NULL`, formatTime(now), reason, id)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// EndSessionsOf ends every session of a person that still works at now, and
// returns how many there were.
func (s *Store) EndSessionsOf(ctx context.Context, email, reason string, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, ended_reason = ? WHERE email = ? AND ended_at IS NULL AND expires_at > ?`,
		formatTime(now), reason, email, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("end sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteSession forgets a session: what its holder asks for by signing out.
func (s *Store) DeleteSession(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// TouchSession records when the session was last used.
func (s *Store) TouchSession(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ? WHERE id = ?`, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("record session use: %w", err)
	}
	return nil
}
