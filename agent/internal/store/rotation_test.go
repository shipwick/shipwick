package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/config"
)

// keyed is a database on disk with its key file next to it, the way the
// agent's data directory holds them.
type keyed struct {
	t       *testing.T
	dir     string
	keyFile string
	pending string
}

func newKeyed(t *testing.T) *keyed {
	t.Helper()
	dir := t.TempDir()
	k := &keyed{t: t, dir: dir, keyFile: filepath.Join(dir, config.EncryptionKeyFile)}
	k.pending = k.keyFile + pendingKeySuffix
	return k
}

// open starts the store the way the agent does: with the key the key file
// holds, created on the first start.
func (k *keyed) open() (*Store, error) {
	k.t.Helper()
	key, _, err := config.ResolveEncryptionKey(config.Config{DataDir: k.dir})
	if err != nil {
		k.t.Fatalf("resolve key: %v", err)
	}
	return Open(context.Background(), filepath.Join(k.dir, "shipwick.db"), Options{EncryptionKey: key, KeyFile: k.keyFile})
}

// openWith starts it with a key from the environment.
func (k *keyed) openWith(key string) (*Store, error) {
	k.t.Helper()
	raw, err := config.ParseEncryptionKey(key)
	if err != nil {
		k.t.Fatalf("parse key: %v", err)
	}
	return Open(context.Background(), filepath.Join(k.dir, "shipwick.db"), Options{EncryptionKey: raw, KeyFile: k.keyFile, KeyFromEnvironment: true})
}

func (k *keyed) mustOpen() *Store {
	k.t.Helper()
	s, err := k.open()
	if err != nil {
		k.t.Fatalf("Open: %v", err)
	}
	k.t.Cleanup(func() { s.Close() })
	return s
}

