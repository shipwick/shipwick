package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// partialSuffix marks a file that is still being written: see Write.
const partialSuffix = ".partial"

// FoundRun is what the destinations hold of one run, whoever recorded it: a
// database restored from a backup has forgotten the runs that came after it,
// and their files are still where they were written.
type FoundRun struct {
	Owner string
	ID    int64
	// Files are the run's files by name, each once however many destinations
	// hold it.
	Files []FoundFile
	// Destinations are the destinations that hold every one of the files.
	Destinations []string
	// Modified is when the newest of the files was written.
	Modified time.Time
}

// FoundFile is one file of a run.
type FoundFile struct {
	Name      string // as it was written: without the suffix of an encrypted file
	Encrypted bool
	Size      int64 // as stored
	// Differs is set when the directory and the bucket hold the file in
	// different sizes: one of them is not the file that was written.
	Differs bool
}

// Found lists the runs of owner that the directory and the bucket hold files
// of, in order of their ids; an empty owner means every owner. It reads and
// changes nothing. Call it only once the bucket is known to be this
// installation's (Prepare): what another installation wrote there is not this
// one's to find.
//
// Names come back as they were found, and a bucket's keys are anybody's to
// choose: a name that is not a plain file name is left out here, before it
// can become part of a path.
func (s *Storage) Found(ctx context.Context, owner string) ([]FoundRun, error) {
	type place struct {
		size     int64
		modified time.Time
	}
	type copies struct{ local, remote *place }
	type runKey struct {
		owner string
		id    int64
	}
	runs := map[runKey]map[string]*copies{}
	file := func(owner string, id int64, name string) *copies {
		k := runKey{owner, id}
		if runs[k] == nil {
			runs[k] = map[string]*copies{}
		}
		if runs[k][name] == nil {
			runs[k][name] = &copies{}
		}
		return runs[k][name]
	}

	if owner != "" && !plainName(owner) {
		return nil, fmt.Errorf("%q is not a name backups are kept under", owner)
	}
	owners := []string{owner}
	if owner == "" {
		owners = owners[:0]
		entries, err := os.ReadDir(s.dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read backup directory: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				owners = append(owners, e.Name())
			}
		}
	}
	for _, o := range owners {
		if !plainName(o) {
			continue
		}
		ids, err := os.ReadDir(filepath.Join(s.dir, o))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read backup directory: %w", err)
		}
		for _, entry := range ids {
			id, err := strconv.ParseInt(entry.Name(), 10, 64)
			// The id as it is written, and nothing that merely parses to it.
			if err != nil || id < 1 || !entry.IsDir() || entry.Name() != strconv.FormatInt(id, 10) {
				continue
			}
			files, err := os.ReadDir(s.runDir(o, id))
			if err != nil {
				return nil, fmt.Errorf("read backup directory: %w", err)
			}
			for _, f := range files {
				info, err := f.Info()
				if err != nil || !info.Mode().IsRegular() || !plainName(f.Name()) || strings.HasSuffix(f.Name(), partialSuffix) || strings.HasSuffix(f.Name(), uploadSuffix) {
					continue
				}
				file(o, id, f.Name()).local = &place{size: info.Size(), modified: info.ModTime()}
			}
		}
	}

	if s.s3 != nil {
		prefix := ""
		if owner != "" {
			prefix = owner + "/"
		}
		objects, err := s.s3.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		for _, obj := range objects {
			if o, id, name, ok := splitRunKey(obj.Key); ok && plainName(o) && plainName(name) {
				file(o, id, name).remote = &place{size: obj.Size, modified: obj.Modified}
			}
		}
	}

	out := make([]FoundRun, 0, len(runs))
	for k, files := range runs {
		run := FoundRun{Owner: k.owner, ID: k.id}
		everywhere := map[string]bool{Local: true, Remote: true}
		for name, c := range files {
			f := FoundFile{Name: name}
			if plain, ok := strings.CutSuffix(name, encryptedSuffix); ok {
				f.Name, f.Encrypted = plain, true
			}
			for where, p := range map[string]*place{Local: c.local, Remote: c.remote} {
				if p == nil {
					everywhere[where] = false
					continue
				}
				f.Size = p.size
				if p.modified.After(run.Modified) {
					run.Modified = p.modified
				}
			}
			f.Differs = c.local != nil && c.remote != nil && c.local.size != c.remote.size
			run.Files = append(run.Files, f)
		}
		sort.Slice(run.Files, func(i, j int) bool { return run.Files[i].Name < run.Files[j].Name })
		for _, where := range []string{Local, Remote} {
			if everywhere[where] {
				run.Destinations = append(run.Destinations, where)
			}
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Owner < out[j].Owner
	})
	return out, nil
}

// plainName reports whether name can be one element of a path and of a key,
// and nothing else: no separator, no way up, nothing hidden.
func plainName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, "/\\:\x00")
}
