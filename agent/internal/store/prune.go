package store

import (
	"context"
	"database/sql"
	"time"
)

// pruneSamplesOf removes the rows of a samples table — metric_samples or
// traffic_samples, whose rows belong to an application and have a time —
// taken before the given time, and reports how many.
//
// One DELETE per application, not one over the table: the tables are indexed
// by (application_id, at), and a DELETE by time alone reads every row there
// is — a week of 40 replicas is 400,000 of them, 130 ms of the one connection
// every hour on a desktop — where the index finds what is due in a fraction
// of a millisecond. A sample belongs to a row of applications or is gone
// with it, so the applications are every owner there is.
func (s *Store) pruneSamplesOf(ctx context.Context, table string, before time.Time) (int64, error) {
	var removed int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM applications`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// table is one of two names this package owns, never input.
		stmt, err := tx.PrepareContext(ctx, `DELETE FROM `+table+` WHERE application_id = ? AND at < ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		cutoff := formatTime(before)
		for _, id := range ids {
			res, err := stmt.ExecContext(ctx, id, cutoff)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			removed += n
		}
		return nil
	})
	return removed, err
}
