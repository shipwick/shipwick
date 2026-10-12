//go:build integration

package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// What undoing a refused upload rests on: the daemon reports which image a
// tag names, a load moves an existing tag onto what it loads, a tag call
// moves it back, and the image that arrived can then be removed by its ID.
func TestIntegrationATagMovedByALoadCanBePointedBackAndTheArrivalRemoved(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	info, err := rt.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	architecture := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[info.Architecture]
	if architecture == "" {
		t.Skipf("no test image for %s", info.Architecture)
	}

	run := fmt.Sprintf("%d", time.Now().UnixNano())
	tag := "shipwick.local/it-tags-" + run + ":1"
	victim := newTestLayer(t, "app.txt", "victim "+run)
	attacker := newTestLayer(t, "app.txt", "attacker "+run)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rt.RemoveImage(cleanup, tag)
	})

	if _, err := rt.LoadImage(ctx, bytes.NewReader(saveArchive(t, tag, architecture, []testLayer{victim}))); err != nil {
		t.Fatalf("load the victim: %v", err)
	}
	before, err := rt.ImageIDs(ctx)
	if err != nil {
		t.Fatalf("ImageIDs: %v", err)
	}
	victimID := before[tag]
	if victimID == "" {
		t.Fatalf("ImageIDs does not list %s: %v", tag, before)
	}

	if _, err := rt.LoadImage(ctx, bytes.NewReader(saveArchive(t, tag, architecture, []testLayer{attacker}))); err != nil {
		t.Fatalf("load the attacker's under the same tag: %v", err)
	}
	after, _ := rt.ImageIDs(ctx)
	attackerID := after[tag]
	if attackerID == "" || attackerID == victimID {
		t.Fatalf("the load did not move the tag: %q before, %q after", victimID, attackerID)
	}

	if err := rt.TagImage(ctx, victimID, tag); err != nil {
		t.Fatalf("TagImage: %v", err)
	}
	if restored, _ := rt.ImageIDs(ctx); restored[tag] != victimID {
		t.Errorf("after TagImage %s names %q, want %q", tag, restored[tag], victimID)
	}
	if err := rt.RemoveImage(ctx, attackerID); err != nil {
		t.Errorf("remove what arrived by its ID: %v", err)
	}
	if ok, err := rt.ImageExists(ctx, attackerID); err != nil || ok {
		t.Errorf("the attacker's image still exists: %v, %v", ok, err)
	}
	if ok, err := rt.ImageExists(ctx, tag); err != nil || !ok {
		t.Errorf("the victim's tag is gone: %v, %v", ok, err)
	}
	if err := rt.RemoveImage(ctx, attackerID); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("removing it again: err = %v, want ErrImageNotFound", err)
	}
}
