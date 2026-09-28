package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const localImage = "shipwick.local/my-api:20260927-153000-a1b2"

// built is an application whose image was built on a developer's machine.
func built(image string) spec.App {
	a := app("my-api", image, 1)
	a.Build = &spec.Build{Context: ".", Dockerfile: "Dockerfile"}
	return a
}

func TestALocalImageIsUsedWithoutAPull(t *testing.T) {
	h := newHarness(t)
	h.rt.AddLocalImage(localImage)

	d := h.deploy(built(localImage))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s), want ACTIVE", d.Status, d.Error)
	}
	if pulled := h.rt.Pulled(); len(pulled) != 0 {
		t.Errorf("nothing can be pulled from shipwick.local, yet pulled %v", pulled)
	}
	var said bool
	for _, e := range h.events(d.ID) {
		if e.Type == api.EventStep && strings.Contains(e.Message, "Using image "+localImage) {
			said = true
		}
	}
	if !said {
		t.Error("the deployment should say which image it used and where it came from")
	}
}

func TestAMissingLocalImageFailsTheDeployment(t *testing.T) {
	h := newHarness(t)

	d := h.deploy(built(localImage))
	if d.Status != api.StatusFailed {
		t.Fatalf("status = %s, want FAILED", d.Status)
	}
	if !strings.Contains(d.Error, localImage+" is not on this server") || !strings.Contains(d.Error, "run shipwick deploy from the project again") {
		t.Errorf("error = %q; it should say the image is missing and what to do", d.Error)
	}
	if pulled := h.rt.Pulled(); len(pulled) != 0 {
		t.Errorf("pulled %v", pulled)
	}
}

func TestLocalImagesArePrunedLikeOthersAndARollbackToAPrunedOneFails(t *testing.T) {
	h := newHarness(t)
	// Stamped now: images sent minutes ago and not deployed yet are left
	// alone by the sweep, as a deploy may be on its way.
	stamp := time.Now().UTC().Format("20060102-150405")
	v1, v2, v3 := "shipwick.local/my-api:"+stamp+"-0001", "shipwick.local/my-api:"+stamp+"-0002", "shipwick.local/my-api:"+stamp+"-0003"
	for _, image := range []string{v1, v2, v3} {
		h.rt.AddLocalImage(image)
	}
	first := h.deploy(built(v1))
	h.deploy(built(v2))
	h.deploy(built(v3))
	if got := h.rt.RemovedImages(); !slices.Equal(got, []string{v1}) {
		t.Fatalf("removed %v, want only the oldest: %s is the rollback target, %s runs", got, v2, v3)
	}

	// The image of the first version is gone, and there is nowhere to pull it from.
	d, err := h.engine.Rollback(context.Background(), "my-api", first.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	h.engine.Wait()
	final, _ := h.store.GetDeployment(context.Background(), d.ID)
	if final.Status != api.StatusFailed || !strings.Contains(final.Error, v1+" is not on this server") {
		t.Errorf("rollback to a pruned local image: status = %s, error = %q", final.Status, final.Error)
	}
	if pulled := h.rt.Pulled(); len(pulled) != 0 {
		t.Errorf("pulled %v", pulled)
	}
	if apps, _ := h.engine.Applications(context.Background()); len(apps) != 1 || apps[0].Status != api.AppHealthy || apps[0].Image != v3 {
		t.Errorf("the running version must be untouched: %+v", apps)
	}
}

func TestAReplicaWhoseLocalImageWasPrunedIsNotPulled(t *testing.T) {
	h := newHarness(t)
	h.rt.AddLocalImage(localImage)
	d := h.deploy(built(localImage))

	// The container is gone and its image was removed by hand since; Docker
	// refuses to create from what is not there.
	for _, c := range h.rt.Containers() {
		h.rt.RemoveContainer(context.Background(), c.ID)
	}
	if err := h.rt.RemoveImage(context.Background(), localImage); err != nil {
		t.Fatal(err)
	}
	h.rt.CreateHook = func(docker.ContainerSpec) error { return errors.New("No such image: " + localImage) }

	_, err := h.engine.createReplicas(context.Background(), d, []int{2})
	if err == nil || !strings.Contains(err.Error(), localImage+" is not on this server") {
		t.Errorf("err = %v; want the missing-image sentence", err)
	}
	if pulled := h.rt.Pulled(); len(pulled) != 0 {
		t.Errorf("pulled %v", pulled)
	}
}

func TestLoadImageAcceptsOnlyTheApplicationsOwnImage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	loaded, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader(localImage+"\n"))
	if err != nil {
		t.Fatalf("LoadImage: %v", err)
	}
	if loaded.Image != localImage || loaded.SizeBytes != int64(len(localImage)+1) {
		t.Errorf("loaded = %+v", loaded)
	}
	if ok, _ := h.rt.ImageExists(ctx, localImage); !ok {
		t.Error("the image should be on the server now")
	}

	var bad *InvalidImageError
	for name, body := range map[string]string{
		"another application's image": "shipwick.local/other:20260927-153000-a1b2\n",
		"a registry image":            "ghcr.io/company/my-api:1.0\n",
		"the bare prefix":             "shipwick.local/my-api:\n",
		"two images":                  "shipwick.local/my-api:a\nshipwick.local/my-api:b\n",
	} {
		_, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader(body))
		if !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want InvalidImageError", name, err)
		}
	}
	for _, ref := range []string{"shipwick.local/other:20260927-153000-a1b2", "ghcr.io/company/my-api:1.0", "shipwick.local/my-api:a"} {
		if ok, _ := h.rt.ImageExists(ctx, ref); ok {
			t.Errorf("%s was refused and must not stay on the server", ref)
		}
	}
	if _, err := h.engine.LoadImage(ctx, "my-api", strings.NewReader("")); err == nil {
		t.Error("an archive without an image must be an error")
	}
}

