package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrTooManyCertificates is returned by SetCertificate when a new hostname
// would exceed api.MaxCertificates.
var ErrTooManyCertificates = fmt.Errorf("at most %d certificates can be stored; remove one first", api.MaxCertificates)

// Certificate is a certificate the operator supplied for a hostname: the
// chain and its key, both PEM. The key is what makes it a secret.
type Certificate struct {
	Hostname  string
	CertPEM   string
	KeyPEM    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// keyLabel is what a certificate's key is sealed under: a key cut from one
// hostname's row and pasted into another's does not decrypt there.
func keyLabel(hostname string) string {
	return "certificate:" + hostname
}

// SetCertificate stores a certificate under hostname, replacing the one
// there, and returns it as stored. The chain is public and kept as it is;
// the key is sealed like a secret's value.
func (s *Store) SetCertificate(ctx context.Context, hostname, certPEM, keyPEM string, now time.Time) (Certificate, error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	stored := keyPEM
	if s.aead != nil {
		var err error
		if stored, err = seal(s.aead, keyLabel(hostname), keyPEM); err != nil {
			return Certificate{}, fmt.Errorf("encrypt the key of the certificate for %s: %w", hostname, err)
		}
	}
	c := Certificate{Hostname: hostname, CertPEM: certPEM, KeyPEM: keyPEM, UpdatedAt: now.UTC()}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificates WHERE hostname <> ?`, hostname).Scan(&others); err != nil {
			return err
		}
		if others >= api.MaxCertificates {
			return ErrTooManyCertificates
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO certificates (hostname, certificate, key, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(hostname) DO UPDATE SET certificate = excluded.certificate, key = excluded.key, updated_at = excluded.updated_at`,
			hostname, certPEM, []byte(stored), formatTime(now), formatTime(now)); err != nil {
			return err
		}
		var created string
		if err := tx.QueryRowContext(ctx, `SELECT created_at FROM certificates WHERE hostname = ?`, hostname).Scan(&created); err != nil {
			return err
		}
		var err error
		c.CreatedAt, err = parseTime(created)
		return err
	})
	if errors.Is(err, ErrTooManyCertificates) {
		return Certificate{}, err
	} else if err != nil {
		return Certificate{}, fmt.Errorf("store the certificate for %s: %w", hostname, err)
	}
	return c, nil
}

func (s *Store) DeleteCertificate(ctx context.Context, hostname string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM certificates WHERE hostname = ?`, hostname)
	if err != nil {
		return fmt.Errorf("delete the certificate for %s: %w", hostname, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCertificates returns every stored certificate with its key, in
// hostname order. The keys are for the proxy and nobody else: nothing that
// answers a request may carry them.
func (s *Store) ListCertificates(ctx context.Context) ([]Certificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hostname, certificate, key, created_at, updated_at FROM certificates ORDER BY hostname`)
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	defer rows.Close()

	var out []Certificate
	for rows.Next() {
		var (
			c                Certificate
			key              []byte
			created, updated string
		)
		if err := rows.Scan(&c.Hostname, &c.CertPEM, &key, &created, &updated); err != nil {
			return nil, err
		}
		c.KeyPEM = string(key)
		if s.aead != nil {
			if !isSealed(c.KeyPEM) {
				return nil, fmt.Errorf("the key of the certificate for %s is stored unencrypted", c.Hostname)
			}
			if c.KeyPEM, err = open(s.aead, keyLabel(c.Hostname), c.KeyPEM); err != nil {
				return nil, err
			}
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
