package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

type Application struct {
	ID                 int64
	Name               string
	DesiredState       string
	ActiveDeploymentID *int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Deployment is an immutable record of one deployment attempt. Only its
// status, error and completion time ever change.
type Deployment struct {
	ID            int64
	ApplicationID int64
	Application   string
	Sequence      int
	Version       string
	Image         string
	Spec          spec.App
	Status        api.DeploymentStatus
	Error         string
	StartedAt     time.Time
	CompletedAt   *time.Time
	// Kind is api.KindDeploy, KindRedeploy or KindRollback; SourceID names
	// the deployment whose stored spec a redeploy or rollback re-used.
	Kind     string
	SourceID *int64
}

type Replica struct {
	DeploymentID  int64
	Index         int
	ContainerID   string
	ContainerName string
	// Restarts counts supervisor-initiated restarts of this container.
	Restarts int
}

// CreateDeployment registers the application if it is new and appends a
// PENDING deployment with the next sequence number.
func (s *Store) CreateDeployment(ctx context.Context, app spec.App, now time.Time) (Deployment, error) {
	return s.CreateDeploymentFrom(ctx, app, api.KindDeploy, nil, now)
}

// CreateDeploymentFrom is CreateDeployment for a deployment that re-uses the
// stored spec of an earlier one: a redeploy or a rollback.
func (s *Store) CreateDeploymentFrom(ctx context.Context, app spec.App, kind string, sourceID *int64, now time.Time) (Deployment, error) {
	specJSON, err := json.Marshal(app)
	if err != nil {
		return Deployment{}, fmt.Errorf("encode spec: %w", err)
	}

	d := Deployment{
		Application: app.Name,
		Version:     app.Version(),
		Image:       app.Image,
		Spec:        app,
		Status:      api.StatusPending,
		StartedAt:   now.UTC(),
		Kind:        kind,
		SourceID:    sourceID,
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		ts := formatTime(now)
		_, err := tx.ExecContext(ctx,
			`INSERT INTO applications (name, created_at, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT (name) DO UPDATE SET updated_at = excluded.updated_at`,
			app.Name, ts, ts)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM applications WHERE name = ?`, app.Name).Scan(&d.ApplicationID); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) + 1 FROM deployments WHERE application_id = ?`,
			d.ApplicationID).Scan(&d.Sequence)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (application_id, sequence, version, image, spec, status, started_at, kind, source_deployment_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ApplicationID, d.Sequence, d.Version, d.Image, string(specJSON), string(d.Status), ts, kind, sourceID)
		if err != nil {
			return err
		}
		d.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	return d, nil
}

// TransitionDeployment moves a deployment from one status to another. It is a
// compare-and-swap: if the deployment is not currently in `from`, nothing
// changes and ErrConflict is returned. Whether the transition is legal is the
// state machine's concern, not the store's.
func (s *Store) TransitionDeployment(ctx context.Context, id int64, from, to api.DeploymentStatus, errMsg string) error {
	// An empty errMsg keeps the recorded error: a failed deployment goes on
	// through ROLLBACK and RESTORING, and why it failed must survive that.
	res, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET status = ?, error = CASE WHEN ? = '' THEN error ELSE ? END
		 WHERE id = ? AND status = ?`,
		string(to), errMsg, errMsg, id, string(from))
	if err != nil {
		return fmt.Errorf("transition deployment %d: %w", id, err)
	}
	return expectOneRow(res)
}

// CompleteDeployment stamps the completion time of a deployment that does not
// have one yet. It is idempotent.
func (s *Store) CompleteDeployment(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET completed_at = ? WHERE id = ? AND completed_at IS NULL`,
		formatTime(now), id)
	if err != nil {
		return fmt.Errorf("complete deployment %d: %w", id, err)
	}
	return nil
}

// CompleteAllDeployments stamps every deployment that lacks a completion
// time. Only meaningful at startup, once interrupted deployments have been
// settled and nothing can be in flight.
func (s *Store) CompleteAllDeployments(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET completed_at = ? WHERE completed_at IS NULL`, formatTime(now))
	if err != nil {
		return fmt.Errorf("complete deployments: %w", err)
	}
	return nil
}

// ActivateDeployment atomically promotes a HEALTHY deployment to ACTIVE,
// supersedes the previously active one and repoints the application. It
// returns the superseded deployment, if there was one.
//
// This is the commit point of a deployment. It deliberately leaves
// completed_at unset: the engine still has the old version's containers to
// retire, and calls CompleteDeployment once that is done.
func (s *Store) ActivateDeployment(ctx context.Context, id int64, now time.Time) (previous *Deployment, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var appID int64
		var prevID sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT a.id, a.active_deployment_id FROM deployments d
			 JOIN applications a ON a.id = d.application_id WHERE d.id = ?`, id).Scan(&appID, &prevID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}

		ts := formatTime(now)
		res, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = ? WHERE id = ? AND status = ?`,
			string(api.StatusActive), id, string(api.StatusHealthy))
		if err != nil {
			return err
		}
		if err := expectOneRow(res); err != nil {
			return err
		}

		if prevID.Valid {
			_, err = tx.ExecContext(ctx,
				`UPDATE deployments SET status = ? WHERE id = ? AND status = ?`,
				string(api.StatusSuperseded), prevID.Int64, string(api.StatusActive))
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE applications SET active_deployment_id = ?, desired_state = ?, updated_at = ? WHERE id = ?`,
			id, api.DesiredRunning, ts, appID)
		if err != nil {
			return err
		}

		if prevID.Valid {
			p, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelect+` WHERE d.id = ?`, prevID.Int64))
			if err != nil {
				return err
			}
			previous = &p
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("activate deployment %d: %w", id, err)
	}
	return previous, nil
}

