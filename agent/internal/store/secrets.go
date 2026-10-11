package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// A secret belongs to the whole server or to one application. An
// application's own is what its ${NAME} resolves to first; the server's is
// what every application falls back to. Both are rows of one table, told
// apart by the application column ('' for the server), because they are one
// kind of thing asked for with one question: the engine names a secret and
// gets the nearest value.

// ErrTooManySecrets is returned by SetSecret when a new secret would exceed
// api.MaxSecrets.
var ErrTooManySecrets = fmt.Errorf("at most %d secrets can be stored; remove one first", api.MaxSecrets)

// Secret is a stored secret without its value: what is listed.
type Secret struct {
	// Application is the application the secret belongs to; "" for a secret
	// of the whole server.
	Application string
	Name        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ResolvedSecret is a value GetSecrets found, and whose it is.
type ResolvedSecret struct {
	Value string
	// Application is the application whose own secret answered; "" when the
	// server's did.
	Application string
}

// secretLabel is what a secret's value is sealed under (crypto.go) and the
// label column of its row. A server-wide secret is bound to its name, as every
// secret was before applications had secrets of their own; one of an
// application to a label that names both, so that a ciphertext moved to
// another application's row of the same name does not open there.
func secretLabel(application, name string) string {
	if application == "" {
		return name
	}
	return "applications/" + application + "/secrets/" + name
}

// secretName is how an error names a secret.
func secretName(application, name string) string {
	if application == "" {
		return name
	}
	return name + " of " + application
}

// SetSecret stores a value under name for the application, or for the whole
// server when application is "", replacing the one there. The value is sealed
// like an env value, with secretLabel as the additional data.
func (s *Store) SetSecret(ctx context.Context, application, name, value string, now time.Time) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	label := secretLabel(application, name)
	stored := value
	if s.aead != nil {
		var err error
		if stored, err = seal(s.aead, label, value); err != nil {
			return fmt.Errorf("encrypt secret %s: %w", secretName(application, name), err)
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE NOT (application = ? AND name = ?)`, application, name).Scan(&others); err != nil {
			return err
		}
		if others >= api.MaxSecrets {
			return ErrTooManySecrets
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO secrets (application, name, label, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(application, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			application, name, label, []byte(stored), formatTime(now), formatTime(now))
		return err
	})
	if errors.Is(err, ErrTooManySecrets) {
		return err
	} else if err != nil {
		return fmt.Errorf("store secret %s: %w", secretName(application, name), err)
	}
	return nil
}

// DeleteSecret removes the application's secret, or the server's when
// application is "". A secret of the same name in the other scope stays.
func (s *Store) DeleteSecret(ctx context.Context, application, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE application = ? AND name = ?`, application, name)
	if err != nil {
		return fmt.Errorf("delete secret %s: %w", secretName(application, name), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSecrets returns the names and timestamps in name order, a server-wide
// secret before the applications' of the same name. Values never leave the
// store this way; the engine asks for them by name.
func (s *Store) ListSecrets(ctx context.Context) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT application, name, created_at, updated_at FROM secrets ORDER BY name, application`)
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
		if err := rows.Scan(&sec.Application, &sec.Name, &created, &updated); err != nil {
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

// GetSecrets returns the values of the named secrets as the application sees
// them: its own where it has one, the server's otherwise. With application ""
// only the server's are looked at. A name neither has is absent from the
// result; it is the caller's to report.
func (s *Store) GetSecrets(ctx context.Context, application string, names []string) (map[string]ResolvedSecret, error) {
	values := make(map[string]ResolvedSecret, len(names))
	for _, name := range names {
		var (
			owner  string
			stored []byte
		)
		// '' sorts before every application name, so where there are two
		// rows the application's own comes first.
		err := s.db.QueryRowContext(ctx,
			`SELECT application, value FROM secrets WHERE name = ? AND application IN (?, '') ORDER BY application DESC LIMIT 1`,
			name, application).Scan(&owner, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("read secret %s: %w", name, err)
		}
		value := string(stored)
		if s.aead != nil {
			if !isSealed(value) {
				return nil, fmt.Errorf("secret %s is stored unencrypted", secretName(owner, name))
			}
			if value, err = open(s.aead, secretLabel(owner, name), value); err != nil {
				return nil, err
			}
		}
		values[name] = ResolvedSecret{Value: value, Application: owner}
	}
	return values, nil
}
