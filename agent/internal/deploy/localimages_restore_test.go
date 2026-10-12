package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// An archive tagged with a tag the server already has — another
// application's image, the proxy's — moves that tag onto what arrived when
// the daemon loads it. Refusing the upload must leave the tag naming what it
// named before, or a deploy token limited to one application could take
// another's image away.
func TestARefusedUploadLeavesTheTagItMovedAsItWas(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := "shipwick.local/other:20260927-153000-a1b2"
	proxy := "ghcr.io/shipwick/caddy:0.9.1"
	// The other application runs: its container uses the image.
	h.rt.AddLocalImage(other)
	h.rt.AddLocalImage(proxy)
	o := app("other", other, 1)
	o.Build = built("").Build
	if d := h.deploy(o); d.Status != api.StatusActive {
		t.Fatalf("other: %s (%s)", d.Status, d.Error)
	}
	before, _ := h.rt.ImageIDs(ctx)

	var bad *InvalidImageError
	for _, ref := range []string{other, proxy} {
		// Another image under the tag: a line that is content, not a tag.
		_, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader(ref+"\n# layers built on the attacker's machine\n"))
		if !errors.As(err, &bad) {
			t.Fatalf("%s: err = %v, want InvalidImageError", ref, err)
		}
	}

	after, _ := h.rt.ImageIDs(ctx)
	for _, ref := range []string{other, proxy} {
		if after[ref] == "" || after[ref] != before[ref] {
			t.Errorf("%s names %q after the refused upload, named %q before", ref, after[ref], before[ref])
		}
	}
	known := map[string]bool{}
	for _, id := range before {
		known[id] = true
	}
	for ref, id := range after {
		if !known[id] {
			t.Errorf("%s names %s, which arrived with a refused upload", ref, id)
		}
	}
	removed := h.rt.RemovedImages()
	if len(removed) != 2 || !strings.HasPrefix(removed[0], "sha256:") || !strings.HasPrefix(removed[1], "sha256:") || known[removed[0]] || known[removed[1]] {
		t.Errorf("removed %v, want the two images that arrived, by ID, and no tag", removed)
	}
	if apps, _ := h.engine.Applications(ctx); len(apps) != 1 || apps[0].Image != other || apps[0].Status != api.AppHealthy {
		t.Errorf("the other application must be untouched: %+v", apps)
	}
	// It can still be deployed again: the image is there under its name.
	if d := h.deploy(o); d.Status != api.StatusActive {
		t.Errorf("other, deployed again: %s (%s)", d.Status, d.Error)
	}
}

func TestTheSameImageSentUnderAnExistingTagIsRefusedAndLeftAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := "shipwick.local/other:20260927-153000-a1b2"
	h.rt.AddLocalImage(other)
	before, _ := h.rt.ImageIDs(ctx)

	var bad *InvalidImageError
	if _, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader(other+"\n")); !errors.As(err, &bad) {
		t.Fatalf("err = %v, want InvalidImageError", err)
	}
	after, _ := h.rt.ImageIDs(ctx)
	if after[other] != before[other] {
		t.Errorf("%s names %q, named %q", other, after[other], before[other])
	}
	if removed := h.rt.RemovedImages(); len(removed) != 0 {
		t.Errorf("removed %v: the image was there before the upload", removed)
	}
}

func TestANewTagOfARefusedUploadIsUntaggedAndItsImageRemoved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var bad *InvalidImageError
	if _, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader("shipwick.local/my-api:a\nshipwick.local/my-api:b\n")); !errors.As(err, &bad) {
		t.Fatalf("err = %v, want InvalidImageError", err)
	}
	if ids, _ := h.rt.ImageIDs(ctx); len(ids) != 0 {
		t.Errorf("tags left: %v", ids)
	}
	// The image went with its last tag, as the daemon's does; removing it by
	// ID afterwards finds nothing, which is not an error.
	removed := h.rt.RemovedImages()
	if len(removed) != 2 || removed[0] != "shipwick.local/my-api:a" || removed[1] != "shipwick.local/my-api:b" {
		t.Errorf("removed %v, want the two tags", removed)
	}
}

func TestRedeployRefusesAnotherApplicationsLocalImage(t *testing.T) {
	h := newHarness(t)
	h.rt.AddLocalImage(localImage)
	h.deploy(built(localImage))

	var bad *InvalidImageError
	_, err := h.engine.Redeploy(context.Background(), "my-api", "shipwick.local/other:20260927-153000-a1b2")
	if !errors.As(err, &bad) || !strings.Contains(bad.Reason, "built for other") || !strings.Contains(bad.Reason, "shipwick deploy") {
		t.Errorf("err = %v, want a refusal that names other and says to deploy from its project", err)
	}
	if all, _ := h.store.ListDeployments(context.Background(), store.DeploymentFilter{Application: "my-api"}); len(all) != 1 {
		t.Errorf("a refused redeploy was recorded: %d deployments", len(all))
	}
	h.engine.Wait()
}
