package store

import (
	"context"
	"database/sql"
)

// querier is what *sql.DB and *sql.Tx have in common, so that a read can be
// done alone or as part of a transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ApplicationsWithDeployments reads the applications and the deployments
// matching f in one transaction. Read separately, a deployment committed
// between the two reads makes an application look as if it had neither an
// active deployment nor one in flight, which the views would call FAILED — for
// one poll, right as the deployment succeeds or as the first one is recorded.
func (s *Store) ApplicationsWithDeployments(ctx context.Context, f DeploymentFilter) (apps []Application, deployments []Deployment, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if apps, err = listApplications(ctx, tx); err != nil {
			return err
		}
		deployments, err = s.listDeployments(ctx, tx, f)
		return err
	})
	return apps, deployments, err
}

// ApplicationWithDeployments is ApplicationsWithDeployments for one
// application, by name.
func (s *Store) ApplicationWithDeployments(ctx context.Context, name string, f DeploymentFilter) (app Application, deployments []Deployment, err error) {
	f.Application = name
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if app, err = scanApplication(tx.QueryRowContext(ctx, applicationSelect+` WHERE name = ?`, name)); err != nil {
			return err
		}
		deployments, err = s.listDeployments(ctx, tx, f)
		return err
	})
	return app, deployments, err
}
