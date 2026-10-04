package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// UpdateToken changes the applications a token is limited to, its end, or
// both. change is handed the token as it is stored and returns it as it is
// to be; an error of its own stops the update and comes back unwrapped. Both
// happen in one transaction, so that what change judged is what is replaced.
// The role, the name and the hash are not written, whatever change did to
// them.
func (s *Store) UpdateToken(ctx context.Context, name string, change func(Token) (Token, error)) (before, after Token, err error) {
	var refused error
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if before, err = scanToken(tx.QueryRowContext(ctx, tokenSelect+` WHERE name = ?`, name)); err != nil {
			return err
		}
		// change gets a list of its own: before is what the caller compares with.
		current := before
		current.Applications = append([]string{}, before.Applications...)
		if after, refused = change(current); refused != nil {
			return refused
		}
		if after.Applications == nil {
			after.Applications = []string{}
		}
		applications, err := json.Marshal(after.Applications)
		if err != nil {
			return err
		}
		var expires sql.NullString
		if after.ExpiresAt != nil {
			at := after.ExpiresAt.UTC()
			after.ExpiresAt = &at
			expires = sql.NullString{String: formatTime(at), Valid: true}
		}
		after.ID, after.Name, after.Role, after.Hash = before.ID, before.Name, before.Role, before.Hash
		_, err = tx.ExecContext(ctx, `UPDATE tokens SET applications = ?, expires_at = ? WHERE id = ?`, string(applications), expires, before.ID)
		return err
	})
	switch {
	case err == nil:
		return before, after, nil
	case refused != nil && errors.Is(err, refused), errors.Is(err, ErrNotFound):
		return Token{}, Token{}, err
	}
	return Token{}, Token{}, fmt.Errorf("update token: %w", err)
}
