package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrTooManySecrets is returned by SetSecret when a new name would exceed
// api.MaxSecrets.
var ErrTooManySecrets = fmt.Errorf("at most %d secrets can be stored; remove one first", api.MaxSecrets)

// Secret is a stored secret without its value: what is listed.
type Secret struct {
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SetSecret stores a value under name, replacing the one there. The value is
// sealed like an env value, with the name as the additional data: a
// ciphertext moved to another name does not decrypt there.
func (s *Store) SetSecret(ctx context.Context, name, value string, now time.Time) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	stored := value
	if s.aead != nil {
		var err error
		if stored, err = seal(s.aead, name, value); err != nil {
			return fmt.Errorf("encrypt secret %s: %w", name, err)
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE name <> ?`, name).Scan(&others); err != nil {
			return err
		}
		if others >= api.MaxSecrets {
			return ErrTooManySecrets
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO secrets (name, value, created_at, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			name, []byte(stored), formatTime(now), formatTime(now))
		return err
	})
	if errors.Is(err, ErrTooManySecrets) {
		return err
	} else if err != nil {
		return fmt.Errorf("store secret %s: %w", name, err)
	}
	return nil
}

func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSecrets returns the names and timestamps, in name order. Values never
// leave the store this way; the engine asks for them by name.
func (s *Store) ListSecrets(ctx context.Context) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, created_at, updated_at FROM secrets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var out []Secret
	for rows.Next() {
		var (
			sec              Secret
			created, updated string
		)
		if err := rows.Scan(&sec.Name, &created, &updated); err != nil {
			return nil, err
		}
		if sec.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if sec.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// GetSecrets returns the values of the named secrets, for the engine to fill
// into env values. A name that is not stored is absent from the result; it is
// the caller's to report.
func (s *Store) GetSecrets(ctx context.Context, names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		var stored []byte
		err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = ?`, name).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("read secret %s: %w", name, err)
		}
		value := string(stored)
		if s.aead != nil {
			if !isSealed(value) {
				return nil, fmt.Errorf("secret %s is stored unencrypted", name)
			}
			if value, err = open(s.aead, name, value); err != nil {
				return nil, err
			}
		}
		values[name] = value
	}
	return values, nil
}
