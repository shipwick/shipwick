package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestGrantingAccessAgainReplacesTheRule(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	first, replaced, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com", Role: api.RoleRead, CreatedAt: now, CreatedBy: "root"})
	if err != nil || replaced || first.ID == 0 {
		t.Fatalf("GrantAccess = %+v, %v, %v", first, replaced, err)
	}
	// The same subject under another kind is another rule.
	if _, replaced, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessGroup, Subject: "ada@example.com", Role: api.RoleAdmin, CreatedAt: now}); err != nil || replaced {
		t.Fatalf("GrantAccess for a group: %v, %v", replaced, err)
	}
	second, replaced, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com", Role: api.RoleDeploy,
		Applications: []string{"my-api"}, CreatedAt: now.Add(time.Hour), CreatedBy: "ops"})
	if err != nil || !replaced || second.ID != first.ID {
		t.Fatalf("GrantAccess again = %+v, %v, %v; want the same rule replaced", second, replaced, err)
	}

	rules, err := s.AccessRules(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("AccessRules = %+v, %v", rules, err)
	}
	want := api.AccessRule{ID: first.ID, Kind: api.AccessEmail, Subject: "ada@example.com", Role: api.RoleDeploy, Applications: []string{"my-api"},
		CreatedAt: now.Add(time.Hour), CreatedBy: "ops"}
	if !reflect.DeepEqual(rules[0], want) {
		t.Errorf("rule = %+v, want %+v", rules[0], want)
	}
	if rules[1].Applications == nil {
		t.Error("a rule without a limit lists nil applications; want an empty list")
	}

	gone, err := s.RevokeAccess(ctx, first.ID)
	if err != nil || gone.Who() != "ada@example.com" {
		t.Errorf("RevokeAccess = %+v, %v", gone, err)
	}
	if _, err := s.RevokeAccess(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("RevokeAccess twice: %v, want ErrNotFound", err)
	}
}

func TestTheAccessTableIsBounded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range api.MaxAccessRules {
		if _, _, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessGroup, Subject: "team-" + strconv.Itoa(i), Role: api.RoleRead, CreatedAt: now}); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	if _, _, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessGroup, Subject: "one-more", Role: api.RoleRead, CreatedAt: now}); !errors.Is(err, ErrTooManyAccessRules) {
		t.Errorf("one rule too many: %v, want ErrTooManyAccessRules", err)
	}
	// Changing one that exists is not one more.
	if _, replaced, err := s.GrantAccess(ctx, api.AccessRule{Kind: api.AccessGroup, Subject: "team-0", Role: api.RoleAdmin, CreatedAt: now}); err != nil || !replaced {
		t.Errorf("replace at the limit: %v, %v", replaced, err)
	}
}

func TestASessionIsFoundByItsHashUntilItIsEndedOrForgotten(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	hash := func(v string) []byte { sum := sha256.Sum256([]byte(v)); return sum[:] }

	ada, err := s.CreateSession(ctx, Session{Hash: hash("ada"), Email: "ada@example.com", Groups: []string{"developers"}, Role: api.RoleDeploy,
		Applications: []string{"web"}, CreatedAt: now, ExpiresAt: now.Add(10 * time.Hour)})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, v := range []string{"ada-laptop", "grace"} {
		email := "ada@example.com"
		if v == "grace" {
			email = "grace@example.com"
		}
		if _, err := s.CreateSession(ctx, Session{Hash: hash(v), Email: email, Role: api.RoleRead, CreatedAt: now, ExpiresAt: now.Add(10 * time.Hour)}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	found, err := s.GetSessionByHash(ctx, hash("ada"))
	if err != nil || found.ID != ada.ID || !reflect.DeepEqual(found.Groups, []string{"developers"}) || !reflect.DeepEqual(found.Applications, []string{"web"}) ||
		!found.ExpiresAt.Equal(now.Add(10*time.Hour)) || found.EndedAt != nil || found.LastUsedAt != nil {
		t.Fatalf("GetSessionByHash = %+v, %v", found, err)
	}
	if _, err := s.GetSessionByHash(ctx, hash("nobody")); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown hash: %v, want ErrNotFound", err)
	}

	if err := s.TouchSession(ctx, ada.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(ctx, ada.ID, api.SessionEndedRuleRemoved, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The first reason stands: the holder is told what happened first.
	if err := s.EndSession(ctx, ada.ID, api.SessionEndedSignedOut, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	found, _ = s.GetSessionByHash(ctx, hash("ada"))
	if found.EndedAt == nil || !found.EndedAt.Equal(now.Add(time.Hour)) || found.EndedReason != api.SessionEndedRuleRemoved || found.LastUsedAt == nil {
		t.Errorf("ended session = %+v", found)
	}

	active, err := s.ActiveSessions(ctx, now.Add(time.Hour))
	if err != nil || len(active) != 2 {
		t.Fatalf("ActiveSessions = %+v, %v; want the two that were not ended", active, err)
	}
	if n, err := s.EndSessionsOf(ctx, "ada@example.com", api.SessionEndedSignedOut, now.Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("EndSessionsOf = %d, %v; want 1: one of the two was ended before", n, err)
	}
	if active, _ := s.ActiveSessions(ctx, now.Add(11*time.Hour)); len(active) != 0 {
		t.Errorf("sessions past their time are listed: %+v", active)
	}

	// A sign-in a day and more after the others expired forgets them.
	if _, err := s.CreateSession(ctx, Session{Hash: hash("later"), Email: "grace@example.com", Role: api.RoleRead,
		CreatedAt: now.Add(35 * time.Hour), ExpiresAt: now.Add(45 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionByHash(ctx, hash("ada")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a session that expired more than a day ago is still stored: %v", err)
	}
	later, _ := s.GetSessionByHash(ctx, hash("later"))
	if err := s.DeleteSession(ctx, later.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionByHash(ctx, hash("later")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted session is still found: %v", err)
	}
}

func TestADatabaseFromBeforeSignInGetsItsTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")
	all := migrations
	migrations = all[:16]
	old, err := Open(ctx, path, testOptions)
	migrations = all
	if err != nil {
		t.Fatalf("Open at schema 16: %v", err)
	}
	old.Close()

	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("Open after the upgrade: %v", err)
	}
	defer s.Close()
	if rules, err := s.AccessRules(ctx); err != nil || len(rules) != 0 {
		t.Errorf("AccessRules = %+v, %v", rules, err)
	}
	if sessions, err := s.ActiveSessions(ctx, time.Now()); err != nil || len(sessions) != 0 {
		t.Errorf("ActiveSessions = %+v, %v", sessions, err)
	}
}
