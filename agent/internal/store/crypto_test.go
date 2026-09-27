package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/config"
)

func TestSealRoundTripsAndBindsTheVariableName(t *testing.T) {
	aead, err := newAEAD(testOptions.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal(aead, "DB_PASSWORD", "hunter2")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !isSealed(sealed) || strings.Contains(sealed, "hunter2") {
		t.Errorf("sealed value %q must carry the prefix and not the plaintext", sealed)
	}
	again, _ := seal(aead, "DB_PASSWORD", "hunter2")
	if again == sealed {
		t.Error("two seals of the same value must differ: the nonce is random")
	}

	got, err := open(aead, "DB_PASSWORD", sealed)
	if err != nil || got != "hunter2" {
		t.Errorf("open = %q, %v", got, err)
	}
	if _, err := open(aead, "OTHER_PASSWORD", sealed); err == nil {
		t.Error("a ciphertext must not decrypt under another variable's name")
	}
	if _, err := open(aead, "DB_PASSWORD", "enc1:not base64!"); err == nil {
		t.Error("malformed ciphertext must be refused")
	}
}

func TestOpenRejectsATamperedValue(t *testing.T) {
	aead, _ := newAEAD(testOptions.EncryptionKey)
	sealed, _ := seal(aead, "KEY", "value")

	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, encPrefix))
	raw[len(raw)-1] ^= 0x01
	tampered := encPrefix + base64.StdEncoding.EncodeToString(raw)
	if _, err := open(aead, "KEY", tampered); err == nil {
		t.Error("a flipped byte must not go unnoticed")
	}

	other, _ := newAEAD([]byte("another-encryption-key-32-bytes!"))
	if _, err := open(other, "KEY", sealed); err == nil {
		t.Error("another key must not decrypt the value")
	}
}

func rawSpec(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRowContext(context.Background(), `SELECT spec FROM deployments WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read stored spec: %v", err)
	}
	return raw
}

func TestEnvValuesAreStoredEncryptedAndReadBackClear(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	app := testApp("api", "nginx:1")
	app.Env = map[string]string{"KEY": "value", "DB_PASSWORD": "hunter2"}
	d, err := s.CreateDeployment(ctx, app, time.Now())
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if app.Env["DB_PASSWORD"] != "hunter2" || d.Spec.Env["DB_PASSWORD"] != "hunter2" {
		t.Error("the caller's spec must keep its clear values: the engine needs them")
	}

	raw := rawSpec(t, s, d.ID)
	if strings.Contains(raw, "hunter2") || strings.Contains(raw, `"value"`) {
		t.Errorf("stored spec holds a plaintext env value: %s", raw)
	}
	if !strings.Contains(raw, `"DB_PASSWORD":"enc1:`) || !strings.Contains(raw, `"image":"nginx:1"`) {
		t.Errorf("only the values are encrypted; names and the rest stay readable: %s", raw)
	}

	got, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Spec.Env["DB_PASSWORD"] != "hunter2" || got.Spec.Env["KEY"] != "value" {
		t.Errorf("env did not round trip: %+v", got.Spec.Env)
	}
	list, err := s.ListDeployments(ctx, DeploymentFilter{Application: "api"})
	if err != nil || len(list) != 1 || list[0].Spec.Env["KEY"] != "value" {
		t.Errorf("ListDeployments must decrypt too: %+v, %v", list, err)
	}
}

func TestAValueMovedToAnotherVariableIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	app := testApp("api", "nginx:1")
	app.Env = map[string]string{"A": "one", "B": "two"}
	d, _ := s.CreateDeployment(ctx, app, time.Now())

	var fields map[string]json.RawMessage
	json.Unmarshal([]byte(rawSpec(t, s, d.ID)), &fields)
	var env map[string]string
	json.Unmarshal(fields["env"], &env)
	env["A"], env["B"] = env["B"], env["A"]
	fields["env"], _ = json.Marshal(env)
	swapped, _ := json.Marshal(fields)
	if _, err := s.db.ExecContext(ctx, `UPDATE deployments SET spec = ? WHERE id = ?`, string(swapped), d.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetDeployment(ctx, d.ID); err == nil {
		t.Error("swapping two ciphertexts in the database must not yield swapped values")
	}
}

