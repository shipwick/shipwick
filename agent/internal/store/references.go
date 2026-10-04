package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

// A deployment's spec holds the values its containers were started with. The
// references a document had in their place — ${NAME}, filled in from the
// stored secrets — are kept next to it, in a row of their own: the record of
// what ran stays what it was, and the document can still be given back as it
// was written. They are sealed like a secret's value: the text around a
// reference is whatever the document said there.

// referencesName is what a deployment's references are sealed under: the
// deployment they belong to, so that a row moved to another does not open.
func referencesName(deploymentID string) string {
	return "deployments/" + deploymentID + "/references"
}

// NewDeployment is what CreateDeploymentWith records.
type NewDeployment struct {
	Spec     spec.App
	Kind     string
	SourceID *int64
	Actor    string
	// Static is set for a static application: see CreateStaticDeployment.
	Static *StaticFiles
	// References are the secret values of Spec that were references.
	References spec.References
}

// CreateDeploymentWith is CreateDeploymentFrom and CreateStaticDeployment in
// one, for a deployment that has references to keep: the record and its
// references are written together or not at all.
func (s *Store) CreateDeploymentWith(ctx context.Context, n NewDeployment, now time.Time) (Deployment, error) {
	d := Deployment{
		Application: n.Spec.Name,
		Version:     n.Spec.Version(),
		Image:       n.Spec.Image,
		Spec:        n.Spec,
		Kind:        n.Kind,
		SourceID:    n.SourceID,
		Actor:       n.Actor,
		References:  n.References.For(n.Spec),
	}
	if n.Static != nil {
		d.Version, d.Image = StaticVersion(n.Static.Digest), ""
		d.StaticDigest, d.StaticFiles, d.StaticBytes = n.Static.Digest, n.Static.Files, n.Static.Bytes
	}
	return s.insertDeployment(ctx, d, now)
}

// insertReferences keeps the references of a deployment that is being
// recorded. The caller holds keyMu, as for the spec it seals.
func (s *Store) insertReferences(ctx context.Context, tx *sql.Tx, deploymentID int64, refs spec.References) error {
	if refs.Empty() {
		return nil
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("encode references: %w", err)
	}
	stored := string(data)
	if s.aead != nil {
		if stored, err = seal(s.aead, referencesName(fmt.Sprint(deploymentID)), stored); err != nil {
			return fmt.Errorf("encrypt references: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deployment_references (deployment_id, value) VALUES (?, ?)`, deploymentID, []byte(stored))
	return err
}

// DeploymentReferences returns the references kept for a deployment; none
// for one that had none, or that was recorded before they were kept.
func (s *Store) DeploymentReferences(ctx context.Context, deploymentID int64) (spec.References, error) {
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM deployment_references WHERE deployment_id = ?`, deploymentID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return spec.References{}, nil
	} else if err != nil {
		return spec.References{}, fmt.Errorf("read references of deployment %d: %w", deploymentID, err)
	}
	plain := string(stored)
	if s.aead != nil {
		if plain, err = open(s.aead, referencesName(fmt.Sprint(deploymentID)), plain); err != nil {
			return spec.References{}, fmt.Errorf("references of deployment %d: %w", deploymentID, err)
		}
	}
	var refs spec.References
	if err := json.Unmarshal([]byte(plain), &refs); err != nil {
		return spec.References{}, fmt.Errorf("decode references of deployment %d: %w", deploymentID, err)
	}
	return refs, nil
}
