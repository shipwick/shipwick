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

	if err := s.SetSecret(ctx, "", "DB_PASSWORD", "hunter2", now); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := s.SetSecret(ctx, "", "API_KEY", "k-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	values, err := s.GetSecrets(ctx, "", []string{"DB_PASSWORD", "API_KEY", "MISSING"})
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if values["DB_PASSWORD"].Value != "hunter2" || values["API_KEY"].Value != "k-1" || values["API_KEY"].Application != "" {
		t.Errorf("values = %v", values)
	}
	if _, ok := values["MISSING"]; ok {
		t.Error("a name that is not stored must be absent, not empty")
	}

	list, err := s.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(list) != 2 || list[0].Name != "API_KEY" || list[1].Name != "DB_PASSWORD" || list[0].Application != "" {
		t.Fatalf("list = %+v, want API_KEY then DB_PASSWORD, both the server's", list)
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

	s.SetSecret(ctx, "", "DB_PASSWORD", "old", created)
	if err := s.SetSecret(ctx, "", "DB_PASSWORD", "new", later); err != nil {
		t.Fatalf("SetSecret again: %v", err)
	}
	values, _ := s.GetSecrets(ctx, "", []string{"DB_PASSWORD"})
	if values["DB_PASSWORD"].Value != "new" {
		t.Errorf("value = %q, want the replacement", values["DB_PASSWORD"].Value)
	}
	list, _ := s.ListSecrets(ctx)
	if len(list) != 1 || !list[0].CreatedAt.Equal(created) || !list[0].UpdatedAt.Equal(later) {
		t.Errorf("list = %+v: created_at must stay, updated_at must move", list)
	}
}

func TestAnApplicationAndTheServerEachHaveASecretOfTheSameName(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, set := range []struct{ application, name, value string }{
		{"", "DB_PASSWORD", "shared"},
		{"api", "DB_PASSWORD", "api-s-own"},
		{"web", "DB_PASSWORD", "web-s-own"},
		{"api", "ONLY_API", "api-only"},
	} {
		if err := s.SetSecret(ctx, set.application, set.name, set.value, now); err != nil {
			t.Fatalf("SetSecret %s/%s: %v", set.application, set.name, err)
		}
	}
	// Setting one scope again replaces that scope only.
	if err := s.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-newer", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		application string
		names       []string
		want        map[string]ResolvedSecret
	}{
		{"api", []string{"DB_PASSWORD", "ONLY_API"}, map[string]ResolvedSecret{"DB_PASSWORD": {"api-s-newer", "api"}, "ONLY_API": {"api-only", "api"}}},
		{"web", []string{"DB_PASSWORD", "ONLY_API"}, map[string]ResolvedSecret{"DB_PASSWORD": {"web-s-own", "web"}}},
		{"worker", []string{"DB_PASSWORD", "ONLY_API"}, map[string]ResolvedSecret{"DB_PASSWORD": {"shared", ""}}},
		{"", []string{"DB_PASSWORD", "ONLY_API"}, map[string]ResolvedSecret{"DB_PASSWORD": {"shared", ""}}},
	} {
		got, err := s.GetSecrets(ctx, tt.application, tt.names)
		if err != nil {
			t.Fatalf("GetSecrets for %q: %v", tt.application, err)
		}
		if fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("GetSecrets for %q = %v, want %v", tt.application, got, tt.want)
		}
	}

	// Listed by name, the server's before the applications'.
	list, _ := s.ListSecrets(ctx)
	var order []string
	for _, sec := range list {
		order = append(order, sec.Application+"/"+sec.Name)
	}
	if want := "/DB_PASSWORD api/DB_PASSWORD web/DB_PASSWORD api/ONLY_API"; strings.Join(order, " ") != want {
		t.Errorf("list = %v, want %s", order, want)
	}
	if list[1].UpdatedAt.Equal(now) {
		t.Error("api's DB_PASSWORD was replaced: its updated_at must have moved")
	}

	// Removing the server's leaves the applications' where they are, and the
	// other way round.
	if err := s.DeleteSecret(ctx, "", "DB_PASSWORD"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if got, _ := s.GetSecrets(ctx, "api", []string{"DB_PASSWORD"}); got["DB_PASSWORD"].Value != "api-s-newer" {
		t.Errorf("api's secret after the server's went = %v", got)
	}
	if got, _ := s.GetSecrets(ctx, "worker", []string{"DB_PASSWORD"}); len(got) != 0 {
		t.Errorf("worker still finds a DB_PASSWORD: %v", got)
	}
	if err := s.DeleteSecret(ctx, "api", "DB_PASSWORD"); err != nil {
		t.Fatalf("DeleteSecret api's: %v", err)
	}
	if err := s.DeleteSecret(ctx, "api", "DB_PASSWORD"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting api's again: err = %v, want ErrNotFound", err)
	}
	if got, _ := s.GetSecrets(ctx, "web", []string{"DB_PASSWORD"}); got["DB_PASSWORD"].Value != "web-s-own" {
		t.Errorf("web's secret after api's went = %v", got)
	}
}