func TestOpenEncryptsTheEnvValuesOfEarlierReleasesOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")
	quiet := Options{EncryptionKey: testOptions.EncryptionKey, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Rows as a release without encryption wrote them.
	s, err := Open(ctx, path, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ts := formatTime(time.Now())
	const legacy = `{"name":"api","image":"nginx:1","replicas":1,"env":{"DB_PASSWORD":"hunter2","MODE":"prod"}}`
	const noEnv = `{"name":"web","image":"nginx:1","replicas":1}`
	for _, stmt := range []string{
		`INSERT INTO applications (id, name, created_at, updated_at) VALUES (1, 'api', '` + ts + `', '` + ts + `')`,
		`INSERT INTO applications (id, name, created_at, updated_at) VALUES (2, 'web', '` + ts + `', '` + ts + `')`,
		`INSERT INTO deployments (id, application_id, sequence, version, image, spec, status, started_at)
		 VALUES (1, 1, 1, '1', 'nginx:1', '` + legacy + `', 'ACTIVE', '` + ts + `')`,
		`INSERT INTO deployments (id, application_id, sequence, version, image, spec, status, started_at)
		 VALUES (2, 2, 1, '1', 'nginx:1', '` + noEnv + `', 'ACTIVE', '` + ts + `')`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	s.Close()

	s, err = Open(ctx, path, quiet)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	encrypted := rawSpec(t, s, 1)
	if strings.Contains(encrypted, "hunter2") || strings.Contains(encrypted, `"prod"`) {
		t.Errorf("legacy values were left in clear: %s", encrypted)
	}
	if !strings.Contains(encrypted, `"image":"nginx:1"`) {
		t.Errorf("the rest of the spec must be kept: %s", encrypted)
	}
	if got := rawSpec(t, s, 2); got != noEnv {
		t.Errorf("a spec without env must not be rewritten: %s", got)
	}
	d, err := s.GetDeployment(ctx, 1)
	if err != nil || d.Spec.Env["DB_PASSWORD"] != "hunter2" || d.Spec.Env["MODE"] != "prod" {
		t.Errorf("legacy values must read back clear: %+v, %v", d.Spec.Env, err)
	}
	s.Close()

	// A third start finds nothing to do. Nonces are random, so an unchanged
	// row proves it was not rewritten.
	s, err = Open(ctx, path, quiet)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer s.Close()
	if again := rawSpec(t, s, 1); again != encrypted {
		t.Error("already encrypted rows must be left alone")
	}
}

func TestOpenRefusesAKeyThatDoesNotMatchTheDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")

	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, testApp("api", "nginx:1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err = Open(ctx, path, Options{EncryptionKey: []byte("another-encryption-key-32-bytes!")})
	if !errors.Is(err, errKeyMismatch) {
		t.Fatalf("err = %v, want the key mismatch", err)
	}
	msg := err.Error()
	for _, want := range []string{"does not match", "KEY", filepath.Join(filepath.Dir(path), config.EncryptionKeyFile), config.EnvEncryptionKey, "backup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "value") {
		t.Errorf("error must not contain the env value: %q", msg)
	}
}

func TestOpenRefusesAFileDatabaseWithoutAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shipwick.db")
	if _, err := Open(context.Background(), path, Options{}); err == nil {
		t.Error("a database on disk must never be written unencrypted")
	}
	if _, err := Open(context.Background(), path, Options{EncryptionKey: []byte("short")}); err == nil {
		t.Error("a key of the wrong length must be refused")
	}
	s, err := Open(context.Background(), ":memory:", Options{})
	if err != nil {
		t.Fatalf("an in-memory database may go without a key: %v", err)
	}
	s.Close()
}
