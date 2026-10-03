package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestTokensAreCreatedListedAndLookedUpByHash(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	hash := sha256.Sum256([]byte("swk_secret"))
	created, err := s.CreateToken(ctx, Token{Name: "ci", Role: api.RoleDeploy, Hash: hash[:], CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if created.ID == 0 || created.Name != "ci" || created.Role != api.RoleDeploy || !created.CreatedAt.Equal(now) {
		t.Errorf("unexpected token: %+v", created)
	}

	found, err := s.GetTokenByHash(ctx, hash[:])
	if err != nil {
		t.Fatalf("GetTokenByHash: %v", err)
	}
	if found.ID != created.ID || !bytes.Equal(found.Hash, hash[:]) || found.LastUsedAt != nil {
		t.Errorf("unexpected token: %+v", found)
	}
	other := sha256.Sum256([]byte("swk_other"))
	if _, err := s.GetTokenByHash(ctx, other[:]); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown hash: err = %v, want ErrNotFound", err)
	}

	if _, err := s.CreateToken(ctx, Token{Name: "ci", Role: api.RoleRead, Hash: other[:], CreatedAt: now}); !errors.Is(err, ErrTokenExists) {
		t.Errorf("duplicate name: err = %v, want ErrTokenExists", err)
	}
	if _, err := s.CreateToken(ctx, Token{Name: "reader", Role: api.RoleRead, Hash: other[:], CreatedAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	tokens, err := s.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 2 || tokens[0].Name != "ci" || tokens[1].Name != "reader" {
		t.Errorf("unexpected list: %+v", tokens)
	}
}

func TestTokenUseIsRecordedAndRevocationRemovesIt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	hash := sha256.Sum256([]byte("swk_secret"))
	created, _ := s.CreateToken(ctx, Token{Name: "ci", Role: api.RoleDeploy, Hash: hash[:], CreatedAt: now})
	if err := s.TouchToken(ctx, created.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchToken: %v", err)
	}
	found, _ := s.GetTokenByHash(ctx, hash[:])
	if found.LastUsedAt == nil || !found.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("last use not recorded: %+v", found)
	}

	if err := s.DeleteToken(ctx, "ci"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := s.GetTokenByHash(ctx, hash[:]); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token still found: %v", err)
	}
	if err := s.DeleteToken(ctx, "ci"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	if err := s.TouchToken(ctx, created.ID, now); err != nil {
		t.Errorf("touching a revoked token must not fail: %v", err)
	}
}

func TestDeploymentRecordsItsActor(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	d, err := s.CreateDeploymentFrom(ctx, testApp("api", "nginx:1"), api.KindDeploy, nil, "ci", time.Now())
	if err != nil {
		t.Fatalf("CreateDeploymentFrom: %v", err)
	}
	got, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Actor != "ci" {
		t.Errorf("actor = %q, want ci", got.Actor)
	}
}
