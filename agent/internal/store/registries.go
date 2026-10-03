package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrTooManyRegistries is returned by SetRegistry when a new registry would
// exceed api.MaxRegistries.
var ErrTooManyRegistries = fmt.Errorf("at most %d registries can be stored; remove one first", api.MaxRegistries)

// Registry is a stored registry credential without its password: what is
// listed.
type Registry struct {
	Registry  string
	Username  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// registryPasswordName is what a registry's password is bound to when it is
// sealed. The prefix keeps it apart from a secret or a variable that happens
// to be named like a registry.
func registryPasswordName(registry string) string {
	return "registry:" + registry
}

// SetRegistry stores the credential for a registry, replacing the one there.
// The password is sealed like a secret; the username is not one.
func (s *Store) SetRegistry(ctx context.Context, registry, username, password string, now time.Time) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()

	stored := password
	if s.aead != nil {
		var err error
		if stored, err = seal(s.aead, registryPasswordName(registry), password); err != nil {
			return fmt.Errorf("encrypt the password for %s: %w", registry, err)
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM registries WHERE registry <> ?`, registry).Scan(&others); err != nil {
			return err
		}
		if others >= api.MaxRegistries {
			return ErrTooManyRegistries
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO registries (registry, username, password, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(registry) DO UPDATE SET username = excluded.username, password = excluded.password, updated_at = excluded.updated_at`,
			registry, username, []byte(stored), formatTime(now), formatTime(now))
		return err
	})
	if errors.Is(err, ErrTooManyRegistries) {
		return err
	} else if err != nil {
		return fmt.Errorf("store the credential for %s: %w", registry, err)
	}
	return nil
}

func (s *Store) DeleteRegistry(ctx context.Context, registry string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE registry = ?`, registry)
	if err != nil {
		return fmt.Errorf("delete the credential for %s: %w", registry, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRegistries returns the registries with their usernames, in name order.
// Passwords never leave the store this way; the engine asks for one by
// registry when it pulls.
func (s *Store) ListRegistries(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT registry, username, created_at, updated_at FROM registries ORDER BY registry`)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	defer rows.Close()

	var out []Registry
	for rows.Next() {
		var (
			reg              Registry
			created, updated string
		)
		if err := rows.Scan(&reg.Registry, &reg.Username, &created, &updated); err != nil {
			return nil, err
		}
		if reg.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if reg.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// RegistryCredential returns the username and password stored for a
// registry; found is false when there is none.
func (s *Store) RegistryCredential(ctx context.Context, registry string) (username, password string, found bool, err error) {
	var stored []byte
	err = s.db.QueryRowContext(ctx, `SELECT username, password FROM registries WHERE registry = ?`, registry).Scan(&username, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	} else if err != nil {
		return "", "", false, fmt.Errorf("read the credential for %s: %w", registry, err)
	}
	password = string(stored)
	if s.aead != nil {
		if !isSealed(password) {
			return "", "", false, fmt.Errorf("the password for %s is stored unencrypted", registry)
		}
		if password, err = open(s.aead, registryPasswordName(registry), password); err != nil {
			return "", "", false, err
		}
	}
	return username, password, true, nil
}
