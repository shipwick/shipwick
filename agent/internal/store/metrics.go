package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// MetricSample is one reading of one replica's resource usage.
type MetricSample struct {
	ApplicationID int64
	Replica       int
	At            time.Time
	CPUPercent    float64 // percent of one core
	MemoryBytes   int64
}

// MetricBucket aggregates the samples of one replica over one step: the
// average CPU and the peak memory. At is the start of the bucket.
type MetricBucket struct {
	Replica     int
	At          time.Time
	CPUPercent  float64
	MemoryBytes int64
}

// AddSamples stores one tick's readings in a single transaction.
func (s *Store) AddSamples(ctx context.Context, samples []MetricSample) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO metric_samples (application_id, replica, at, cpu_percent, memory_bytes) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range samples {
			if _, err := stmt.ExecContext(ctx, m.ApplicationID, m.Replica, formatTime(m.At), m.CPUPercent, m.MemoryBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store metric samples: %w", err)
	}
	return nil
}

// PruneSamples removes every sample taken before the given time and reports
// how many.
func (s *Store) PruneSamples(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune metric samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MetricHistory aggregates the application's samples since `since` into
// buckets of `step`, aligned to multiples of the step since the epoch, ordered
// by replica and time. Buckets that hold no sample are not returned.
func (s *Store) MetricHistory(ctx context.Context, appID int64, since time.Time, step time.Duration) ([]MetricBucket, error) {
	secs := int64(step / time.Second)
	if secs < 1 {
		return nil, fmt.Errorf("metric history: step %s is shorter than a second", step)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT replica, (CAST(strftime('%s', at) AS INTEGER) / ?) * ? AS bucket, AVG(cpu_percent), MAX(memory_bytes)
		 FROM metric_samples WHERE application_id = ? AND at >= ?
		 GROUP BY replica, bucket ORDER BY replica, bucket`,
		secs, secs, appID, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("metric history: %w", err)
	}
	defer rows.Close()

	var out []MetricBucket
	for rows.Next() {
		var (
			b      MetricBucket
			bucket int64
		)
		if err := rows.Scan(&b.Replica, &bucket, &b.CPUPercent, &b.MemoryBytes); err != nil {
			return nil, err
		}
		b.At = time.Unix(bucket, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}
