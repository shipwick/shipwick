package store

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestATokenKeepsItsApplicationsAndItsExpiry(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	expires := now.Add(90 * 24 * time.Hour)
	limited, plain := sha256.Sum256([]byte("limited")), sha256.Sum256([]byte("plain"))

	if _, err := s.CreateToken(ctx, Token{Name: "ci", Role: api.RoleDeploy, Hash: limited[:], CreatedAt: now, Applications: []string{"api", "web"}, ExpiresAt: &expires}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := s.CreateToken(ctx, Token{Name: "ops", Role: api.RoleAdmin, Hash: plain[:], CreatedAt: now}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	found, err := s.GetTokenByHash(ctx, limited[:])
	if err != nil || !reflect.DeepEqual(found.Applications, []string{"api", "web"}) || found.ExpiresAt == nil || !found.ExpiresAt.Equal(expires) {
		t.Errorf("limited token = %+v, %v", found, err)
	}
	found, err = s.GetTokenByHash(ctx, plain[:])
	if err != nil || found.Applications == nil || len(found.Applications) != 0 || found.ExpiresAt != nil {
		t.Errorf("plain token = %+v, %v; want no applications and no expiry", found, err)
	}
}

func TestTokensFromThePreviousSchemaKeepWorkingUnchanged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")

	// A database as 0.5 left it: thirteen migrations, and a token written
	// with the columns that existed then.
	all := migrations
	migrations = all[:13]
	old, err := Open(ctx, path, testOptions)
	migrations = all
	if err != nil {
		t.Fatalf("Open at schema 13: %v", err)
	}
	hash := sha256.Sum256([]byte("swk_from-before"))
	created, used := formatTime(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)), formatTime(time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC))
	if _, err := old.db.ExecContext(ctx, `INSERT INTO tokens (name, role, hash, created_at, last_used_at) VALUES ('ci', 'deploy', ?, ?, ?)`, hash[:], created, used); err != nil {
		old.Close()
		t.Fatalf("seed a token: %v", err)
	}
	old.Close()

	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatalf("Open with the new migrations: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(all) || version < 16 {
		t.Fatalf("schema version = %d, %v; want %d", version, err, len(all))
	}
	found, err := s.GetTokenByHash(ctx, hash[:])
	if err != nil {
		t.Fatalf("the token is gone after the upgrade: %v", err)
	}
	if found.Name != "ci" || found.Role != api.RoleDeploy || found.LastUsedAt == nil || found.ExpiresAt != nil || found.Applications == nil || len(found.Applications) != 0 {
		t.Errorf("token = %+v; want it as it was: not limited, never expiring", found)
	}
	tokens, err := s.ListTokens(ctx)
	if err != nil || len(tokens) != 1 {
		t.Errorf("ListTokens = %+v, %v", tokens, err)
	}
	if entries, err := s.AuditEntries(ctx, AuditFilter{Limit: 10}); err != nil || len(entries) != 0 {
		t.Errorf("the audit trail starts empty: %+v, %v", entries, err)
	}
	if _, err := s.AddAuditEntry(ctx, api.AuditEntry{At: time.Now(), Actor: api.Actor{Kind: api.ActorToken, Name: "ci"}, Action: "deploy", Outcome: api.AuditOK, Status: 202}); err != nil {
		t.Errorf("AddAuditEntry after the upgrade: %v", err)
	}
}

func auditEntry(at time.Time, actor, action, application string) api.AuditEntry {
	return api.AuditEntry{At: at, Actor: api.Actor{Kind: api.ActorToken, Name: actor}, Address: "172.18.0.3", ForwardedFor: "203.0.113.9",
		Action: action, Application: application, Target: "nightly", Outcome: api.AuditFailed, Status: 409, Code: "JOB_ALREADY_RUNNING", Detail: "run 4"}
}