func (k *keyed) keyOnDisk() string {
	k.t.Helper()
	data, err := os.ReadFile(k.keyFile)
	if err != nil {
		k.t.Fatalf("read key file: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fill stores one of every kind of sealed value and returns the deployment.
func fill(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	app := testApp("api", "ghcr.io/company/api:1")
	app.Env = map[string]string{"DB_PASSWORD": "hunter2", "PLAIN": "value"}
	d, err := s.CreateDeployment(ctx, app, now)
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if err := s.SetSecret(ctx, "API_KEY", "s3cr3t", now); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := s.SetRegistry(ctx, "ghcr.io", "octocat", "ghp_token", now); err != nil {
		t.Fatalf("SetRegistry: %v", err)
	}
	return d.ID
}

// assertFilled reads back, through the store, everything fill wrote.
func assertFilled(t *testing.T, s *Store, deployment int64) {
	t.Helper()
	ctx := context.Background()
	d, err := s.GetDeployment(ctx, deployment)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.Spec.Env["DB_PASSWORD"] != "hunter2" || d.Spec.Env["PLAIN"] != "value" {
		t.Errorf("env = %v", d.Spec.Env)
	}
	secrets, err := s.GetSecrets(ctx, []string{"API_KEY"})
	if err != nil || secrets["API_KEY"] != "s3cr3t" {
		t.Errorf("secret = %q, %v", secrets["API_KEY"], err)
	}
	username, password, found, err := s.RegistryCredential(ctx, "ghcr.io")
	if err != nil || !found || username != "octocat" || password != "ghp_token" {
		t.Errorf("registry credential = %q, %q, %v, %v", username, password, found, err)
	}
}

func rawColumn(t *testing.T, s *Store, query string) string {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), query).Scan(&raw); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return string(raw)
}

func TestRotateKeyReencryptsEveryKindOfSealedValueAndReplacesTheKeyFile(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	id := fill(t, s)
	oldKey := k.keyOnDisk()
	oldAEAD, _ := s.aead.(*keyring).keys()
	before := map[string]string{
		"spec":     rawSpec(t, s, id),
		"secret":   rawColumn(t, s, `SELECT value FROM secrets`),
		"registry": rawColumn(t, s, `SELECT password FROM registries`),
	}

	rotation, err := s.RotateKey(context.Background())
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if rotation.Values != 2 || rotation.Deployments != 1 {
		t.Errorf("rotation = %+v, want 2 values and 1 deployment", rotation)
	}
	if rotation.FromEnvironment || rotation.Key != "" || rotation.KeyFile != k.keyFile {
		t.Errorf("a key kept in the data directory is replaced there and never returned: %+v", rotation)
	}
	if k.keyOnDisk() == oldKey {
		t.Error("the key file still holds the old key")
	}
	if exists(k.pending) {
		t.Error("the pending key file must be gone once the rotation is complete")
	}

	// The running store reads everything back, and nothing on disk still
	// opens with the old key.
	assertFilled(t, s, id)
	after := map[string]string{
		"spec":     rawSpec(t, s, id),
		"secret":   rawColumn(t, s, `SELECT value FROM secrets`),
		"registry": rawColumn(t, s, `SELECT password FROM registries`),
	}
	for what := range before {
		if before[what] == after[what] {
			t.Errorf("%s was not re-encrypted", what)
		}
	}
	if _, err := open(oldAEAD, "API_KEY", after["secret"]); err == nil {
		t.Error("the secret still opens with the old key")
	}
	if _, err := open(oldAEAD, registryPasswordName("ghcr.io"), after["registry"]); err == nil {
		t.Error("the registry password still opens with the old key")
	}
	if keyFits(context.Background(), s.db, &Store{aead: oldAEAD}) == nil {
		t.Error("the old key still opens the database")
	}

	// And so does an agent started afterwards, with the key now in the file.
	s.Close()
	again := k.mustOpen()
	assertFilled(t, again, id)

	// A second rotation works on what the first one wrote.
	if _, err := again.RotateKey(context.Background()); err != nil {
		t.Fatalf("second RotateKey: %v", err)
	}
	assertFilled(t, again, id)
}

func TestRotateKeyChangesNothingInASpecButItsSealedValues(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	id := fill(t, s)
	// A field this agent does not know, as a newer agent's record would have:
	// the record is the deployment's history and is not rewritten wholesale.
	if _, err := s.db.Exec(`UPDATE deployments SET spec = json_set(spec, '$.from_the_future', json('{"kept": true}')) WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	fields := func() map[string]json.RawMessage {
		var f map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rawSpec(t, s, id)), &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	before := fields()

	if _, err := s.RotateKey(context.Background()); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	after := fields()
	if len(after) != len(before) {
		t.Errorf("fields before: %d, after: %d", len(before), len(after))
	}
	for name, value := range before {
		if name == "env" {
			if string(after[name]) == string(value) {
				t.Error("env was not re-encrypted")
			}
			continue
		}
		if string(after[name]) != string(value) {
			t.Errorf("%s changed: %s → %s", name, value, after[name])
		}
	}
}

func TestRotateKeyLeavesDeploymentsWithoutSealedValuesAlone(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	app := testApp("plain", "nginx:1")
	app.Env = nil
	d, err := s.CreateDeployment(context.Background(), app, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before := rawSpec(t, s, d.ID)
	rotation, err := s.RotateKey(context.Background())
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if rotation.Deployments != 0 || rawSpec(t, s, d.ID) != before {
		t.Errorf("a spec with nothing sealed must stay byte for byte; rotation = %+v", rotation)
	}
}

var errCrash = errors.New("simulated crash")

// crashAt makes the next rotation stop at point, as the process dying there
// would.
func crashAt(t *testing.T, point string) {
	t.Helper()
	rotationFault = func(p string) error {
		if p == point {
			return errCrash
		}
		return nil
	}
	t.Cleanup(func() { rotationFault = func(string) error { return nil } })
}

func TestACrashBeforeTheRotationCommitsKeepsTheOldKey(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	id := fill(t, s)
	oldKey := k.keyOnDisk()

	crashAt(t, "key-written")
	if _, err := s.RotateKey(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("RotateKey = %v, want the simulated crash", err)
	}
	if !exists(k.pending) {
		t.Fatal("the new key must be on disk before the database is touched")
	}
	s.Close()

	again := k.mustOpen()
	assertFilled(t, again, id)
	if k.keyOnDisk() != oldKey {
		t.Error("the key file changed although the rotation never committed")
	}
	if exists(k.pending) {
		t.Error("the pending key of a rotation that never committed must be discarded")
	}
}

func TestACrashAfterTheRotationCommittedIsCompletedOnTheNextStart(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	id := fill(t, s)
	oldKey := k.keyOnDisk()

	crashAt(t, "committed")
	if _, err := s.RotateKey(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("RotateKey = %v, want the simulated crash", err)
	}
	if k.keyOnDisk() != oldKey || !exists(k.pending) {
		t.Fatal("the crash point is after the commit and before the rename")
	}
	pendingKey, _ := os.ReadFile(k.pending)
	s.Close()

	// The key file still holds the old key, which no longer opens the data.
	again := k.mustOpen()
	assertFilled(t, again, id)
	if k.keyOnDisk() != strings.TrimSpace(string(pendingKey)) {
		t.Error("the pending key must have become the key file")
	}
	if exists(k.pending) {
		t.Error("the pending key file must be gone")
	}
	again.Close()

	// And the start after that is an ordinary one.
	third := k.mustOpen()
	assertFilled(t, third, id)
}

func TestAPendingKeyCutShortWhileItWasWrittenIsDiscarded(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	id := fill(t, s)
	s.Close()
	if err := os.WriteFile(k.pending, []byte("0123abc"), 0o600); err != nil {
		t.Fatal(err)
	}

	again := k.mustOpen()
	assertFilled(t, again, id)
	if exists(k.pending) {
		t.Error("the truncated pending key must be removed")
	}
}

func TestAStartWithNeitherKeyFittingSaysSoAndKeepsBothFiles(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	fill(t, s)
	s.Close()
	stranger := strings.Repeat("ab", config.EncryptionKeySize)
	os.WriteFile(k.keyFile, []byte(stranger+"\n"), 0o600)
	os.WriteFile(k.pending, []byte(strings.Repeat("cd", config.EncryptionKeySize)+"\n"), 0o600)

	_, err := k.open()
	if !errors.Is(err, errKeyMismatch) || !strings.Contains(err.Error(), k.pending) {
		t.Fatalf("Open = %v, want a key mismatch that names the pending file", err)
	}
	if !exists(k.pending) || k.keyOnDisk() != stranger {
		t.Error("nothing may be removed while no key opens the data")
	}
}

func TestRotateKeyWithTheKeyInTheEnvironmentReturnsItOnceAndKeepsACopyUntilTheRestart(t *testing.T) {
	k := newKeyed(t)
	oldKey := strings.Repeat("0f", config.EncryptionKeySize)
	s, err := k.openWith(oldKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	id := fill(t, s)

	rotation, err := s.RotateKey(context.Background())
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if !rotation.FromEnvironment || len(rotation.Key) != 2*config.EncryptionKeySize || rotation.Key == oldKey {
		t.Fatalf("rotation = %+v, want the new key for the operator", rotation)
	}
	if rotation.KeyFile != k.pending {
		t.Errorf("KeyFile = %s, want the pending file", rotation.KeyFile)
	}
	if data, _ := os.ReadFile(k.pending); strings.TrimSpace(string(data)) != rotation.Key {
		t.Error("the pending file must hold the key that was returned: a lost response must not lose the key")
	}
	if exists(k.keyFile) {
		t.Error("the key file is not this agent's to write: its key comes from the environment")
	}
	// The agent keeps working with the new key until it restarts.
	assertFilled(t, s, id)
	if err := s.SetSecret(context.Background(), "LATER", "v", time.Now()); err != nil {
		t.Fatal(err)
	}

	// A second rotation would overwrite the only copy of the current key.
	if _, err := s.RotateKey(context.Background()); !errors.Is(err, ErrRotationPending) {
		t.Errorf("second RotateKey = %v, want ErrRotationPending", err)
	}
	s.Close()

	// Restarted with the old key still in the environment: refused, with
	// what happened and where the new key is.
	_, err = k.openWith(oldKey)
	if err == nil {
		t.Fatal("a start with the old key must be refused")
	}
	for _, want := range []string{"was rotated", config.EnvEncryptionKey, k.pending} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), rotation.Key) {
		t.Error("the key itself must not be in the error: errors are logged")
	}
	if !exists(k.pending) {
		t.Fatal("the pending key must survive the refused start")
	}

	// Restarted with the new key: an ordinary start, and the copy goes.
	again, err := k.openWith(rotation.Key)
	if err != nil {
		t.Fatalf("Open with the new key: %v", err)
	}
	t.Cleanup(func() { again.Close() })
	assertFilled(t, again, id)
	if exists(k.pending) {
		t.Error("the pending key must be removed once the agent has started with it")
	}
	if _, err := again.RotateKey(context.Background()); err != nil {
		t.Errorf("rotation after the restart: %v", err)
	}
}

func TestRotateKeyNeedsAKeyFileExceptInMemory(t *testing.T) {
	s := openTest(t)
	id := fill(t, s)
	if _, err := s.RotateKey(context.Background()); err != nil {
		t.Fatalf("RotateKey in memory: %v", err)
	}
	assertFilled(t, s, id)

	onDisk, err := Open(context.Background(), filepath.Join(t.TempDir(), "shipwick.db"), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer onDisk.Close()
	if _, err := onDisk.RotateKey(context.Background()); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("RotateKey without a key file = %v, want ErrNotEncrypted: the new key would be lost with the process", err)
	}
}

func TestAValueReadBeforeTheRotationStillOpensAfterIt(t *testing.T) {
	s := openTest(t)
	sealed, err := seal(s.aead, "NAME", "value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := open(s.aead, "NAME", sealed); err != nil || got != "value" {
		t.Errorf("open after rotation = %q, %v", got, err)
	}
	current, _ := s.aead.(*keyring).keys()
	if _, err := open(current, "NAME", sealed); err == nil {
		t.Error("the new key alone must not open a value sealed under the old one")
	}
}

func TestValuesWrittenWhileTheKeyRotatesAreAllUnderTheFinalKey(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	ctx := context.Background()

	// One goroutine per kind of writer, so that none is paced by another's
	// waiting for a rotation.
	const each = 40
	writers := []func(i int) error{
		func(i int) error { return s.SetSecret(ctx, fmt.Sprintf("S_%d", i), "value", time.Now()) },
		func(i int) error {
			return s.SetRegistry(ctx, fmt.Sprintf("r%d.example.com", i), "user", "password", time.Now())
		},
		func(i int) error {
			app := testApp("app", "nginx:1")
			app.Env = map[string]string{"N": fmt.Sprint(i)}
			_, err := s.CreateDeployment(ctx, app, time.Now())
			return err
		},
	}
	var wg sync.WaitGroup
	for _, write := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if err := write(i); err != nil {
					t.Errorf("write %d: %v", i, err)
				}
			}
		}()
	}
	// Rotations back to back for as long as the writers run: a writer then
	// finds the database busy with one almost every time it has sealed.
	written := make(chan struct{})
	go func() { wg.Wait(); close(written) }()
	for rotating := true; rotating; {
		select {
		case <-written:
			rotating = false
		default:
		}
		if _, err := s.RotateKey(ctx); err != nil {
			t.Fatalf("RotateKey: %v", err)
		}
	}
	s.Close()

	// A fresh start knows only the final key: a value that had been sealed
	// under an earlier one and written after its rotation would not open.
	again := k.mustOpen()
	current, _ := again.aead.(*keyring).keys()
	if err := keyFits(ctx, again.db, &Store{aead: current}); err != nil {
		t.Fatalf("not every value is under the final key: %v", err)
	}
	var secrets, deployments int
	again.db.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&secrets)
	again.db.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&deployments)
	if secrets != each || deployments != each {
		t.Errorf("secrets = %d, deployments = %d, want %d each", secrets, deployments, each)
	}
}

// Sealed values are BLOB columns by convention, which is what lets this test
// find one that rotation has not been told about. A BLOB column that holds
// something else is declared here.
func TestEverySealedColumnIsKnownToRotation(t *testing.T) {
	notSealed := map[string]bool{
		"tokens.hash": true, // SHA-256 of an API token
	}

	s := openTest(t)
	rows, err := s.db.Query(`SELECT m.name, p.name, p.type FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]string{}
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatal(err)
		}
		columns[table+"."+column] = strings.ToUpper(typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	listed := map[string]bool{}
	for _, c := range sealedColumns {
		listed[c.table+"."+c.column] = true
		if columns[c.table+"."+c.column] != "BLOB" {
			t.Errorf("sealedColumns lists %s.%s, which is not a BLOB column of the schema", c.table, c.column)
		}
		if _, ok := columns[c.table+"."+c.key]; !ok {
			t.Errorf("sealedColumns names %s.%s as the key, which the schema does not have", c.table, c.key)
		}
		if c.name == nil {
			t.Errorf("sealedColumns: %s.%s does not say what its values are bound to", c.table, c.column)
		}
	}
	for column, typ := range columns {
		if typ == "BLOB" && !listed[column] && !notSealed[column] {
			t.Errorf("%s is a BLOB column that key rotation does not know: add it to sealedColumns in rotation.go if its values are sealed, or to notSealed in this test if they are not", column)
		}
	}
}
