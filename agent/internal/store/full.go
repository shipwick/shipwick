package store

import (
	"context"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsFull reports whether err is SQLite refusing a write for lack of room: the
// disk that holds the data directory is full. Reads still work then, and so
// does every write once there is room again; nothing has to be reopened.
func IsFull(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) && serr.Code()&0xff == sqlite3.SQLITE_FULL
}

// What a full disk does to the database depends on what filled it. Measured
// on a filesystem of a few megabytes (see TestWhatAFullDiskDoesToTheDatabase):
// when something else took the room — images, logs, an upload — every write
// fails, an update in place as much as an insert, because none of them can be
// added to the write-ahead log; when the database itself grew to the end of
// the disk, only the writes that need a new page fail. The two switches below
// make a database behave in each of these ways without a small disk, for the
// tests of what the agent does then. Both live in the connection and are gone
// with the process.

// Fill keeps the database from growing beyond the pages it has: a write that
// needs a new one fails with SQLITE_FULL, as on a disk the database has
// filled. full == false lifts the limit, as freeing space does.
func (s *Store) Fill(ctx context.Context, full bool) error {
	limit := int64(1<<32 - 2) // SQLite's own maximum
	if full {
		if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&limit); err != nil {
			return fmt.Errorf("read page count: %w", err)
		}
	}
	// PRAGMA does not accept bind parameters; limit is a trusted integer.
	var got int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", limit)).Scan(&got); err != nil {
		return fmt.Errorf("limit the database: %w", err)
	}
	return nil
}

// RefuseWrites makes every write fail and leaves reads alone, as on a disk
// that something else has filled, or one the kernel has remounted read-only
// after an error. refuse == false ends it.
func (s *Store) RefuseWrites(ctx context.Context, refuse bool) error {
	value := 0
	if refuse {
		value = 1
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA query_only = %d", value)); err != nil {
		return fmt.Errorf("refuse writes: %w", err)
	}
	return nil
}