func TestAuditEntriesComeBackAsWrittenNewestFirst(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	for i, e := range []api.AuditEntry{
		auditEntry(start, "ci", "job.run", "api"),
		auditEntry(start.Add(time.Hour), "root", "stop", "web"),
		auditEntry(start.Add(2*time.Hour), "ci", "deploy", "web"),
	} {
		if id, err := s.AddAuditEntry(ctx, e); err != nil || id != int64(i+1) {
			t.Fatalf("AddAuditEntry = %d, %v", id, err)
		}
	}

	all, err := s.AuditEntries(ctx, AuditFilter{Limit: 10})
	if err != nil || len(all) != 3 {
		t.Fatalf("AuditEntries = %+v, %v", all, err)
	}
	want := auditEntry(start, "ci", "job.run", "api")
	want.ID = 1
	if all[2] != want {
		t.Errorf("oldest entry:\n got %+v\nwant %+v", all[2], want)
	}

	ids := func(f AuditFilter) []int64 {
		t.Helper()
		f.Limit = max(f.Limit, 1)
		entries, err := s.AuditEntries(ctx, f)
		if err != nil {
			t.Fatalf("AuditEntries(%+v): %v", f, err)
		}
		out := []int64{}
		for _, e := range entries {
			out = append(out, e.ID)
		}
		return out
	}
	for _, tt := range []struct {
		filter AuditFilter
		want   []int64
	}{
		{AuditFilter{Limit: 10, Application: "web"}, []int64{3, 2}},
		{AuditFilter{Limit: 10, Actor: "ci"}, []int64{3, 1}},
		{AuditFilter{Limit: 10, Actor: "ci", Application: "web"}, []int64{3}},
		{AuditFilter{Limit: 10, Since: start.Add(time.Hour)}, []int64{3, 2}},
		{AuditFilter{Limit: 10, Before: 3}, []int64{2, 1}},
		{AuditFilter{Limit: 1, Before: 3}, []int64{2}},
		{AuditFilter{Limit: 10, Actor: "nobody"}, []int64{}},
	} {
		if got := ids(tt.filter); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("AuditEntries(%+v) = %v, want %v", tt.filter, got, tt.want)
		}
	}
}

func TestAuditEntriesOlderThanTheRetentionGoWhenTheNextIsWritten(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	s.AddAuditEntry(ctx, auditEntry(start, "ci", "deploy", "api"))
	s.AddAuditEntry(ctx, auditEntry(start.Add(time.Hour), "ci", "deploy", "api"))
	s.AddAuditEntry(ctx, auditEntry(start.Add(AuditRetention), "ci", "deploy", "api"))
	if entries, _ := s.AuditEntries(ctx, AuditFilter{Limit: 10}); len(entries) != 3 {
		t.Fatalf("%d entries; one exactly as old as the retention is kept", len(entries))
	}
	s.AddAuditEntry(ctx, auditEntry(start.Add(AuditRetention+time.Minute), "ci", "deploy", "api"))
	entries, _ := s.AuditEntries(ctx, AuditFilter{Limit: 10})
	if len(entries) != 3 || entries[2].ID != 2 {
		t.Errorf("entries = %+v; want the first one gone and the rest kept", entries)
	}
}

func TestTheAuditTrailNeverGrowsBeyondItsCount(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// Two entries, then one whose id is past the count: writing a hundred
	// thousand rows would say the same and take a minute.
	s.AddAuditEntry(ctx, auditEntry(at, "ci", "deploy", "api"))
	s.AddAuditEntry(ctx, auditEntry(at, "ci", "deploy", "api"))
	if _, err := s.db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = 'audit_log'`, AuditMaxEntries); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddAuditEntry(ctx, auditEntry(at, "ci", "deploy", "api"))
	if err != nil || id != AuditMaxEntries+1 {
		t.Fatalf("AddAuditEntry = %d, %v", id, err)
	}
	entries, _ := s.AuditEntries(ctx, AuditFilter{Limit: 10})
	if len(entries) != 2 || entries[0].ID != AuditMaxEntries+1 || entries[1].ID != 2 {
		t.Errorf("entries = %+v; want entry 1 gone: it is %d behind the newest", entries, AuditMaxEntries)
	}
}
