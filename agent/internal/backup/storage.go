// Package backup is where backups are kept: a directory on the server,
// always, and an S3-compatible bucket as well when one is configured. It
// knows files and objects; what goes into them and when is the deployment
// engine's business.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/pkg/backupfile"
)

// Destination names, as the API reports them.
const (
	Local  = "local"
	Remote = "s3"
)

// encryptedSuffix is appended to the name of a file that is stored in the
// encrypted format, so that nobody takes it for the tar archive it wraps.
const encryptedSuffix = ".enc"

// ErrNoPassphrase means an encrypted backup was asked for and the agent has
// no passphrase to open it with.
var ErrNoPassphrase = errors.New("the backup is encrypted, and SHIPWICK_BACKUP_PASSPHRASE is not set on the agent")

// Storage keeps the files of backup runs. A run's files live together, under
// <owner>/<run id>/, in the directory and under the same key in the bucket;
// the owner is an application's name, or StateOwner.
type Storage struct {
	dir        string
	s3         *S3
	passphrase string
}

// StateOwner is the owner of the agent's own state. It cannot be an
// application's name: those do not contain an underscore.
const StateOwner = "_agent"

// New returns a Storage that writes under dir, to bucket as well unless it is
// nil, and encrypts what it writes when passphrase is not empty.
func New(dir string, bucket *S3, passphrase string) *Storage {
	return &Storage{dir: dir, s3: bucket, passphrase: passphrase}
}

// Dir is the directory on the server.
func (s *Storage) Dir() string { return s.dir }

// Encrypted reports whether new backups are written encrypted.
func (s *Storage) Encrypted() bool { return s.passphrase != "" }

// Destinations lists where new backups go.
func (s *Storage) Destinations() []string {
	if s.s3 != nil {
		return []string{Local, Remote}
	}
	return []string{Local}
}

func (s *Storage) runDir(owner string, run int64) string {
	return filepath.Join(s.dir, owner, strconv.FormatInt(run, 10))
}

func runKey(owner string, run int64) string {
	return owner + "/" + strconv.FormatInt(run, 10) + "/"
}

func stored(name string, encrypted bool) string {
	if encrypted {
		return name + encryptedSuffix
	}
	return name
}

