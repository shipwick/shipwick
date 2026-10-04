package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// The audit trail is kept for a year, and never grows beyond AuditMaxEntries:
// a year answers "who did that" for as long as anyone asks, and the count
// keeps a token that is used in a loop from filling the disk.
const (
	AuditRetention  = 365 * 24 * time.Hour
	AuditMaxEntries = 100_000
)

// AddAuditEntry writes one entry and returns its id. What has outlived the
// retention goes in the same transaction: both deletions walk an index, and
// entries are written at the pace people and CI change things.
func (s *Store) AddAuditEntry(ctx context.Context, e api.AuditEntry) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO audit_log (at, actor_kind, actor, address, forwarded_for, action, application, target, outcome, status, code, detail)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			formatTime(e.At), e.Actor.Kind, e.Actor.Name, e.Address, e.ForwardedFor, e.Action, e.Application, e.Target, e.Outcome, e.Status, e.Code, e.Detail)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, formatTime(e.At.Add(-AuditRetention))); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM audit_log WHERE id <= ?`, id-AuditMaxEntries)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("write audit entry: %w", err)
	}
	return id, nil
}

// AuditFilter narrows AuditEntries. Zero values do not filter.
type AuditFilter struct {
	Application string
	Actor       string
	Since       time.Time
	// Actions are actions as the trail records them, or the start of a
	// family with its dot ("token."); an entry matches when one of them does.
	Actions []string
	// Outcomes are api.AuditOK and its neighbours; any of them matches.
	Outcomes []string
	// ActorKind is api.ActorToken or api.ActorUser.
	ActorKind string
	// Before is the id of the last entry of the page before: only older
	// entries are returned.
	Before int64
	Limit  int
}

// conditions is the filter as the WHERE clause of a query on audit_log.
func (f AuditFilter) conditions() (where []string, args []any) {
	if f.Application != "" {
		where = append(where, "application = ?")
		args = append(args, f.Application)
	}
	if f.Actor != "" {
		where = append(where, "actor = ?")
		args = append(args, f.Actor)
	}
	if !f.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, formatTime(f.Since))
	}
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	if len(f.Actions) > 0 {
		var either []string
		for _, action := range f.Actions {
			if strings.HasSuffix(action, ".") {
				either = append(either, "substr(action, 1, ?) = ?")
				args = append(args, len(action), action)
			} else {
				either = append(either, "action = ?")
				args = append(args, action)
			}
		}
		where = append(where, "("+strings.Join(either, " OR ")+")")
	}
	if len(f.Outcomes) > 0 {
		where = append(where, "outcome IN (?"+strings.Repeat(", ?", len(f.Outcomes)-1)+")")
		for _, outcome := range f.Outcomes {
			args = append(args, outcome)
		}
	}
	if f.ActorKind != "" {
		where = append(where, "actor_kind = ?")
		args = append(args, f.ActorKind)
	}
	return where, args
}

// AuditEntries returns the entries that match, newest first.
func (s *Store) AuditEntries(ctx context.Context, f AuditFilter) ([]api.AuditEntry, error) {
	where, args := f.conditions()
	query := `SELECT id, at, actor_kind, actor, address, forwarded_for, action, application, target, outcome, status, code, detail FROM audit_log`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	out := []api.AuditEntry{}
	for rows.Next() {
		var (
			e  api.AuditEntry
			at string
		)
		if err := rows.Scan(&e.ID, &at, &e.Actor.Kind, &e.Actor.Name, &e.Address, &e.ForwardedFor, &e.Action, &e.Application, &e.Target, &e.Outcome, &e.Status, &e.Code, &e.Detail); err != nil {
			return nil, err
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
