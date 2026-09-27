package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestSecretsRoundTripAndAreListedWithoutValues(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	if err := s.SetSecret(ctx, "DB_PASSWORD", "hunter2", now); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := s.SetSecret(ctx, "API_KEY", "k-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	values, err := s.GetSecrets(ctx, []string{"DB_PASSWORD", "API_KEY", "MISSING"})
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if values["DB_PASSWORD"] != "hunter2" || values["API_KEY"] != "k-1" {
		t.Errorf("values = %v", values)
	}
	if _, ok := values["MISSING"]; ok {
		t.Error("a name that is not stored must be absent, not empty")
	}

	list, err := s.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(list) != 2 || list[0].Name != "API_KEY" || list[1].Name != "DB_PASSWORD" {
		t.Fatalf("list = %+v, want API_KEY then DB_PASSWORD", list)
	}
	if !list[1].CreatedAt.Equal(now) || !list[1].UpdatedAt.Equal(now) {
		t.Errorf("timestamps = %v / %v, want %v", list[1].CreatedAt, list[1].UpdatedAt, now)
	}
	if s := fmt.Sprintf("%+v", list); strings.Contains(s, "hunter2") || strings.Contains(s, "k-1") {
		t.Errorf("the list carries a value: %s", s)
	}
}

func TestSetSecretReplacesTheValueAndKeepsCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	later := created.Add(time.Hour)

	s.SetSecret(ctx, "DB_PASSWORD", "old", created)
	if err := s.SetSecret(ctx, "DB_PASSWORD", "new", later); err != nil {
		t.Fatalf("SetSecret again: %v", err)
	}
	values, _ := s.GetSecrets(ctx, []string{"DB_PASSWORD"})
	if values["DB_PASSWORD"] != "new" {
		t.Errorf("value = %q, want the replacement", values["DB_PASSWORD"])
	}
	list, _ := s.ListSecrets(ctx)
	if len(list) != 1 || !list[0].CreatedAt.Equal(created) || !list[0].UpdatedAt.Equal(later) {
		t.Errorf("list = %+v: created_at must stay, updated_at must move", list)
	}
}

func TestSecretsAreEncryptedAtRestAndBoundToTheirName(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetSecret(ctx, "A", "one", time.Now())
	s.SetSecret(ctx, "B", "two", time.Now())

	var rawA, rawB []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'A'`).Scan(&rawA); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'B'`).Scan(&rawB)
	if strings.Contains(string(rawA), "one") || !isSealed(string(rawA)) {
		t.Errorf("the row holds %q: want ciphertext with the enc1 prefix", rawA)
	}

	// Swapping two rows' ciphertexts must not hand A the value of B.
	if _, err := s.db.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE name = 'A'`, rawB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSecrets(ctx, []string{"A"}); err == nil {
		t.Error("a ciphertext moved to another name must be refused")
	}

	// A wrong key at open time is caught by the env values; a secret read
	// under another key fails the same way.
	other := &Store{db: s.db}
	other.aead, _ = newAEAD([]byte("another-encryption-key-32-bytes!"))
	if _, err := other.GetSecrets(ctx, []string{"B"}); err == nil {
		t.Error("another key must not decrypt a secret")
	}
}

func TestDeleteSecret(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetSecret(ctx, "A", "one", time.Now())

	if err := s.DeleteSecret(ctx, "A"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := s.DeleteSecret(ctx, "A"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again: err = %v, want ErrNotFound", err)
	}
	if list, _ := s.ListSecrets(ctx); len(list) != 0 {
		t.Errorf("list = %+v, want empty", list)
	}
}

func TestSetSecretRefusesMoreThanTheLimit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	for i := 0; i < api.MaxSecrets; i++ {
		if err := s.SetSecret(ctx, fmt.Sprintf("S_%d", i), "v", now); err != nil {
			t.Fatalf("secret %d: %v", i, err)
		}
	}
	if err := s.SetSecret(ctx, "ONE_MORE", "v", now); !errors.Is(err, ErrTooManySecrets) {
		t.Errorf("err = %v, want ErrTooManySecrets", err)
	}
	// Replacing an existing one is not a new secret.
	if err := s.SetSecret(ctx, "S_0", "changed", now); err != nil {
		t.Errorf("replacing at the limit: %v", err)
	}
}

func TestSecretsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/shipwick.db"
	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecret(ctx, "DB_PASSWORD", "hunter2", time.Now())
	s.Close()

	s, err = Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	values, err := s.GetSecrets(ctx, []string{"DB_PASSWORD"})
	if err != nil || values["DB_PASSWORD"] != "hunter2" {
		t.Errorf("values = %v, %v", values, err)
	}
}
