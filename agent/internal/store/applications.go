package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

const applicationSelect = `
	SELECT id, name, desired_state, active_deployment_id, created_at, updated_at FROM applications`

func scanApplication(row rowScanner) (Application, error) {
	var (
		a                Application
		active           sql.NullInt64
		created, updated string
	)
	err := row.Scan(&a.ID, &a.Name, &a.DesiredState, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, ErrNotFound
	} else if err != nil {
		return Application{}, err
	}
	if active.Valid {
		a.ActiveDeploymentID = &active.Int64
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return Application{}, err
	}
	if a.UpdatedAt, err = parseTime(updated); err != nil {
		return Application{}, err
	}
	return a, nil
}

func (s *Store) GetApplication(ctx context.Context, name string) (Application, error) {
	return scanApplication(s.db.QueryRowContext(ctx, applicationSelect+` WHERE name = ?`, name))
}

func (s *Store) ListApplications(ctx context.Context) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, applicationSelect+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer rows.Close()

	var out []Application
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetDesiredState(ctx context.Context, appID int64, state string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE applications SET desired_state = ?, updated_at = ? WHERE id = ?`,
		state, formatTime(now), appID)
	if err != nil {
		return fmt.Errorf("set desired state: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteApplication removes an application together with its deployment
// history, replicas and events (via ON DELETE CASCADE).
func (s *Store) DeleteApplication(ctx context.Context, appID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM applications WHERE id = ?`, appID)
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddEvent(ctx context.Context, appID int64, deploymentID *int64, level, typ, message string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (application_id, deployment_id, level, type, message, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		appID, deploymentID, level, typ, message, formatTime(now))
	if err != nil {
		return fmt.Errorf("record event: %w", err)
	}
	return nil
}

// ListDeploymentEvents returns the events of one deployment, oldest first.
func (s *Store) ListDeploymentEvents(ctx context.Context, deploymentID int64) ([]api.Event, error) {
	return s.listEvents(ctx,
		`SELECT id, deployment_id, level, type, message, created_at FROM events
		 WHERE deployment_id = ? ORDER BY id`, deploymentID)
}

// ListApplicationEvents returns the most recent events that belong to the
// application itself rather than to one of its deployments — what the
// supervisor did, stops and starts — newest first.
func (s *Store) ListApplicationEvents(ctx context.Context, appID int64, limit int) ([]api.Event, error) {
	return s.listEvents(ctx,
		`SELECT id, deployment_id, level, type, message, created_at FROM events
		 WHERE application_id = ? AND deployment_id IS NULL ORDER BY id DESC LIMIT ?`, appID, limit)
}

// PruneApplicationEvents keeps only the newest `keep` application-level
// events. A crash-looping application produces them indefinitely; deployment
// events are bounded by nature and are never pruned.
func (s *Store) PruneApplicationEvents(ctx context.Context, appID int64, keep int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM events WHERE application_id = ? AND deployment_id IS NULL AND id NOT IN (
			SELECT id FROM events WHERE application_id = ? AND deployment_id IS NULL ORDER BY id DESC LIMIT ?)`,
		appID, appID, keep)
	if err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	return nil
}

func (s *Store) listEvents(ctx context.Context, query string, args ...any) ([]api.Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	out := []api.Event{}
	for rows.Next() {
		var (
			e       api.Event
			depID   sql.NullInt64
			created string
		)
		if err := rows.Scan(&e.ID, &depID, &e.Level, &e.Type, &e.Message, &created); err != nil {
			return nil, err
		}
		if depID.Valid {
			e.DeploymentID = &depID.Int64
		}
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
