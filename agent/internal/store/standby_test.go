package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferStateSurvivesReopeningAndIsReplacedByName(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shipwick.db")
	s, err := Open(ctx, path, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	type record struct {
		Export int64  `json:"export"`
		Error  string `json:"error"`
	}
	var got record
	if found, err := s.TransferState(ctx, TransferPull, &got); found || err != nil {
		t.Fatalf("before anything was kept: found %v, %v", found, err)
	}
	if err := s.SetTransferState(ctx, TransferPull, record{Export: 7, Error: "first"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTransferState(ctx, TransferPull, record{Export: 8}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTransferState(ctx, TransferImport, record{Export: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if found, err := s.TransferState(ctx, TransferPull, &got); !found || err != nil || got != (record{Export: 8}) {
		t.Errorf("after reopening: %+v, found %v, %v; want the record written last", got, found, err)
	}
}

func TestADeploymentIsDormantOnlyOnceItWasMarked(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	d, err := s.CreateDeployment(ctx, testApp("db", "postgres:17"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if dormant, err := s.DeploymentDormant(ctx, d.ID); dormant || err != nil {
		t.Fatalf("a new deployment: dormant %v, %v", dormant, err)
	}
	if err := s.MarkDeploymentDormant(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if dormant, err := s.DeploymentDormant(ctx, d.ID); !dormant || err != nil {
		t.Errorf("after it was marked: dormant %v, %v", dormant, err)
	}
	if err := s.MarkDeploymentDormant(ctx, d.ID+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("marking a deployment that does not exist: %v", err)
	}
	if _, err := s.DeploymentDormant(ctx, d.ID+1); !errors.Is(err, ErrNotFound) {
		t.Errorf("asking about a deployment that does not exist: %v", err)
	}
}