const deploymentSelect = `
	SELECT d.id, d.application_id, a.name, d.sequence, d.version, d.image, d.spec,
	       d.status, d.error, d.started_at, d.completed_at, d.kind, d.source_deployment_id
	FROM deployments d JOIN applications a ON a.id = d.application_id`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeployment(row rowScanner) (Deployment, error) {
	var (
		d         Deployment
		specJSON  string
		status    string
		started   string
		completed sql.NullString
		source    sql.NullInt64
	)
	err := row.Scan(&d.ID, &d.ApplicationID, &d.Application, &d.Sequence, &d.Version, &d.Image,
		&specJSON, &status, &d.Error, &started, &completed, &d.Kind, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	} else if err != nil {
		return Deployment{}, err
	}
	d.Status = api.DeploymentStatus(status)
	if source.Valid {
		d.SourceID = &source.Int64
	}
	if err := json.Unmarshal([]byte(specJSON), &d.Spec); err != nil {
		return Deployment{}, fmt.Errorf("decode spec of deployment %d: %w", d.ID, err)
	}
	if d.StartedAt, err = parseTime(started); err != nil {
		return Deployment{}, err
	}
	if d.CompletedAt, err = parseNullTime(completed); err != nil {
		return Deployment{}, err
	}
	return d, nil
}

func (s *Store) GetDeployment(ctx context.Context, id int64) (Deployment, error) {
	return scanDeployment(s.db.QueryRowContext(ctx, deploymentSelect+` WHERE d.id = ?`, id))
}

// DeploymentFilter narrows ListDeployments. Zero values mean "no filter".
type DeploymentFilter struct {
	Application string
	Statuses    []api.DeploymentStatus
	Limit       int
}

// ListDeployments returns deployments, newest first.
func (s *Store) ListDeployments(ctx context.Context, f DeploymentFilter) ([]Deployment, error) {
	query := deploymentSelect
	var where []string
	var args []any
	if f.Application != "" {
		where = append(where, "a.name = ?")
		args = append(args, f.Application)
	}
	if len(f.Statuses) > 0 {
		marks := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			marks[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "d.status IN ("+strings.Join(marks, ", ")+")")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY d.id DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()

	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AddReplica(ctx context.Context, r Replica, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_replicas (deployment_id, replica_index, container_id, container_name, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		r.DeploymentID, r.Index, r.ContainerID, r.ContainerName, formatTime(now))
	if err != nil {
		return fmt.Errorf("record replica: %w", err)
	}
	return nil
}

// MarkReplicaRemoved records that a container no longer exists. Unknown
// container IDs are ignored: removal must stay idempotent.
func (s *Store) MarkReplicaRemoved(ctx context.Context, containerID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployment_replicas SET removed_at = ? WHERE container_id = ? AND removed_at IS NULL`,
		formatTime(now), containerID)
	if err != nil {
		return fmt.Errorf("mark replica removed: %w", err)
	}
	return nil
}

// ListReplicas returns the replicas of a deployment whose containers have not
// been removed.
func (s *Store) ListReplicas(ctx context.Context, deploymentID int64) ([]Replica, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deployment_id, replica_index, container_id, container_name, restart_count FROM deployment_replicas
		 WHERE deployment_id = ? AND removed_at IS NULL ORDER BY replica_index`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("list replicas: %w", err)
	}
	defer rows.Close()

	var out []Replica
	for rows.Next() {
		var r Replica
		if err := rows.Scan(&r.DeploymentID, &r.Index, &r.ContainerID, &r.ContainerName, &r.Restarts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func expectOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

// IncrementReplicaRestarts records one supervisor-initiated restart.
func (s *Store) IncrementReplicaRestarts(ctx context.Context, containerID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployment_replicas SET restart_count = restart_count + 1 WHERE container_id = ? AND removed_at IS NULL`,
		containerID)
	if err != nil {
		return fmt.Errorf("count replica restart: %w", err)
	}
	return nil
}
