package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestATokensApplicationsAndEndAreChangedAndNothingElse(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("swk_secret"))
	end := now.Add(24 * time.Hour)
	created, err := s.CreateToken(ctx, Token{Name: "ci", Role: api.RoleDeploy, Hash: hash[:], CreatedAt: now, Applications: []string{"my-api"}, ExpiresAt: &end})
	if err != nil {
		t.Fatal(err)
	}

	later := now.Add(90 * 24 * time.Hour)
	before, after, err := s.UpdateToken(ctx, "ci", func(tok Token) (Token, error) {
		// What the callback does to the rest is not written.
		tok.Applications, tok.ExpiresAt, tok.Role, tok.Name, tok.Hash = []string{"my-api", "web"}, &later, api.RoleAdmin, "other", []byte("x")
		return tok, nil
	})
	if err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	if !reflect.DeepEqual(before.Applications, []string{"my-api"}) || !before.ExpiresAt.Equal(end) {
		t.Errorf("before = %+v", before)
	}
	found, err := s.GetTokenByHash(ctx, hash[:])
	if err != nil {
		t.Fatalf("the token is not found by its value after the update: %v", err)
	}
	if found.ID != created.ID || found.Name != "ci" || found.Role != api.RoleDeploy || !reflect.DeepEqual(found.Applications, []string{"my-api", "web"}) || !found.ExpiresAt.Equal(later) {
		t.Errorf("stored token = %+v", found)
	}
	if !reflect.DeepEqual(after, found) {
		t.Errorf("after = %+v, stored %+v", after, found)
	}

	// The limit lifted and the end taken away.
	if _, after, err = s.UpdateToken(ctx, "ci", func(tok Token) (Token, error) {
		tok.Applications, tok.ExpiresAt = nil, nil
		return tok, nil
	}); err != nil || after.Applications == nil || len(after.Applications) != 0 || after.ExpiresAt != nil {
		t.Errorf("after = %+v, %v", after, err)
	}

	// A change that is refused changes nothing, and its error comes back as it is.
	refusal := errors.New("no")
	if _, _, err := s.UpdateToken(ctx, "ci", func(tok Token) (Token, error) {
		tok.Applications = []string{"other"}
		return tok, refusal
	}); err != refusal {
		t.Errorf("err = %v, want the callback's own", err)
	}
	if found, _ := s.GetTokenByHash(ctx, hash[:]); len(found.Applications) != 0 {
		t.Errorf("a refused change was written: %+v", found)
	}
	if _, _, err := s.UpdateToken(ctx, "nobody", func(tok Token) (Token, error) { return tok, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown token: %v", err)
	}
}

