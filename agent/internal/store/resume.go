package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CompleteDeploymentsExcept stamps every deployment that lacks a completion
// time, apart from the given ones. Only meaningful at startup: those are the
// deployments the agent resumes, and everything else that looks unfinished
// was cut off after it had settled.
func (s *Store) CompleteDeploymentsExcept(ctx context.Context, except []int64, now time.Time) error {
	query := `UPDATE deployments SET completed_at = ? WHERE completed_at IS NULL`
	args := []any{formatTime(now)}
	if len(except) > 0 {
		marks := make([]string, len(except))
		for i, id := range except {
			marks[i] = "?"
			args = append(args, id)
		}
		query += " AND id NOT IN (" + strings.Join(marks, ", ") + ")"
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("complete deployments: %w", err)
	}
	return nil
}