func TestSecretsAreEncryptedAtRestAndBoundToTheirName(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetSecret(ctx, "", "A", "value-of-A-written-in-plain-text", time.Now())
	s.SetSecret(ctx, "", "B", "two", time.Now())

	var rawA, rawB []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'A'`).Scan(&rawA); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'B'`).Scan(&rawB)
	// The plain value is long enough that a ciphertext cannot hold it by chance.
	if strings.Contains(string(rawA), "value-of-A-written-in-plain-text") || !isSealed(string(rawA)) {
		t.Errorf("the row holds %q: want ciphertext with the enc1 prefix", rawA)
	}

	// Swapping two rows' ciphertexts must not hand A the value of B.
	if _, err := s.db.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE name = 'A'`, rawB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSecrets(ctx, "", []string{"A"}); err == nil {
		t.Error("a ciphertext moved to another name must be refused")
	}

	// A wrong key at open time is caught by the env values; a secret read
	// under another key fails the same way.
	other := &Store{db: s.db}
	other.aead, _ = newAEAD([]byte("another-encryption-key-32-bytes!"))
	if _, err := other.GetSecrets(ctx, "", []string{"B"}); err == nil {
		t.Error("another key must not decrypt a secret")
	}
}

func TestAnApplicationsSecretIsBoundToTheApplicationToo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-own", time.Now())
	s.SetSecret(ctx, "web", "DB_PASSWORD", "web-s-own", time.Now())
	s.SetSecret(ctx, "", "DB_PASSWORD", "shared", time.Now())

	var fromAPI []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE application = 'api' AND name = 'DB_PASSWORD'`).Scan(&fromAPI); err != nil {
		t.Fatal(err)
	}
	// The same name under another application, or under the server, must
	// not open api's ciphertext: a row moved between scopes is refused.
	for _, scope := range []string{"web", ""} {
		if _, err := s.db.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE application = ? AND name = 'DB_PASSWORD'`, fromAPI, scope); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSecrets(ctx, scope, []string{"DB_PASSWORD"}); err == nil {
			t.Errorf("api's ciphertext opened as %q's DB_PASSWORD", scope)
		}
	}
	var label string
	if err := s.db.QueryRowContext(ctx, `SELECT label FROM secrets WHERE application = 'api' AND name = 'DB_PASSWORD'`).Scan(&label); err != nil || label != "applications/api/secrets/DB_PASSWORD" {
		t.Errorf("label = %q, %v: want the one the value is sealed under, for the rotation to walk", label, err)
	}
}

func TestDeleteSecret(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.SetSecret(ctx, "", "A", "one", time.Now())

	if err := s.DeleteSecret(ctx, "", "A"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := s.DeleteSecret(ctx, "", "A"); !errors.Is(err, ErrNotFound) {
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
		if err := s.SetSecret(ctx, "", fmt.Sprintf("S_%d", i), "v", now); err != nil {
			t.Fatalf("secret %d: %v", i, err)
		}
	}
	if err := s.SetSecret(ctx, "", "ONE_MORE", "v", now); !errors.Is(err, ErrTooManySecrets) {
		t.Errorf("err = %v, want ErrTooManySecrets", err)
	}
	// An application's secret counts like any other.
	if err := s.SetSecret(ctx, "api", "S_0", "v", now); !errors.Is(err, ErrTooManySecrets) {
		t.Errorf("api's S_0 at the limit: err = %v, want ErrTooManySecrets", err)
	}
	// Replacing an existing one is not a new secret.
	if err := s.SetSecret(ctx, "", "S_0", "changed", now); err != nil {
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
	s.SetSecret(ctx, "", "DB_PASSWORD", "hunter2", time.Now())
	s.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-own", time.Now())
	s.Close()

	s, err = Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	values, err := s.GetSecrets(ctx, "", []string{"DB_PASSWORD"})
	if err != nil || values["DB_PASSWORD"].Value != "hunter2" {
		t.Errorf("values = %v, %v", values, err)
	}
	if own, err := s.GetSecrets(ctx, "api", []string{"DB_PASSWORD"}); err != nil || own["DB_PASSWORD"].Value != "api-s-own" {
		t.Errorf("api's = %v, %v", own, err)
	}
}

// A database written by 0.8 has its secrets in a table keyed by name alone.
// The upgrade keeps every one of them as the server's, readable under the
// key they were sealed with, and lets an application have one of its own by
// the same name afterwards.
func TestSecretsOfAnOlderDatabaseStayTheServersAfterTheUpgrade(t *testing.T) {
	ctx := context.Background()
	k := newKeyed(t)

	all := migrations
	migrations = all[:21]
	old, err := k.open()
	migrations = all
	if err != nil {
		t.Fatalf("Open at 0.8: %v", err)
	}
	created := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for _, name := range []string{"DB_PASSWORD", "API_KEY"} {
		sealed, err := seal(old.aead, name, "value-of-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.db.ExecContext(ctx, `INSERT INTO secrets (name, value, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			name, []byte(sealed), formatTime(created), formatTime(created)); err != nil {
			old.Close()
			t.Fatalf("seed 0.8 secrets: %v", err)
		}
	}
	old.Close()

	s := k.mustOpen()
	list, err := s.ListSecrets(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, sec := range list {
		if sec.Application != "" || !sec.CreatedAt.Equal(created) {
			t.Errorf("%+v: want the server's, with its timestamps", sec)
		}
	}
	values, err := s.GetSecrets(ctx, "api", []string{"DB_PASSWORD", "API_KEY"})
	if err != nil || values["DB_PASSWORD"].Value != "value-of-DB_PASSWORD" || values["API_KEY"].Application != "" {
		t.Errorf("values = %v, %v: want the old values, found as the server's", values, err)
	}
	if err := s.SetSecret(ctx, "api", "DB_PASSWORD", "api-s-own", time.Now()); err != nil {
		t.Fatalf("an application's secret next to an old one: %v", err)
	}
	if values, _ := s.GetSecrets(ctx, "api", []string{"DB_PASSWORD"}); values["DB_PASSWORD"].Value != "api-s-own" {
		t.Errorf("api's = %v", values)
	}
	// The key rotation knows the upgraded table.
	if _, err := s.RotateKey(ctx); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if values, err := s.GetSecrets(ctx, "web", []string{"DB_PASSWORD"}); err != nil || values["DB_PASSWORD"].Value != "value-of-DB_PASSWORD" {
		t.Errorf("after the rotation = %v, %v", values, err)
	}
}