func TestTheAuditTrailIsSearchedByActionOutcomeAndKindOfActor(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	entry := func(kind, actor, action, outcome string) {
		t.Helper()
		if _, err := s.AddAuditEntry(ctx, api.AuditEntry{At: at, Actor: api.Actor{Kind: kind, Name: actor}, Action: action, Outcome: outcome, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	entry(api.ActorToken, "ci", "deploy", api.AuditOK)                    // 1
	entry(api.ActorToken, "ci", "token.create", api.AuditRefused)         // 2
	entry(api.ActorUser, "ada@example.com", "token.revoke", api.AuditOK)  // 3
	entry(api.ActorUser, "ada@example.com", "backup.create", api.AuditOK) // 4
	entry(api.ActorToken, "root", "server.backup", api.AuditFailed)       // 5
	entry(api.ActorToken, "root", "tokens", api.AuditOK)                  // 6: not in the family "token."
	entry(api.ActorUser, "ada@example.com", "signin", api.AuditRefused)   // 7

	for name, tt := range map[string]struct {
		filter AuditFilter
		want   string
	}{
		"one action":                  {AuditFilter{Actions: []string{"deploy"}}, "1"},
		"a family":                    {AuditFilter{Actions: []string{"token."}}, "3 2"},
		"a family and an action":      {AuditFilter{Actions: []string{"backup.", "deploy"}}, "4 1"},
		"an action is not its family": {AuditFilter{Actions: []string{"token"}}, ""},
		"one outcome":                 {AuditFilter{Outcomes: []string{api.AuditRefused}}, "7 2"},
		"two outcomes":                {AuditFilter{Outcomes: []string{api.AuditRefused, api.AuditFailed}}, "7 5 2"},
		"people":                      {AuditFilter{ActorKind: api.ActorUser}, "7 4 3"},
		"tokens":                      {AuditFilter{ActorKind: api.ActorToken}, "6 5 2 1"},
		"all at once":                 {AuditFilter{ActorKind: api.ActorUser, Actions: []string{"token.", "signin"}, Outcomes: []string{api.AuditOK}}, "3"},
		"with an actor and a page":    {AuditFilter{Actor: "ada@example.com", Actions: []string{"token.", "backup.", "signin"}, Before: 7}, "4 3"},
	} {
		tt.filter.Limit = 10
		entries, err := s.AuditEntries(ctx, tt.filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var ids []string
		for _, e := range entries {
			ids = append(ids, strconv.FormatInt(e.ID, 10))
		}
		if got := strings.Join(ids, " "); got != tt.want {
			t.Errorf("%s: entries %q, want %q", name, got, tt.want)
		}
	}
}

func TestEveryMatchingEntryIsHandedOutAcrossPagesNewestFirst(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// More than two pages, and every third entry another action.
	const total = 2*auditExportPage + 7
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for i := 1; i <= total; i++ {
			action := "deploy"
			if i%3 == 0 {
				action = "stop"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (at, actor_kind, actor, address, action, outcome, status) VALUES (?, 'token', 'ci', '127.0.0.1', ?, 'ok', 200)`, formatTime(at), action); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	if err := s.EachAuditEntry(ctx, AuditFilter{}, func(e api.AuditEntry) error {
		// An entry written while the export runs is newer than where it
		// started: it must not come up, and must not shift what does.
		if len(ids) == 10 {
			if _, err := s.AddAuditEntry(ctx, auditEntry(at, "root", "deploy", "web")); err != nil {
				return err
			}
		}
		ids = append(ids, e.ID)
		return nil
	}); err != nil {
		t.Fatalf("EachAuditEntry: %v", err)
	}
	if len(ids) != total {
		t.Fatalf("%d entries handed out, want %d", len(ids), total)
	}
	for i, id := range ids {
		if id != int64(total-i) {
			t.Fatalf("entry %d has id %d, want %d: not every entry once, newest first", i, id, total-i)
		}
	}

	stops := 0
	if err := s.EachAuditEntry(ctx, AuditFilter{Actions: []string{"stop"}}, func(api.AuditEntry) error { stops++; return nil }); err != nil || stops != total/3 {
		t.Errorf("filtered: %d entries, %v; want %d", stops, err, total/3)
	}

	stop := errors.New("the reader went away")
	seen := 0
	if err := s.EachAuditEntry(ctx, AuditFilter{}, func(api.AuditEntry) error {
		if seen++; seen == 3 {
			return stop
		}
		return nil
	}); err != stop || seen != 3 {
		t.Errorf("err = %v after %d entries; want it to stop at the third", err, seen)
	}
}

func TestSessionsFromThePreviousSchemaAreNamedByTheEmailClaim(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")

	// A database as 0.6 left it: eighteen migrations, with a token, a rule
	// and a session written with the columns that existed then.
	all := migrations
	migrations = all[:18]
	old, err := Open(ctx, path, testOptions)
	migrations = all
	if err != nil {
		t.Fatalf("Open at schema 18: %v", err)
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tokenHash, sessionHash := sha256.Sum256([]byte("swk_from-0.6")), sha256.Sum256([]byte("sws_from-0.6"))
	end := now.Add(30 * 24 * time.Hour)
	if _, err := old.CreateToken(ctx, Token{Name: "ci", Role: api.RoleDeploy, Hash: tokenHash[:], CreatedAt: now, Applications: []string{"my-api"}, ExpiresAt: &end}); err != nil {
		t.Fatalf("seed a token: %v", err)
	}
	if _, _, err := old.GrantAccess(ctx, api.AccessRule{Kind: api.AccessDomain, Subject: "example.com", Role: api.RoleRead, CreatedAt: now, CreatedBy: "root"}); err != nil {
		t.Fatalf("seed a rule: %v", err)
	}
	if _, err := old.db.ExecContext(ctx, `INSERT INTO sessions (hash, email, groups, role, applications, created_at, expires_at) VALUES (?, 'ada@example.com', '["developers"]', 'read', '[]', ?, ?)`,
		hex.EncodeToString(sessionHash[:]), formatTime(now), formatTime(now.Add(api.SessionLifetime))); err != nil {
		t.Fatalf("seed a session: %v", err)
	}
	old.Close()

	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("Open with the new migrations: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(all) || version < 21 {
		t.Fatalf("schema version = %d, %v; want %d", version, err, len(all))
	}

	token, err := s.GetTokenByHash(ctx, tokenHash[:])
	if err != nil || token.Name != "ci" || !reflect.DeepEqual(token.Applications, []string{"my-api"}) || !token.ExpiresAt.Equal(end) {
		t.Errorf("token = %+v, %v; want it as it was", token, err)
	}
	if rules, err := s.AccessRules(ctx); err != nil || len(rules) != 1 || rules[0].Who() != "*@example.com" || rules[0].Role != api.RoleRead {
		t.Errorf("rules = %+v, %v; want the one that was there", rules, err)
	}
	sess, err := s.GetSessionByHash(ctx, sessionHash[:])
	if err != nil || sess.Email != "ada@example.com" || sess.NameClaim != api.DefaultNameClaim || sess.Tenant != "" || !reflect.DeepEqual(sess.Groups, []string{"developers"}) {
		t.Errorf("session = %+v, %v; want it named by the email claim, without a tenant", sess, err)
	}

	// And what is written from now on keeps what it is told.
	hash := sha256.Sum256([]byte("sws_new"))
	if _, err := s.CreateSession(ctx, Session{Hash: hash[:], Email: "AAAA-bbbb_cccc", Role: api.RoleRead, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		NameClaim: "sub", Tenant: "11111111-1111-4111-8111-111111111111"}); err != nil {
		t.Fatal(err)
	}
	if sess, err := s.GetSessionByHash(ctx, hash[:]); err != nil || sess.Email != "AAAA-bbbb_cccc" || sess.NameClaim != "sub" || sess.Tenant != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("new session = %+v, %v", sess, err)
	}
}