// Write stores src as one file of a run, in every destination, and returns
// how many bytes src held. The file appears under its name only once it is
// complete: a half-written archive must not look like a backup.
func (s *Storage) Write(ctx context.Context, owner string, run int64, name string, src io.Reader) (int64, error) {
	dir := s.runDir(owner, run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create backup directory: %w", err)
	}
	name = stored(name, s.Encrypted())
	final := filepath.Join(dir, name)
	tmp, err := os.OpenFile(final+".partial", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create backup file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// Hashed as it is written: the upload's signature wants the hash of what
	// is sent, and reading the file a second time for it would double the
	// work.
	hash := sha256.New()
	var dst io.Writer = io.MultiWriter(tmp, hash)
	var sealer *backupfile.Writer
	if s.Encrypted() {
		if sealer, err = backupfile.NewWriter(dst, s.passphrase); err != nil {
			return 0, err
		}
		dst = sealer
	}
	n, err := io.Copy(dst, &contextReader{ctx: ctx, r: src})
	if err != nil {
		return n, err
	}
	if sealer != nil {
		if err := sealer.Close(); err != nil {
			return n, fmt.Errorf("write backup file: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return n, fmt.Errorf("write backup file: %w", err)
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return n, fmt.Errorf("write backup file: %w", err)
	}

	if s.s3 != nil {
		// A multipart upload is noted next to the file before its first part
		// goes: an agent that dies in the middle cannot abort it, and the next
		// one must be able to (AbortLeftovers). The note goes when the upload
		// is over, unless it ended with its parts still in the bucket.
		note := final + uploadSuffix
		err := s.s3.Upload(ctx, runKey(owner, run)+name, tmp, size, hex.EncodeToString(hash.Sum(nil)), func(id string) error {
			if err := os.WriteFile(note, []byte(id), 0o600); err != nil {
				return fmt.Errorf("note the upload: %w", err)
			}
			return nil
		})
		if !errors.Is(err, errPartsRemain) {
			os.Remove(note)
		}
		if err != nil {
			return n, err
		}
	}
	if err := tmp.Close(); err != nil {
		return n, fmt.Errorf("write backup file: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return n, fmt.Errorf("write backup file: %w", err)
	}
	return n, nil
}

// Open returns one file of a run, decrypted when the run was written
// encrypted. The directory is asked first; a file that is not there — the
// server was rebuilt, the disk replaced — comes from the bucket.
func (s *Storage) Open(ctx context.Context, owner string, run int64, name string, encrypted bool) (io.ReadCloser, error) {
	if encrypted && s.passphrase == "" {
		return nil, ErrNoPassphrase
	}
	name = stored(name, encrypted)
	var rc io.ReadCloser
	f, err := os.Open(filepath.Join(s.runDir(owner, run), name))
	switch {
	case err == nil:
		rc = f
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("open backup file: %w", err)
	case s.s3 == nil:
		return nil, fmt.Errorf("the backup's file %s is no longer in %s", name, s.runDir(owner, run))
	default:
		if rc, err = s.s3.Get(ctx, runKey(owner, run)+name); err != nil {
			return nil, err
		}
	}
	if !encrypted {
		return rc, nil
	}
	plain, err := backupfile.NewReader(rc, s.passphrase)
	if err != nil {
		rc.Close()
		return nil, err
	}
	return &decrypted{Reader: plain, src: rc}, nil
}

type decrypted struct {
	io.Reader
	src io.Closer
}

func (d *decrypted) Close() error { return d.src.Close() }

// Remove deletes every file of a run, in the directory and in the bucket,
// and aborts an upload of it that was left unfinished. A run that has none is
// not an error.
func (s *Storage) Remove(ctx context.Context, owner string, run int64) error {
	var errs []error
	if _, err := s.abortNoted(ctx, owner, run); err != nil {
		// The note is all that says which upload to abort: it stays, with
		// the directory around it, for the next start of the agent.
		errs = append(errs, err)
		entries, _ := os.ReadDir(s.runDir(owner, run))
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), uploadSuffix) {
				os.RemoveAll(filepath.Join(s.runDir(owner, run), e.Name()))
			}
		}
	} else if err := os.RemoveAll(s.runDir(owner, run)); err != nil {
		errs = append(errs, fmt.Errorf("remove backup directory: %w", err))
	}
	// The owner's directory goes with its last run; it is not empty otherwise,
	// and the error is the answer.
	os.Remove(filepath.Join(s.dir, owner))
	if s.s3 != nil {
		objects, err := s.s3.List(ctx, runKey(owner, run))
		if err != nil {
			errs = append(errs, err)
		}
		for _, o := range objects {
			if err := s.s3.Delete(ctx, o.Key); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// contextReader ends a copy when its context does: a file read does not
// notice a cancelled context by itself.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// installationKey is where a bucket records which installation writes to it.
// It lives under the state's owner, where no run's files can be: those sit
// one level down, under a run id.
const installationKey = StateOwner + "/installation"

// ErrForeignBucket means the bucket, under the configured prefix, holds the
// backups of another installation.
var ErrForeignBucket = errors.New("the bucket holds the backups of another Shipwick installation under this prefix: " +
	"to carry on as that installation, restore its state on this server (handbook: Restoring the agent's state); " +
	"to keep the two apart, set SHIPWICK_BACKUP_S3_PREFIX to a prefix of this server's own")

// Prepare makes sure this installation may write where the Storage writes,
// and returns the highest run id any destination holds files for.
//
// Run ids name directories and object keys, and a database knows only the ids
// it handed out itself. A server that was reinstalled starts counting at 1
// again; a database restored from a backup has forgotten the runs that came
// after it. Either would, in time, write its run 7 over the run 7 that is
// already in the bucket — quite possibly the backup somebody is about to
// restore. So the bucket is marked with the installation that writes to it,
// and anybody else is refused; and whoever is entitled to write is told how
// far the ids in use go, to count on from there.
func (s *Storage) Prepare(ctx context.Context, installation string) (int64, error) {
	var highest int64
	owners, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read backup directory: %w", err)
	}
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		runs, err := os.ReadDir(filepath.Join(s.dir, owner.Name()))
		if err != nil {
			return 0, fmt.Errorf("read backup directory: %w", err)
		}
		for _, run := range runs {
			if id, err := strconv.ParseInt(run.Name(), 10, 64); err == nil {
				highest = max(highest, id)
			}
		}
	}
	if s.s3 == nil {
		return highest, nil
	}

	marker, err := s.s3.Get(ctx, installationKey)
	switch {
	case errors.Is(err, ErrObjectNotFound):
		sum := sha256.Sum256([]byte(installation))
		if err := s.s3.Put(ctx, installationKey, strings.NewReader(installation), int64(len(installation)), hex.EncodeToString(sum[:])); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		found, err := io.ReadAll(io.LimitReader(marker, 1024))
		marker.Close()
		if err != nil {
			return 0, fmt.Errorf("download %s: %w", installationKey, err)
		}
		if strings.TrimSpace(string(found)) != installation {
			return 0, ErrForeignBucket
		}
	}

	objects, err := s.s3.List(ctx, "")
	if err != nil {
		return 0, err
	}
	for _, o := range objects {
		// <owner>/<run id>/<file>
		if parts := strings.Split(o.Key, "/"); len(parts) == 3 {
			if id, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				highest = max(highest, id)
			}
		}
	}
	return highest, nil
}
