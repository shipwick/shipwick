package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// Scrape is what the database knows of everything a metrics scrape reports.
type Scrape struct {
	Applications []Application
	// Active holds the active deployment of every application that has one,
	// by ID; Replicas are theirs, as far as their containers still exist.
	Active   map[int64]Deployment
	Replicas []Replica
	// InFlight names the applications with a deployment in progress.
	InFlight map[string]bool
	// Counts is the number of deployments per application and status.
	Counts []DeploymentCount
	// LastDuration is how long each application's most recently completed
	// deployment took.
	LastDuration map[string]time.Duration
	// Samples holds the latest sample of every replica that has one at or
	// after the time asked for, ordered by application and replica.
	Samples []MetricSample
}

type DeploymentCount struct {
	Application string
	Status      api.DeploymentStatus
	Count       int
}

// Scrape reads everything in one transaction, so that the numbers of one
// scrape agree with each other: an application never appears with the
// replicas of a deployment that has just been replaced.
func (s *Store) Scrape(ctx context.Context, inFlight []api.DeploymentStatus, samplesSince time.Time) (Scrape, error) {
	out := Scrape{Active: map[int64]Deployment{}, InFlight: map[string]bool{}, LastDuration: map[string]time.Duration{}}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if out.Applications, err = listApplications(ctx, tx); err != nil {
			return err
		}
		if err := s.scrapeDeployments(ctx, tx, &out, inFlight); err != nil {
			return err
		}
		if err := scrapeReplicas(ctx, tx, &out); err != nil {
			return err
		}
		// SQLite takes the other columns from the row that holds the maximum.
		rows, err := tx.QueryContext(ctx,
			`SELECT application_id, replica, MAX(at), cpu_percent, memory_bytes FROM metric_samples
			 WHERE at >= ? GROUP BY application_id, replica ORDER BY application_id, replica`,
			formatTime(samplesSince))
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Samples, err = scanSamples(rows)
		return err
	})
	if err != nil {
		return Scrape{}, fmt.Errorf("read metrics: %w", err)
	}
	return out, nil
}

func (s *Store) scrapeDeployments(ctx context.Context, tx *sql.Tx, out *Scrape, inFlight []api.DeploymentStatus) error {
	rows, err := tx.QueryContext(ctx, deploymentSelect+
		` WHERE d.id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		d, err := s.scanDeployment(rows)
		if err != nil {
			rows.Close()
			return err
		}
		out.Active[d.ID] = d
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx,
		`SELECT a.name, d.status, COUNT(*) FROM deployments d JOIN applications a ON a.id = d.application_id
		 GROUP BY a.name, d.status ORDER BY a.name, d.status`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c DeploymentCount
		var status string
		if err := rows.Scan(&c.Application, &status, &c.Count); err != nil {
			rows.Close()
			return err
		}
		c.Status = api.DeploymentStatus(status)
		for _, st := range inFlight {
			if c.Status == st {
				out.InFlight[c.Application] = true
			}
		}
		out.Counts = append(out.Counts, c)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx,
		`SELECT a.name, d.started_at, d.completed_at FROM deployments d JOIN applications a ON a.id = d.application_id
		 WHERE d.id IN (SELECT MAX(id) FROM deployments WHERE completed_at IS NOT NULL GROUP BY application_id)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, started, completed string
		if err := rows.Scan(&name, &started, &completed); err != nil {
			rows.Close()
			return err
		}
		from, err := parseTime(started)
		if err != nil {
			rows.Close()
			return err
		}
		to, err := parseTime(completed)
		if err != nil {
			rows.Close()
			return err
		}
		out.LastDuration[name] = to.Sub(from)
	}
	return closeRows(rows)
}

func scrapeReplicas(ctx context.Context, tx *sql.Tx, out *Scrape) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT deployment_id, replica_index, container_id, container_name, restart_count FROM deployment_replicas
		 WHERE removed_at IS NULL AND deployment_id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)
		 ORDER BY deployment_id, replica_index`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r Replica
		if err := rows.Scan(&r.DeploymentID, &r.Index, &r.ContainerID, &r.ContainerName, &r.Restarts); err != nil {
			rows.Close()
			return err
		}
		out.Replicas = append(out.Replicas, r)
	}
	return closeRows(rows)
}

// closeRows closes a result inside a transaction, where the one connection
// must be free before the next query, and reports what ended the iteration.
func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
