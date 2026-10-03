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

func TestRegistryCredentialsRoundTripAndThePasswordIsSealedAtRest(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	if _, _, found, err := s.RegistryCredential(ctx, "ghcr.io"); err != nil || found {
		t.Fatalf("before login: found = %v, err = %v", found, err)
	}
	if err := s.SetRegistry(ctx, "ghcr.io", "octocat", "ghp_token", created); err != nil {
		t.Fatalf("SetRegistry: %v", err)
	}
	username, password, found, err := s.RegistryCredential(ctx, "ghcr.io")
	if err != nil || !found || username != "octocat" || password != "ghp_token" {
		t.Fatalf("RegistryCredential = %q, %q, %v, %v", username, password, found, err)
	}

	stored := rawColumn(t, s, `SELECT password FROM registries WHERE registry = 'ghcr.io'`)
	if !isSealed(stored) || strings.Contains(stored, "ghp_token") {
		t.Errorf("the password is stored as %q", stored)
	}
	// Bound to its registry: pasted into another row, it does not open.
	if err := s.SetRegistry(ctx, "registry.example.com:5000", "ci", "other", created); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE registries SET password = ? WHERE registry = 'registry.example.com:5000'`, []byte(stored)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.RegistryCredential(ctx, "registry.example.com:5000"); err == nil {
		t.Error("a password moved to another registry must not decrypt there")
	}
}

func TestSetRegistryReplacesTheCredentialAndListsWithoutPasswords(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)

	s.SetRegistry(ctx, "ghcr.io", "octocat", "first", created)
	s.SetRegistry(ctx, "docker.io", "company", "hub", created)
	if err := s.SetRegistry(ctx, "ghcr.io", "ci-bot", "second", updated); err != nil {
		t.Fatalf("replace: %v", err)
	}

	list, err := s.ListRegistries(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRegistries = %v, %v", list, err)
	}
	if list[0].Registry != "docker.io" || list[1].Registry != "ghcr.io" {
		t.Errorf("want name order, got %v", list)
	}
	if got := list[1]; got.Username != "ci-bot" || !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Errorf("replaced credential = %+v", got)
	}
	if _, password, _, _ := s.RegistryCredential(ctx, "ghcr.io"); password != "second" {
		t.Errorf("password = %q", password)
	}

	if err := s.DeleteRegistry(ctx, "ghcr.io"); err != nil {
		t.Fatalf("DeleteRegistry: %v", err)
	}
	if err := s.DeleteRegistry(ctx, "ghcr.io"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	if _, _, found, _ := s.RegistryCredential(ctx, "ghcr.io"); found {
		t.Error("the credential is still there")
	}
}

func TestSetRegistryStopsAtTheLimitButStillReplaces(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	for i := range api.MaxRegistries {
		if err := s.SetRegistry(ctx, fmt.Sprintf("r%d.example.com", i), "u", "p", now); err != nil {
			t.Fatalf("registry %d: %v", i, err)
		}
	}
	if err := s.SetRegistry(ctx, "one-more.example.com", "u", "p", now); !errors.Is(err, ErrTooManyRegistries) {
		t.Errorf("over the limit = %v, want ErrTooManyRegistries", err)
	}
	if err := s.SetRegistry(ctx, "r0.example.com", "u", "new", now); err != nil {
		t.Errorf("replacing at the limit: %v", err)
	}
}
