package store

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/pkg/spec"
)

// Rotating the key: every sealed value is opened with the old key and sealed
// again with a new one, in one transaction, while the agent keeps running.
//
// The database and the key are two files, and no single step changes both. The
// order makes every point in between recoverable:
//
//  1. the new key is written next to the key file, under pendingKeySuffix,
//     and synced;
//  2. the transaction re-encrypts and commits, synced as well;
//  3. the new key is renamed over the key file.
//
// A crash before 2 leaves the database under the old key and a pending key
// that opens nothing; a crash before 3 leaves the database under the pending
// key. Open tells the two apart by trying which key opens the data
// (settleRotation) and finishes or discards the rotation accordingly.
//
// When the key comes from the environment there is no step 3: the agent
// cannot change the environment it will be started with next. The pending key
// stays where it is, the response carries it once, and the operator puts it
// into the environment. A start with the old key then finds a database it
// cannot open and a pending key that can, and refuses with exactly that
// explanation; a start with the new key removes the pending file.

// pendingKeySuffix names the new key while a rotation is under way.
const pendingKeySuffix = ".new"

// sealedColumn is a column whose values are sealed (crypto.go): which row
// each belongs to, and how the name it was bound to is built from that row's
// key.
type sealedColumn struct {
	table, key, column string
	name               func(key string) string
}

// sealedColumns is everything sealed outside deployment specs. A rotation
// re-encrypts what is listed here and nothing else: a sealed column missing
// from the list would be left under a key that no longer exists.
// TestEverySealedColumnIsKnownToRotation fails for a BLOB column that is
// neither here nor declared unsealed there.
//
// Deployment specs are not listed: their sealed values are inside JSON, and a
// rotation takes them through openSpec and sealSpec, so a field those two
// learn to seal is covered without an entry here.
var sealedColumns = []sealedColumn{
	{table: "secrets", key: "name", column: "value", name: func(name string) string { return name }},
	{table: "registries", key: "registry", column: "password", name: registryPasswordName},
	{table: "certificates", key: "hostname", column: "key", name: keyLabel},
	{table: "deployment_references", key: "deployment_id", column: "value", name: referencesName},
}

// keyring is the store's cipher.AEAD. It seals with the current key; it opens
// with the current key and, failing that, with the one before the last
// rotation: a reader that fetched a row just before the rotation committed
// still holds a value sealed under it.
type keyring struct {
	mu       sync.RWMutex
	current  cipher.AEAD
	previous cipher.AEAD
}

func (k *keyring) keys() (current, previous cipher.AEAD) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.current, k.previous
}

func (k *keyring) rotate(next cipher.AEAD) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.previous, k.current = k.current, next
}

func (k *keyring) NonceSize() int {
	current, _ := k.keys()
	return current.NonceSize()
}

func (k *keyring) Overhead() int {
	current, _ := k.keys()
	return current.Overhead()
}

func (k *keyring) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	current, _ := k.keys()
	return current.Seal(dst, nonce, plaintext, additionalData)
}

func (k *keyring) Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	current, previous := k.keys()
	plain, err := current.Open(dst, nonce, ciphertext, additionalData)
	if err != nil && previous != nil {
		if plain, perr := previous.Open(dst, nonce, ciphertext, additionalData); perr == nil {
			return plain, nil
		}
	}
	return plain, err
}

var (
	// ErrNotEncrypted is returned by RotateKey for a store that seals nothing,
	// or that was not told where its key is kept.
	ErrNotEncrypted = errors.New("this agent has no encryption key to rotate")
	// ErrRotationPending is returned by RotateKey when the key comes from the
	// environment and was rotated since the agent started: the environment
	// still holds the old key.
	ErrRotationPending = errors.New("the key was already rotated since the agent started")
)

// Rotation is what RotateKey did.
type Rotation struct {
	// Values counts the sealed values outside deployments (secrets, registry
	// passwords, ...); Deployments the deployment records whose environment
	// was re-encrypted.
	Values      int
	Deployments int
	// FromEnvironment says that the old key came from the environment. Key is
	// then the new one, in the form the environment variable takes, for the
	// operator to put there; it is empty otherwise.
	FromEnvironment bool
	Key             string
	// KeyFile is where the new key is now: the key file, or the pending file
	// when FromEnvironment.
	KeyFile string
}

// rotationFault lets tests stop a rotation where a crash could: it is called
// with "key-written" and "committed", and an error it returns is returned by
// RotateKey with everything left as it is at that point.
var rotationFault = func(point string) error { return nil }

