package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// JobRun is one execution of a one-off container: the pre-deploy hook of a
// deployment, a scheduled job, or a command started by hand. Once finished it
// never changes.
type JobRun struct {
	ID            int64
	ApplicationID int64
	Application   string
	// DeploymentID is the deployment whose image and environment the run
	// used; nil once that deployment's record is gone.
	DeploymentID *int64
	Job          string // the job's name; "pre-deploy" for the hook, "run" for an ad-hoc command
	Kind         string // api.RunKindHook, RunKindScheduled or RunKindManual
	Command      []string
	Status       api.RunStatus
	ExitCode     *int
	// Output is the tail of the container's output, filled in when the run
	// finishes. ListJobRuns leaves it empty: it is the bulk of a row.
	Output     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// CreateJobRun records a run that is starting now.
func (s *Store) CreateJobRun(ctx context.Context, run JobRun, now time.Time) (JobRun, error) {
	command, err := json.Marshal(run.Command)
	if err != nil {
		return JobRun{}, fmt.Errorf("encode command: %w", err)
	}
	run.Status = api.RunRunning
	run.StartedAt = now.UTC()
	run.ExitCode, run.FinishedAt, run.Output = nil, nil, ""
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO job_runs (application_id, deployment_id, job, kind, command, status, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.ApplicationID, run.DeploymentID, run.Job, run.Kind, string(command), string(run.Status), formatTime(now))
	if err != nil {
		return JobRun{}, fmt.Errorf("record job run: %w", err)
	}
	if run.ID, err = res.LastInsertId(); err != nil {
		return JobRun{}, fmt.Errorf("record job run: %w", err)
	}
	if run.Application == "" {
		err = s.db.QueryRowContext(ctx, `SELECT name FROM applications WHERE id = ?`, run.ApplicationID).Scan(&run.Application)
		if err != nil {
			return JobRun{}, fmt.Errorf("record job run: %w", err)
		}
	}
	return run, nil
}

// FinishJobRun records how a run ended. A run whose application has been
// deleted in the meantime is gone with it, which is not an error.
func (s *Store) FinishJobRun(ctx context.Context, id int64, status api.RunStatus, exitCode *int, output string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE job_runs SET status = ?, exit_code = ?, output = ?, finished_at = ? WHERE id = ? AND status = ?`,
		string(status), exitCode, output, formatTime(now), id, string(api.RunRunning))
	if err != nil {
		return fmt.Errorf("finish job run %d: %w", id, err)
	}
	return nil
}

// MarkJobRunsInterrupted ends every run still marked running. Only meaningful
// at startup: the processes that were waiting on them died with the old agent,
// and Recover removes their containers.
func (s *Store) MarkJobRunsInterrupted(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE job_runs SET status = ?, finished_at = ? WHERE status = ?`,
		string(api.RunInterrupted), formatTime(now), string(api.RunRunning))
	if err != nil {
		return 0, fmt.Errorf("mark job runs interrupted: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

const jobRunSelect = `
	SELECT r.id, r.application_id, a.name, r.deployment_id, r.job, r.kind, r.command, r.status, r.exit_code, %s, r.started_at, r.finished_at
	FROM job_runs r JOIN applications a ON a.id = r.application_id`

func scanJobRun(row rowScanner) (JobRun, error) {
	var (
		r          JobRun
		deployment sql.NullInt64
		command    string
		status     string
		exitCode   sql.NullInt64
		started    string
		finished   sql.NullString
	)
	err := row.Scan(&r.ID, &r.ApplicationID, &r.Application, &deployment, &r.Job, &r.Kind, &command, &status, &exitCode, &r.Output, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return JobRun{}, ErrNotFound
	} else if err != nil {
		return JobRun{}, err
	}
	if deployment.Valid {
		r.DeploymentID = &deployment.Int64
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		r.ExitCode = &code
	}
	r.Status = api.RunStatus(status)
	if err := json.Unmarshal([]byte(command), &r.Command); err != nil {
		return JobRun{}, fmt.Errorf("decode command of job run %d: %w", r.ID, err)
	}
	if r.StartedAt, err = parseTime(started); err != nil {
		return JobRun{}, err
	}
	if r.FinishedAt, err = parseNullTime(finished); err != nil {
		return JobRun{}, err
	}
	return r, nil
}

// GetJobRun returns one run with its output.
func (s *Store) GetJobRun(ctx context.Context, id int64) (JobRun, error) {
	return scanJobRun(s.db.QueryRowContext(ctx, fmt.Sprintf(jobRunSelect, "r.output")+` WHERE r.id = ?`, id))
}

// ListJobRuns returns the runs of an application, newest first and without
// their output; job narrows them to one job.
func (s *Store) ListJobRuns(ctx context.Context, appID int64, job string, limit int) ([]JobRun, error) {
	query := fmt.Sprintf(jobRunSelect, "''") + ` WHERE r.application_id = ?`
	args := []any{appID}
	if job != "" {
		query += ` AND r.job = ?`
		args = append(args, job)
	}
	query += ` ORDER BY r.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.listJobRuns(ctx, query, args...)
}

// LastJobRuns returns the most recent run of every job of an application, by
// job name, without output.
func (s *Store) LastJobRuns(ctx context.Context, appID int64) (map[string]JobRun, error) {
	runs, err := s.listJobRuns(ctx, fmt.Sprintf(jobRunSelect, "''")+`
		WHERE r.id IN (SELECT MAX(id) FROM job_runs WHERE application_id = ? GROUP BY job)`, appID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]JobRun, len(runs))
	for _, r := range runs {
		out[r.Job] = r
	}
	return out, nil
}

// PruneJobRuns keeps only the newest `keep` runs of one job.
func (s *Store) PruneJobRuns(ctx context.Context, appID int64, job string, keep int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM job_runs WHERE application_id = ? AND job = ? AND id NOT IN (
			SELECT id FROM job_runs WHERE application_id = ? AND job = ? ORDER BY id DESC LIMIT ?)`,
		appID, job, appID, job, keep)
	if err != nil {
		return fmt.Errorf("prune job runs: %w", err)
	}
	return nil
}

func (s *Store) listJobRuns(ctx context.Context, query string, args ...any) ([]JobRun, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list job runs: %w", err)
	}
	defer rows.Close()

	out := []JobRun{}
	for rows.Next() {
		r, err := scanJobRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
