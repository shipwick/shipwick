//go:build integration

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// testLayer is a layer of one file: its tar, and the digest of that tar, which
// is both its diff ID and — the layer being stored uncompressed — its blob.
type testLayer struct {
	tar    []byte
	digest string // 64 hex characters
}

func newTestLayer(t *testing.T, file, content string) testLayer {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: file, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(content))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return testLayer{tar: buf.Bytes(), digest: hexDigest(buf.Bytes())}
}

func (l testLayer) diffID() string { return "sha256:" + l.digest }

func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// saveArchive writes what `docker save` writes for an image of these layers —
// an OCI layout with a manifest.json — leaving out the blobs of the layers in
// omit, as the CLI does for layers the server has.
func saveArchive(t *testing.T, ref, architecture string, layers []testLayer, omit ...testLayer) []byte {
	t.Helper()
	type descriptor struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int               `json:"size"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}
	marshal := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	var diffIDs, files []string
	var layerDescriptors []descriptor
	for _, l := range layers {
		diffIDs = append(diffIDs, l.diffID())
		files = append(files, "blobs/sha256/"+l.digest)
		layerDescriptors = append(layerDescriptors, descriptor{MediaType: "application/vnd.oci.image.layer.v1.tar", Digest: l.diffID(), Size: len(l.tar)})
	}
	config := marshal(map[string]any{
		"architecture": architecture,
		"os":           "linux",
		"config":       map[string]any{"Cmd": []string{"/bin/true"}},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": diffIDs},
	})
	manifest := marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:" + hexDigest(config), Size: len(config)},
		"layers":        layerDescriptors,
	})
	index := marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []descriptor{{
			MediaType:   "application/vnd.oci.image.manifest.v1+json",
			Digest:      "sha256:" + hexDigest(manifest),
			Size:        len(manifest),
			Annotations: map[string]string{"io.containerd.image.name": ref},
		}},
	})
	legacy := marshal([]map[string]any{{"Config": "blobs/sha256/" + hexDigest(config), "RepoTags": []string{ref}, "Layers": files}})

	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	add := func(name string, content []byte) {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o444, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		w.Write(content)
	}
	for _, l := range layers {
		if !slices.ContainsFunc(omit, func(o testLayer) bool { return o.digest == l.digest }) {
			add("blobs/sha256/"+l.digest, l.tar)
		}
	}
	add("blobs/sha256/"+hexDigest(config), config)
	add("blobs/sha256/"+hexDigest(manifest), manifest)
	add("index.json", index)
	add("manifest.json", legacy)
	add("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An archive may leave out the layers the daemon has, and only those: the
// promise the CLI's reduced uploads rest on, kept by both image stores.
func TestIntegrationLoadImageWithoutTheLayersTheDaemonHas(t *testing.T) {
	rt, ctx := newIntegrationRuntime(t)
	info, err := rt.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	architecture := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[info.Architecture]
	if architecture == "" {
		t.Skipf("no test image for %s", info.Architecture)
	}

	// Unique to this run: a layer left behind by another must not be found.
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	repository := "shipwick.local/it-layers-" + run
	base := newTestLayer(t, "base.txt", "base "+run)
	one := newTestLayer(t, "app.txt", "one "+run)
	two := newTestLayer(t, "app.txt", "two "+run)
	other := newTestLayer(t, "base.txt", "another base "+run)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, tag := range []string{"1", "2", "3"} {
			rt.RemoveImage(cleanup, repository+":"+tag)
		}
	})

	refs, err := rt.LoadImage(ctx, bytes.NewReader(saveArchive(t, repository+":1", architecture, []testLayer{base, one})))
	if err != nil || !slices.Equal(refs, []string{repository + ":1"}) {
		t.Fatalf("load the whole image: %v, %v", refs, err)
	}
	images, err := rt.ImageLayers(ctx)
	if err != nil {
		t.Fatalf("ImageLayers: %v", err)
	}
	if !slices.ContainsFunc(images, func(layers []string) bool { return slices.Equal(layers, []string{base.diffID(), one.diffID()}) }) {
		t.Fatalf("ImageLayers does not report the image just loaded, %v, among %d images", []string{base.diffID(), one.diffID()}, len(images))
	}

	// The next version, without the base layer the daemon now has.
	refs, err = rt.LoadImage(ctx, bytes.NewReader(saveArchive(t, repository+":2", architecture, []testLayer{base, two}, base)))
	if err != nil || !slices.Equal(refs, []string{repository + ":2"}) {
		t.Fatalf("load an image without the layer the daemon has: %v, %v", refs, err)
	}
	spec := ContainerSpec{App: "it-layers", DeploymentID: 1, Sequence: 1, Replica: 1, Image: repository + ":2"}
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	id, _, err := rt.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatalf("an image loaded that way must be whole enough to create a container from: %v", err)
	}
	if err := rt.RemoveContainer(ctx, id); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}

	// One that leaves out a layer the daemon does not have is refused, and
	// nothing of it stays.
	_, err = rt.LoadImage(ctx, bytes.NewReader(saveArchive(t, repository+":3", architecture, []testLayer{other, two}, other)))
	if !errors.Is(err, ErrImageIncomplete) {
		t.Fatalf("err = %v, want ErrImageIncomplete", err)
	}
	if ok, err := rt.ImageExists(ctx, repository+":3"); err != nil || ok {
		t.Errorf("the refused image must not exist: %v, %v", ok, err)
	}
}
