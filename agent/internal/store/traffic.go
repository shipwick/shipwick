package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TrafficSample is what the proxy saw of one application in one minute. At is
// the start of the minute; Latency holds the counts of a histogram whose
// buckets the caller defines.
type TrafficSample struct {
	ApplicationID int64
	At            time.Time
	Requests      int64
	Status2xx     int64
	Status3xx     int64
	Status4xx     int64
	Status5xx     int64
	Bytes         int64
	Latency       []int64
}

// AddTrafficSamples stores the minutes that have ended, in one transaction.
func (s *Store) AddTrafficSamples(ctx context.Context, samples []TrafficSample) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO traffic_samples (application_id, at, requests, status_2xx, status_3xx, status_4xx, status_5xx, bytes, latency)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, t := range samples {
			if _, err := stmt.ExecContext(ctx, t.ApplicationID, formatTime(t.At), t.Requests,
				t.Status2xx, t.Status3xx, t.Status4xx, t.Status5xx, t.Bytes, formatCounts(t.Latency)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store traffic samples: %w", err)
	}
	return nil
}

// PruneTrafficSamples removes every sample of a minute before the given time
// and reports how many.
func (s *Store) PruneTrafficSamples(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM traffic_samples WHERE at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune traffic samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// TrafficSamples returns the application's samples since `since`, oldest
// first. They are aggregated by the caller, which knows the histogram: a week
// of one application is at most ten thousand rows.
func (s *Store) TrafficSamples(ctx context.Context, appID int64, since time.Time) ([]TrafficSample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, requests, status_2xx, status_3xx, status_4xx, status_5xx, bytes, latency
		 FROM traffic_samples WHERE application_id = ? AND at >= ? ORDER BY at`,
		appID, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("traffic samples: %w", err)
	}
	defer rows.Close()

	var out []TrafficSample
	for rows.Next() {
		var at, latency string
		t := TrafficSample{ApplicationID: appID}
		if err := rows.Scan(&at, &t.Requests, &t.Status2xx, &t.Status3xx, &t.Status4xx, &t.Status5xx, &t.Bytes, &latency); err != nil {
			return nil, err
		}
		if t.At, err = parseTime(at); err != nil {
			return nil, err
		}
		if t.Latency, err = parseCounts(latency); err != nil {
			return nil, fmt.Errorf("traffic samples: latency of %s: %w", at, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func formatCounts(counts []int64) string {
	parts := make([]string, len(counts))
	for i, n := range counts {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(parts, ",")
}

func parseCounts(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}