func TestADeployWithBuildNeedsTheImageTheCLIFillsIn(t *testing.T) {
	h := newHarness(t)
	_, err := h.engine.Deploy(context.Background(), built(""))
	if !errors.Is(err, ErrImageNotBuilt) {
		t.Errorf("err = %v, want ErrImageNotBuilt: the agent never builds", err)
	}
	if apps, _ := h.engine.Applications(context.Background()); len(apps) != 0 {
		t.Errorf("nothing may be recorded: %+v", apps)
	}
}

func TestRedeployOfABuiltApplicationRefusesARegistryImage(t *testing.T) {
	h := newHarness(t)
	h.rt.AddLocalImage(localImage)
	h.deploy(built(localImage))

	var bad *InvalidImageError
	if _, err := h.engine.Redeploy(context.Background(), "my-api", "ghcr.io/company/my-api:1.0"); !errors.As(err, &bad) {
		t.Errorf("err = %v, want InvalidImageError", err)
	}
	if _, err := h.engine.Redeploy(context.Background(), "my-api", ""); err != nil {
		t.Errorf("a plain redeploy keeps the image: %v", err)
	}
	h.engine.Wait()
}

func TestALocalImageNobodyDeployedIsRemovedWithTheNextSweep(t *testing.T) {
	h := newHarness(t)
	// One upload was followed by a deploy the agent refused; the next one went
	// through. The first image names no deployment, so nothing else would
	// ever remove it. A fresh one is left alone: its deploy may be on its way.
	orphan := "shipwick.local/my-api:20260101-000000-ee80"
	fresh := "shipwick.local/my-api:" + time.Now().UTC().Format("20060102-150405") + "-ffff"
	h.rt.AddLocalImage(orphan)
	h.rt.AddLocalImage(fresh)
	h.rt.AddLocalImage(localImage)

	d := h.deploy(built(localImage))
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s), want ACTIVE", d.Status, d.Error)
	}
	removed := h.rt.RemovedImages()
	if len(removed) != 1 || removed[0] != orphan {
		t.Errorf("removed %v, want only the stale orphan %s; the fresh %s may still be deployed", removed, orphan, fresh)
	}
	if ok, _ := h.rt.ImageExists(context.Background(), localImage); !ok {
		t.Error("the active image must stay")
	}

	// Deleting the application takes its remaining local images with it.
	h.rt.AddLocalImage("shipwick.local/my-api:20260928-000000-aaaa")
	if err := h.engine.Delete(context.Background(), "my-api"); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.rt.ListImages(context.Background(), "shipwick.local/my-api"); len(left) != 0 {
		t.Errorf("after delete, local images left: %v", left)
	}
}

func TestAnImageAlreadyGoneIsNotCountedAsRemoved(t *testing.T) {
	h := newHarness(t)
	stamp := time.Now().UTC().Format("20060102-150405")
	v1, v2 := "shipwick.local/my-api:"+stamp+"-0001", "shipwick.local/my-api:"+stamp+"-0002"
	h.rt.AddLocalImage(v1)
	h.rt.AddLocalImage(v2)
	h.deploy(built(v1))
	d := h.deploy(built(v2))
	// v1 is the rollback target and stays. Someone removes it by hand; the
	// next rollback's sweep finds nothing to do and must say nothing.
	if err := h.rt.RemoveImage(context.Background(), v1); err != nil {
		t.Fatal(err)
	}
	h.rt.AddLocalImage(v1)
	d, err := h.engine.Rollback(context.Background(), "my-api", d.ID-1)
	if err != nil {
		t.Fatal(err)
	}
	h.engine.Wait()
	for _, e := range h.events(d.ID) {
		if strings.Contains(e.Message, "Removed") {
			t.Errorf("a rollback with nothing to remove said %q", e.Message)
		}
	}
}