// RotateKey replaces the encryption key with a new random one.
func (s *Store) RotateKey(ctx context.Context) (Rotation, error) {
	ring, _ := s.aead.(*keyring)
	if ring == nil {
		return Rotation{}, ErrNotEncrypted
	}
	// Without a key file only a database in memory may rotate: there the
	// key has to outlive nothing.
	if s.keyFile == "" && !s.inMemory {
		return Rotation{}, ErrNotEncrypted
	}

	s.keyMu.Lock()
	defer s.keyMu.Unlock()

	key := make([]byte, config.EncryptionKeySize)
	if _, err := rand.Read(key); err != nil {
		return Rotation{}, fmt.Errorf("generate encryption key: %w", err)
	}
	next, err := newAEAD(key)
	if err != nil {
		return Rotation{}, err
	}
	old, _ := ring.keys()

	out := Rotation{FromEnvironment: s.keyFromEnv, KeyFile: s.keyFile}
	pending := s.keyFile + pendingKeySuffix
	if s.keyFile != "" {
		if s.keyFromEnv {
			// The pending file of an earlier rotation is the only copy of
			// the key the database is under now, apart from this process.
			if _, err := os.Stat(pending); err == nil {
				return Rotation{}, ErrRotationPending
			}
			out.Key, out.KeyFile = config.FormatEncryptionKey(key), pending
		}
		if err := writeKeyFile(pending, key); err != nil {
			return Rotation{}, fmt.Errorf("write the new key: %w", err)
		}
	}
	if err := rotationFault("key-written"); err != nil {
		return Rotation{}, err
	}

	committed, err := s.reencrypt(ctx, old, next, &out)
	if err != nil {
		// A failed commit may or may not have reached the disk. The pending
		// key is only discarded when the old key demonstrably still opens
		// the data; otherwise the next start settles it.
		if s.keyFile != "" && (!committed || keyFits(context.WithoutCancel(ctx), s.db, &Store{aead: old}) == nil) {
			os.Remove(pending)
		}
		return Rotation{}, fmt.Errorf("rotate key: %w", err)
	}
	ring.rotate(next)
	s.rawKey = key
	if err := rotationFault("committed"); err != nil {
		return Rotation{}, err
	}

	if s.keyFile != "" && !s.keyFromEnv {
		if err := os.Rename(pending, s.keyFile); err != nil {
			return Rotation{}, fmt.Errorf("the key was rotated and the new one is in %s, but it could not be moved to %s: %w; the agent does that when it starts next", pending, s.keyFile, err)
		}
		syncDir(filepath.Dir(s.keyFile))
	}
	return out, nil
}

