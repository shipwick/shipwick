package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The names under which the engine keeps what it remembers about imports and
// a promotion (deploy/import.go, deploy/promotion.go).
const (
	TransferImport    = "import"
	TransferPull      = "pull"
	TransferPromotion = "promotion"
)

// SetTransferState keeps v, as JSON, under name. None of it is secret: the
// records hold names, statuses and messages.
func (s *Store) SetTransferState(ctx context.Context, name string, v any, now time.Time) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s state: %w", name, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO transfer_state (name, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		name, string(data), formatTime(now))
	if err != nil {
		return fmt.Errorf("store %s state: %w", name, err)
	}
	return nil
}

// TransferState reads what was kept under name into v; found is false when
// nothing was.
func (s *Store) TransferState(ctx context.Context, name string, v any) (found bool, err error) {
	var data string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM transfer_state WHERE name = ?`, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("read %s state: %w", name, err)
	}
	if err := json.Unmarshal([]byte(data), v); err != nil {
		return false, fmt.Errorf("decode %s state: %w", name, err)
	}
	return true, nil
}

// MarkDeploymentDormant records that the deployment is to exist without
// running. It is said once, before the deployment begins, and never taken
// back: like its kind, it is how the deployment came to be.
func (s *Store) MarkDeploymentDormant(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE deployments SET dormant = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark deployment %d dormant: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeploymentDormant reports whether the deployment was recorded as one that
// is not to be started.
func (s *Store) DeploymentDormant(ctx context.Context, id int64) (bool, error) {
	var dormant bool
	err := s.db.QueryRowContext(ctx, `SELECT dormant FROM deployments WHERE id = ?`, id).Scan(&dormant)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("read deployment %d: %w", id, err)
	}
	return dormant, nil
}
