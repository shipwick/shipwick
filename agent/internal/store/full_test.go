package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// large is a deployment whose record needs pages of its own, whatever room
// the pages in use have left.
func large(name string) spec.App {
	return spec.App{Name: name, Image: name + ":1", Replicas: 1, Env: map[string]string{"LICENCE": strings.Repeat("x", 64<<10)}}
}

func TestADatabaseThatCannotGrowRefusesWhatNeedsRoomAndTakesItOnceThereIsSome(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, large("web"), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Fill(ctx, true); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateDeployment(ctx, large("web"), time.Now())
	if !IsFull(err) {
		t.Fatalf("CreateDeployment on a full database: err = %v, want SQLITE_FULL", err)
	}
	if strings.Contains(err.Error(), "xxxx") {
		t.Errorf("the error carries the record it could not write: %.200s", err)
	}
	// Half a deployment must not exist: the insert was one transaction.
	if all, _ := s.ListDeployments(ctx, DeploymentFilter{}); len(all) != 1 {
		t.Errorf("%d deployments after the refused one, want the one from before", len(all))
	}
	// Reading goes on, which is what lets the agent keep running what runs.
	if got, err := s.GetDeployment(ctx, d.ID); err != nil || got.Spec.Env["LICENCE"] != large("web").Env["LICENCE"] {
		t.Errorf("reading on a full database: %v", err)
	}

	if err := s.Fill(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, large("web"), time.Now()); err != nil {
		t.Errorf("CreateDeployment once there is room: %v", err)
	}
}

func TestOnlyALackOfRoomIsCalledFull(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, large("web"), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetDeployment(ctx, d.ID+1); IsFull(err) {
		t.Errorf("%v is taken for a full disk", err)
	}
	if err := s.RefuseWrites(ctx, true); err != nil {
		t.Fatal(err)
	}
	err = s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, "")
	if err == nil || IsFull(err) {
		t.Errorf("a database that refuses writes: err = %v, want an error that is not SQLITE_FULL", err)
	}
	if _, err := s.GetDeployment(ctx, d.ID); err != nil {
		t.Errorf("reading while writes are refused: %v", err)
	}
	if err := s.RefuseWrites(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, ""); err != nil {
		t.Errorf("writing again: %v", err)
	}
	if IsFull(nil) {
		t.Error("no error is taken for a full disk")
	}
}

// TestWhatAFullDiskDoesToTheDatabase is the measurement the two switches in
// full.go stand in for. It wants a filesystem of a few megabytes, named by
// SHIPWICK_TEST_SMALL_DIR, and is skipped without one: `make test-full-disk`.
func TestWhatAFullDiskDoesToTheDatabase(t *testing.T) {
	root := os.Getenv("SHIPWICK_TEST_SMALL_DIR")
	if root == "" {
		t.Skip("SHIPWICK_TEST_SMALL_DIR is not set: no small filesystem to fill (make test-full-disk)")
	}
	dir, err := os.MkdirTemp(root, "store-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(dir, "shipwick.db"), Options{EncryptionKey: []byte("an-encryption-key-of-32-bytes!!!")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	small := spec.App{Name: "web", Image: "web:1", Replicas: 1}
	d, err := s.CreateDeployment(ctx, small, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// Something that is not the database takes all the room: images, logs,
	// an upload.
	filler := filepath.Join(dir, "filler")
	f, err := os.Create(filler)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := make([]byte, 64<<10); ; {
		if _, err := f.Write(chunk); err != nil {
			break
		}
	}
	// What is left of the last block, down to the byte.
	for one := []byte{0}; ; {
		if _, err := f.Write(one); err != nil {
			break
		}
	}
	f.Close()

	// Every write fails, an update in place as much as an insert: none can
	// be added to the write-ahead log.
	writes := map[string]func() error{
		"a status change": func() error { return s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, "") },
		"a completion":    func() error { return s.CompleteDeployment(ctx, d.ID, time.Now()) },
		"an event": func() error {
			return s.AddEvent(ctx, d.ApplicationID, &d.ID, api.LevelInfo, api.EventStep, "Pulled image", time.Now())
		},
		"a new deployment": func() error {
			_, err := s.CreateDeployment(ctx, small, time.Now())
			return err
		},
	}
	// The log may have room left from before it was full: write until it is used.
	for i := 0; i < 5000; i++ {
		if err := s.AddEvent(ctx, d.ApplicationID, nil, api.LevelInfo, api.EventApp, "filling", time.Now()); err != nil {
			break
		}
	}
	for what, write := range writes {
		if err := write(); !IsFull(err) {
			t.Errorf("%s on a full disk: err = %v, want SQLITE_FULL", what, err)
		}
	}
	if got, err := s.GetDeployment(ctx, d.ID); err != nil || got.Status != api.StatusPending {
		t.Errorf("reading on a full disk: %s, %v; want the record as it was", got.Status, err)
	}

	// Room again: the same connection writes, nothing is reopened.
	if err := os.Remove(filler); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionDeployment(ctx, d.ID, api.StatusPending, api.StatusBuilding, ""); err != nil {
		t.Errorf("a status change once there is room: %v", err)
	}
	if err := s.CompleteDeployment(ctx, d.ID, time.Now()); err != nil {
		t.Errorf("a completion once there is room: %v", err)
	}
}
