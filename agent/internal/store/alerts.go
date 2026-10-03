package store

import (
	"context"
	"fmt"
	"time"
)

// RecentSamples returns the application's samples taken at or after `since`,
// unaggregated, ordered by replica and time. It is what the memory alert is
// decided from; the window is a few samples long.
func (s *Store) RecentSamples(ctx context.Context, appID int64, since time.Time) ([]MetricSample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT application_id, replica, at, cpu_percent, memory_bytes FROM metric_samples
		 WHERE application_id = ? AND at >= ? ORDER BY replica, at`, appID, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("recent metric samples: %w", err)
	}
	defer rows.Close()
	return scanSamples(rows)
}

func scanSamples(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]MetricSample, error) {
	var out []MetricSample
	for rows.Next() {
		var (
			m  MetricSample
			at string
		)
		if err := rows.Scan(&m.ApplicationID, &m.Replica, &at, &m.CPUPercent, &m.MemoryBytes); err != nil {
			return nil, err
		}
		var err error
		if m.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