// reencrypt takes every sealed value from old to next in one transaction.
// committed reports whether Commit was reached, whatever it returned.
func (s *Store) reencrypt(ctx context.Context, old, next cipher.AEAD, out *Rotation) (committed bool, err error) {
	// One connection for the pragma and the transaction. synchronous is
	// NORMAL otherwise, under which a commit survives a crash of the agent
	// but not necessarily a power cut; this commit decides which key the
	// data needs, so it is synced before the key file is touched.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if s.keyFile != "" {
		if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = FULL`); err != nil {
			return false, err
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA synchronous = NORMAL`)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	for _, c := range sealedColumns {
		n, err := reencryptColumn(ctx, tx, c, old, next)
		if err != nil {
			return false, err
		}
		out.Values += n
	}
	if out.Deployments, err = reencryptSpecs(ctx, tx, &Store{aead: old}, &Store{aead: next}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// The table and column names below are from sealedColumns, never from input.
func reencryptColumn(ctx context.Context, tx *sql.Tx, c sealedColumn, old, next cipher.AEAD) (int, error) {
	values, err := readColumn(ctx, tx, c)
	if err != nil {
		return 0, err
	}
	for _, v := range values {
		name := c.name(v.key)
		plain, err := open(old, name, v.stored)
		if err != nil {
			return 0, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
		}
		sealed, err := seal(next, name, plain)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ?`, c.table, c.column, c.key), []byte(sealed), v.key); err != nil {
			return 0, err
		}
	}
	return len(values), nil
}

type columnValue struct{ key, stored string }

func readColumn(ctx context.Context, q querier, c sealedColumn) ([]columnValue, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf(`SELECT %s, %s FROM %s ORDER BY 1`, c.key, c.column, c.table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []columnValue
	for rows.Next() {
		var (
			key    string
			stored []byte
		)
		if err := rows.Scan(&key, &stored); err != nil {
			return nil, err
		}
		values = append(values, columnValue{key, string(stored)})
	}
	return values, rows.Err()
}

type storedSpec struct {
	id  int64
	raw string
}

func readSpecs(ctx context.Context, q querier) ([]storedSpec, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, spec FROM deployments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var specs []storedSpec
	for rows.Next() {
		var s storedSpec
		if err := rows.Scan(&s.id, &s.raw); err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, rows.Err()
}

// reencryptSpecs rewrites the specs that hold sealed values. old and next are
// stores only in that openSpec and sealSpec are methods: each carries one key
// and nothing else.
func reencryptSpecs(ctx context.Context, tx *sql.Tx, old, next *Store) (int, error) {
	specs, err := readSpecs(ctx, tx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range specs {
		raw, changed, err := reencryptSpec(s.raw, old, next)
		if err != nil {
			return 0, fmt.Errorf("deployment %d: %w", s.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET spec = ? WHERE id = ?`, raw, s.id); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// reencryptSpec returns a stored spec with its sealed values under next's
// key. Which values those are is openSpec's and sealSpec's knowledge, not
// this function's: it finds the fields that opening changed and replaces
// those, and only those, with what sealing made of them. The rest of the
// record stays byte for byte what was written when it was deployed.
func reencryptSpec(raw string, old, next *Store) (string, bool, error) {
	// Decoded twice: openSpec works in place, on maps the copies would share.
	var stored, opened spec.App
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return "", false, fmt.Errorf("decode spec: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		return "", false, fmt.Errorf("decode spec: %w", err)
	}
	if err := old.openSpec(&opened); err != nil {
		return "", false, err
	}
	before, err := specFields(stored)
	if err != nil {
		return "", false, err
	}
	plain, err := specFields(opened)
	if err != nil {
		return "", false, err
	}
	resealed, err := next.sealSpec(opened)
	if err != nil {
		return "", false, err
	}
	after, err := specFields(resealed)
	if err != nil {
		return "", false, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", false, fmt.Errorf("decode spec: %w", err)
	}
	changed := false
	for name, value := range plain {
		if bytes.Equal(value, before[name]) {
			continue
		}
		fields[name] = after[name]
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

func specFields(app spec.App) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(app)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(data, &fields)
	return fields, err
}

// keyFits reports, as an error, the first sealed value that the key carried
// by with does not open.
func keyFits(ctx context.Context, q querier, with *Store) error {
	for _, c := range sealedColumns {
		values, err := readColumn(ctx, q, c)
		if err != nil {
			return err
		}
		for _, v := range values {
			if _, err := open(with.aead, c.name(v.key), v.stored); err != nil {
				return errKeyMismatch
			}
		}
	}
	specs, err := readSpecs(ctx, q)
	if err != nil {
		return err
	}
	for _, s := range specs {
		var app spec.App
		if err := json.Unmarshal([]byte(s.raw), &app); err != nil {
			return fmt.Errorf("decode spec of deployment %d: %w", s.id, err)
		}
		if err := with.openSpec(&app); err != nil {
			return errKeyMismatch
		}
	}
	return nil
}

// settleRotation deals with the pending key a rotation left behind, when Open
// finds one: see the order at the top of this file.
func (s *Store) settleRotation(ctx context.Context) error {
	ring, _ := s.aead.(*keyring)
	if ring == nil || s.keyFile == "" {
		return nil
	}
	pending := s.keyFile + pendingKeySuffix
	data, err := os.ReadFile(pending)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read %s: %w", pending, err)
	}

	current, _ := ring.keys()
	if keyFits(ctx, s.db, &Store{aead: current}) == nil {
		// The rotation never committed, or it did and the environment has
		// the new key by now. Either way the file has served.
		if err := os.Remove(pending); err != nil {
			return fmt.Errorf("remove %s: %w", pending, err)
		}
		if s.keyFromEnv {
			s.log.Info("removed the pending encryption key: the agent was started with the key the database is under", "file", pending)
		} else {
			s.log.Info("discarded the key of a rotation that did not complete; the encryption key is unchanged", "file", pending)
		}
		return nil
	}

	// A file cut short while it was written decodes to nothing; the
	// transaction had not begun then, so the mismatch is not this file's.
	unsettled := func() error {
		return fmt.Errorf("%w, and neither does the key a rotation left in %s: restore %s from your backup or set %s", errKeyMismatch, pending, s.keyFile, config.EnvEncryptionKey)
	}
	key, err := config.ParseEncryptionKey(strings.TrimSpace(string(data)))
	if err != nil {
		return unsettled()
	}
	next, err := newAEAD(key)
	if err != nil {
		return err
	}
	if keyFits(ctx, s.db, &Store{aead: next}) != nil {
		return unsettled()
	}
	if s.keyFromEnv {
		return fmt.Errorf("the encryption key was rotated, and %s still holds the old one: the new key is in %s. "+
			"Put it where the agent's environment is set (/opt/shipwick/.env in a standard installation) as %s and start the agent again; it removes the file once it has started with the new key",
			config.EnvEncryptionKey, pending, config.EnvEncryptionKey)
	}
	if err := os.Rename(pending, s.keyFile); err != nil {
		return fmt.Errorf("complete the key rotation: %w", err)
	}
	syncDir(filepath.Dir(s.keyFile))
	ring.rotate(next)
	s.rawKey = key
	s.log.Info("completed a key rotation that was interrupted after the database had been re-encrypted", "file", s.keyFile)
	return nil
}

// writeKeyFile writes a key the way the key file holds it, and does not
// return before it is on disk.
func writeKeyFile(path string, key []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(config.FormatEncryptionKey(key) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes a created or renamed entry of dir durable. Best effort: not
// every platform lets a directory be synced.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// PendingKeyFile is where a rotated key waits when the key comes from the
// environment.
func (s *Store) PendingKeyFile() string {
	return s.keyFile + pendingKeySuffix
}
