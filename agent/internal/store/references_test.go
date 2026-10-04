package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

var someReferences = spec.References{
	Env:       map[string]string{"KEY": "prefix-${STORED_KEY}"},
	BasicAuth: map[int]string{0: "${ADMIN_PASSWORD}"},
}

func referringApp() spec.App {
	a := testApp("my-api", "my-api:1.0")
	a.Domain, a.Port = "api.example.com", 8080
	a.Proxy = &spec.Proxy{BasicAuth: []spec.BasicAuth{{Username: "admin", Password: "correct horse"}}}
	return a
}

func createReferring(t *testing.T, s *Store) Deployment {
	t.Helper()
	d, err := s.CreateDeploymentWith(context.Background(), NewDeployment{Spec: referringApp(), Kind: api.KindDeploy, References: someReferences}, time.Now())
	if err != nil {
		t.Fatalf("CreateDeploymentWith: %v", err)
	}
	return d
}

func TestReferencesAreKeptWithTheirDeploymentAndSealed(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d := createReferring(t, s)

	got, err := s.DeploymentReferences(ctx, d.ID)
	if err != nil || !reflect.DeepEqual(got, someReferences) {
		t.Fatalf("references = %+v, err = %v", got, err)
	}
	// The record holds the values; the references are beside it, not in it.
	stored, err := s.GetDeployment(ctx, d.ID)
	if err != nil || stored.Spec.Env["KEY"] != "value" || stored.Spec.Proxy.BasicAuth[0].Password != "correct horse" {
		t.Errorf("spec = %+v, err = %v", stored.Spec, err)
	}

	var raw []byte
	if err := s.db.QueryRow(`SELECT value FROM deployment_references WHERE deployment_id = ?`, d.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !isSealed(string(raw)) || strings.Contains(string(raw), "STORED_KEY") || strings.Contains(string(raw), "prefix-") {
		t.Errorf("the references are readable in the database: %s", raw)
	}
	// Sealed for this deployment: moved to another, the row does not open.
	other := createReferring(t, s)
	if _, err := s.db.Exec(`UPDATE deployment_references SET value = ? WHERE deployment_id = ?`, raw, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeploymentReferences(ctx, other.ID); err == nil {
		t.Error("references sealed for one deployment opened for another")
	}
}

func TestADeploymentWithoutReferencesHasNoRow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	plain, err := s.CreateDeployment(ctx, testApp("plain", "plain:1"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// References to what the spec does not have are not kept either.
	stale, err := s.CreateDeploymentWith(ctx, NewDeployment{Spec: testApp("stale", "stale:1"), Kind: api.KindDeploy,
		References: spec.References{Env: map[string]string{"GONE": "${GONE}"}, BasicAuth: map[int]string{0: "${GONE}"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []Deployment{plain, stale} {
		if refs, err := s.DeploymentReferences(ctx, d.ID); err != nil || !refs.Empty() {
			t.Errorf("%s: references = %+v, err = %v", d.Application, refs, err)
		}
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deployment_references`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows = %d, err = %v", rows, err)
	}
	if refs, err := s.DeploymentReferences(ctx, 999); err != nil || !refs.Empty() {
		t.Errorf("an unknown deployment: %+v, %v", refs, err)
	}
}

func TestCreateDeploymentWithRecordsAStaticDeploymentLikeCreateStaticDeployment(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	app := spec.App{Name: "docs", Domain: "docs.example.com", Replicas: 1, Static: &spec.Static{Dir: "dist"},
		Restart: spec.Restart{Policy: spec.RestartAlways}, Deploy: spec.Deploy{Strategy: spec.StrategyRolling}}
	files := StaticFiles{Digest: "sha256:" + strings.Repeat("ab", 32), Files: 3, Bytes: 1234}
	now := time.Now()

	want, err := s.CreateStaticDeployment(ctx, app, api.KindDeploy, nil, "ci", files, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CreateDeploymentWith(ctx, NewDeployment{Spec: app, Kind: api.KindDeploy, Actor: "ci", Static: &files}, now)
	if err != nil {
		t.Fatal(err)
	}
	want.ID, want.Sequence = got.ID, got.Sequence
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deployment = %+v, want %+v", got, want)
	}

	container := testApp("my-api", "my-api:1.4.2")
	source := int64(1)
	wantC, err := s.CreateDeploymentFrom(ctx, container, api.KindRedeploy, &source, "ci", now)
	if err != nil {
		t.Fatal(err)
	}
	gotC, err := s.CreateDeploymentWith(ctx, NewDeployment{Spec: container, Kind: api.KindRedeploy, SourceID: &source, Actor: "ci"}, now)
	if err != nil {
		t.Fatal(err)
	}
	wantC.ID, wantC.Sequence = gotC.ID, gotC.Sequence
	if !reflect.DeepEqual(gotC, wantC) {
		t.Errorf("deployment = %+v, want %+v", gotC, wantC)
	}
}

func TestReferencesGoWithTheirApplication(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d := createReferring(t, s)
	if err := s.DeleteApplication(ctx, d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deployment_references`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows = %d, err = %v", rows, err)
	}
}

func TestAKeyRotationTakesTheReferencesAlongAndTheNextStartOpensThem(t *testing.T) {
	k := newKeyed(t)
	s := k.mustOpen()
	ctx := context.Background()
	d := createReferring(t, s)
	var before []byte
	s.db.QueryRow(`SELECT value FROM deployment_references WHERE deployment_id = ?`, d.ID).Scan(&before)

	rotation, err := s.RotateKey(ctx)
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if rotation.Values != 1 || rotation.Deployments != 1 {
		t.Errorf("rotation = %+v, want the references counted among the values", rotation)
	}
	var after []byte
	s.db.QueryRow(`SELECT value FROM deployment_references WHERE deployment_id = ?`, d.ID).Scan(&after)
	if string(after) == string(before) {
		t.Error("the references were not sealed again")
	}
	if got, err := s.DeploymentReferences(ctx, d.ID); err != nil || !reflect.DeepEqual(got, someReferences) {
		t.Errorf("after the rotation: references = %+v, err = %v", got, err)
	}
	s.Close()

	// A start with the new key proves it against the references as well.
	again := k.mustOpen()
	if got, err := again.DeploymentReferences(ctx, d.ID); err != nil || !reflect.DeepEqual(got, someReferences) {
		t.Errorf("after a restart: references = %+v, err = %v", got, err)
	}
}

func TestMigration20IsTheReferences(t *testing.T) {
	if len(migrations) < 20 || !strings.Contains(migrations[19], "CREATE TABLE deployment_references") {
		t.Fatalf("migration 20 is not the references: %d migrations", len(migrations))
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("schema version = %d, err = %v", version, err)
	}
}
