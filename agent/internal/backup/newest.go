package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Newest returns the highest run of owner that holds the file name, in the
// directory or the bucket, and 0 when there is none. A file appears under its
// name only once it is complete, in either place, so what is found can be
// opened. A standby asks this of a bucket another installation writes to; it
// reads there and writes nothing.
func (s *Storage) Newest(ctx context.Context, owner, name string) (int64, error) {
	var newest int64
	runs, err := os.ReadDir(filepath.Join(s.dir, owner))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read backup directory: %w", err)
	}
	for _, run := range runs {
		id, err := strconv.ParseInt(run.Name(), 10, 64)
		if err != nil {
			continue
		}
		for _, file := range []string{name, name + encryptedSuffix} {
			if _, err := os.Stat(filepath.Join(s.runDir(owner, id), file)); err == nil {
				newest = max(newest, id)
			}
		}
	}
	if s.s3 == nil {
		return newest, nil
	}
	objects, err := s.s3.List(ctx, owner+"/")
	if err != nil {
		return 0, err
	}
	for _, o := range objects {
		// <owner>/<run id>/<file>
		parts := strings.Split(o.Key, "/")
		if len(parts) != 3 || (parts[2] != name && parts[2] != name+encryptedSuffix) {
			continue
		}
		if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			newest = max(newest, id)
		}
	}
	return newest, nil
}
