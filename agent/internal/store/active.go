package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/shipwick/shipwick/pkg/api"
)

// Active is what every application runs right now: the applications, the
// active deployment of each that has one, and the replicas of those
// deployments.
type Active struct {
	Applications []Application
	// Deployments are the active deployments, by application ID.
	Deployments map[int64]Deployment
	// Replicas are the replicas of the active deployments whose containers
	// have not been removed, by deployment ID, in replica order.
	Replicas map[int64][]Replica
	// InFlight are the deployments whose status ActiveDeployments was asked
	// for; nil when it was asked for none.
	InFlight []Deployment
}

// ActiveDeployments reads, in one transaction, what the supervisor, the
// sampler and the views otherwise read one application at a time: the
// applications, the active deployment of each and its replicas, and — when
// statuses are given — every deployment in one of those statuses, which is
// how the views learn what is under way without a read of their own that
// could fall between the others.
//
// Three statements, whatever the number of applications. The driver parses
// a statement every time it runs, prepared or not, and the deployment's
// SELECT is the longest one the agent has: read per application, as it was,
// a supervision pass over a hundred applications spent a third of its time
// parsing that one statement.
func (s *Store) ActiveDeployments(ctx context.Context, statuses ...api.DeploymentStatus) (Active, error) {
	a := Active{Deployments: map[int64]Deployment{}, Replicas: map[int64][]Replica{}}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if a.Applications, err = listApplications(ctx, tx); err != nil {
			return err
		}
		// Both reads start from the applications, which are few, and look the
		// rest up by key. Joined the other way round SQLite walks the whole
		// deployment history, which a year of daily deployments makes long.
		rows, err := tx.QueryContext(ctx, deploymentSelect+
			` WHERE d.id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)`)
		if err != nil {
			return fmt.Errorf("list active deployments: %w", err)
		}
		for rows.Next() {
			d, err := s.scanDeployment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			a.Deployments[d.ApplicationID] = d
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx,
			`SELECT deployment_id, replica_index, container_id, container_name, restart_count FROM deployment_replicas
			 WHERE deployment_id IN (SELECT active_deployment_id FROM applications WHERE active_deployment_id IS NOT NULL)
			   AND removed_at IS NULL ORDER BY deployment_id, replica_index`)
		if err != nil {
			return fmt.Errorf("list active replicas: %w", err)
		}
		for rows.Next() {
			var r Replica
			if err := rows.Scan(&r.DeploymentID, &r.Index, &r.ContainerID, &r.ContainerName, &r.Restarts); err != nil {
				rows.Close()
				return err
			}
			a.Replicas[r.DeploymentID] = append(a.Replicas[r.DeploymentID], r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(statuses) > 0 {
			a.InFlight, err = s.listDeployments(ctx, tx, DeploymentFilter{Statuses: statuses})
		}
		return err
	})
	if err != nil {
		return Active{}, err
	}
	return a, nil
}
