package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrTokenExists is returned by CreateToken for a name already in use.
var ErrTokenExists = errors.New("a token with this name already exists")

// Token is a stored API token. Hash is the SHA-256 of the token value; the
// value itself is never stored.
type Token struct {
	ID         int64
	Name       string
	Role       api.Role
	Hash       []byte
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

func (s *Store) CreateToken(ctx context.Context, name string, role api.Role, hash []byte, now time.Time) (Token, error) {
	t := Token{Name: name, Role: role, Hash: hash, CreatedAt: now.UTC()}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrTokenExists
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO tokens (name, role, hash, created_at) VALUES (?, ?, ?, ?)`,
			name, string(role), hash, formatTime(now))
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	if errors.Is(err, ErrTokenExists) {
		return Token{}, err
	} else if err != nil {
		return Token{}, fmt.Errorf("create token: %w", err)
	}
	return t, nil
}

const tokenSelect = `SELECT id, name, role, hash, created_at, last_used_at FROM tokens`

func scanToken(row rowScanner) (Token, error) {
	var (
		t        Token
		role     string
		created  string
		lastUsed sql.NullString
	)
	err := row.Scan(&t.ID, &t.Name, &role, &t.Hash, &created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	} else if err != nil {
		return Token{}, err
	}
	t.Role = api.Role(role)
	if t.CreatedAt, err = parseTime(created); err != nil {
		return Token{}, err
	}
	if t.LastUsedAt, err = parseNullTime(lastUsed); err != nil {
		return Token{}, err
	}
	return t, nil
}

// GetTokenByHash looks a presented token up by the hash of its value. The
// column is unique, so this is one indexed lookup per request.
func (s *Store) GetTokenByHash(ctx context.Context, hash []byte) (Token, error) {
	return scanToken(s.db.QueryRowContext(ctx, tokenSelect+` WHERE hash = ?`, hash))
}

// ListTokens returns every token, oldest first.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, tokenSelect+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToken(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchToken records when the token was last used. A revoked token is
// silently ignored: its last request may still be finishing.
func (s *Store) TouchToken(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("record token use: %w", err)
	}
	return nil
}
