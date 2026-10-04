package commands

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// The images of a bundle are proven the way its files are: the release
// publishes image-digests.txt (scripts/image-digests.sh), listed in its
// checksums.txt like every other file, and the archive `docker save` wrote is
// read here, byte by byte, before it goes into the bundle. An image is the
// release's when the archive holds the configuration the release published
// for the server's platform, the layers that configuration lists, and names
// that image, and no other, by the release's tag.
//
// What the archive looks like depends on how the Docker that wrote it stores
// images. With the containerd image store it holds what the registry served:
// the platform's manifest, the configuration and the compressed layers. With
// the classic store it holds the configuration and the layers unpacked, under
// a manifest Docker wrote for the occasion. The configuration is the same
// file in both, and it lists the layers by the digest of their unpacked
// content, so both can be followed from the release's digests to the bytes.

// bundleDigests is the release's file of image digests.
const bundleDigests = "image-digests.txt"

// maxImageDocument bounds what is kept of an archive to look inside: its
// manifests and configurations, which are a few kilobytes.
const maxImageDocument = 4 << 20

// imageDigests is what a release published about one image for one platform.
type imageDigests struct {
	index, manifest, config string
}

// digestsFor finds an image's lines in image-digests.txt:
//
//	<name> index    sha256:…
//	<name> manifest sha256:… linux/amd64
//	<name> config   sha256:… linux/amd64
func digestsFor(published []byte, image, platform string) (imageDigests, bool) {
	var d imageDigests
	for _, line := range strings.Split(string(published), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != image || !isDigest(fields[2]) {
			continue
		}
		switch {
		case fields[1] == "index" && len(fields) == 3:
			d.index = fields[2]
		case fields[1] == "manifest" && len(fields) == 4 && fields[3] == platform:
			d.manifest = fields[2]
		case fields[1] == "config" && len(fields) == 4 && fields[3] == platform:
			d.config = fields[2]
		}
	}
	return d, d.index != "" && d.manifest != "" && d.config != ""
}

func isDigest(s string) bool {
	hexPart, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(hexPart) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(hexPart)
	return err == nil
}

// savedImages is a `docker save` archive, read once.
type savedImages struct {
	// sums is the SHA-256 of every file by its name; digests is the same
	// set by digest, for "is this content here".
	sums    map[string]string
	digests map[string]bool
	// documents are the files small enough to be a manifest or a
	// configuration, by digest.
	documents map[string][]byte
}

func readSavedImages(path string) (*savedImages, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	a := &savedImages{sums: map[string]string{}, digests: map[string]bool{}, documents: map[string][]byte{}}
	for tr := tar.NewReader(f); ; {
		h, err := tr.Next()
		if err == io.EOF {
			return a, nil
		}
		if err != nil {
			return nil, fmt.Errorf("the archive docker saved cannot be read: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		sum := sha256.New()
		var kept bytes.Buffer
		w := io.Writer(sum)
		if h.Size <= maxImageDocument {
			w = io.MultiWriter(sum, &kept)
		}
		if _, err := io.Copy(w, tr); err != nil {
			return nil, fmt.Errorf("the archive docker saved cannot be read: %w", err)
		}
		digest := "sha256:" + hex.EncodeToString(sum.Sum(nil))
		// A file under blobs/ is named after its content.
		if name, ok := strings.CutPrefix(h.Name, "blobs/sha256/"); ok && "sha256:"+name != digest {
			return nil, fmt.Errorf("the archive docker saved is damaged: %s holds other content than its name says", h.Name)
		}
		a.sums[h.Name], a.digests[digest] = digest, true
		if h.Size <= maxImageDocument {
			a.documents[digest] = kept.Bytes()
		}
	}
}

// document decodes the manifest, index or configuration with that digest.
func (a *savedImages) document(digest string, into any) bool {
	data, ok := a.documents[digest]
	return ok && json.Unmarshal(data, into) == nil
}

type ociDescriptor struct {
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
}

// ociManifest reads a manifest and an index alike: one has a configuration
// and layers, the other manifests.
type ociManifest struct {
	Config    ociDescriptor   `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

// verify checks that the archive holds image as the release published it,
// and names nothing else by that tag.
func (a *savedImages) verify(image string, want imageDigests) error {
	var config struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if !a.document(want.config, &config) || len(config.RootFS.DiffIDs) == 0 {
		return fmt.Errorf("the archive does not hold the configuration the release published for it (%s)", want.config)
	}

	// served: the manifest and the layers as the registry serves them.
	// follows: m is the release's manifest, or one written by docker save
	// around the release's configuration and its unpacked layers.
	served := func() bool {
		var m ociManifest
		if !a.document(want.manifest, &m) || m.Config.Digest != want.config {
			return false
		}
		for _, layer := range m.Layers {
			if !a.digests[layer.Digest] {
				return false
			}
		}
		return len(m.Layers) > 0
	}()
	follows := func(digest string) bool {
		if digest == want.manifest {
			return served
		}
		var m ociManifest
		if !a.document(digest, &m) || m.Config.Digest != want.config || len(m.Layers) != len(config.RootFS.DiffIDs) {
			return false
		}
		for i, layer := range m.Layers {
			if layer.Digest != config.RootFS.DiffIDs[i] || !a.digests[layer.Digest] {
				return false
			}
		}
		return true
	}
	if !served {
		for _, id := range config.RootFS.DiffIDs {
			if !a.digests[id] {
				return fmt.Errorf("the archive does not hold every layer of the configuration the release published (%s is missing)", id)
			}
		}
	}

	// A Docker with the classic store loads by manifest.json, one with the
	// containerd store by index.json: both must name the image, and both
	// must mean the release's.
	named := 0
	if digest, ok := a.sums["manifest.json"]; ok {
		var entries []struct {
			Config   string
			RepoTags []string
		}
		if !a.document(digest, &entries) {
			return errors.New("the archive's manifest.json cannot be read")
		}
		for _, entry := range entries {
			for _, tag := range entry.RepoTags {
				if tag != image {
					continue
				}
				named++
				if a.sums[entry.Config] != want.config {
					return fmt.Errorf("the archive names another image by this tag (configuration %s, published %s)", a.sums[entry.Config], want.config)
				}
			}
		}
	}
	if digest, ok := a.sums["index.json"]; ok {
		var index ociManifest
		if !a.document(digest, &index) {
			return errors.New("the archive's index.json cannot be read")
		}
		for _, entry := range index.Manifests {
			if entry.Annotations["io.containerd.image.name"] != image {
				continue
			}
			named++
			// Saved without a platform, the entry is the manifest list
			// itself, which names the platform's manifest.
			if entry.Digest == want.index && served {
				continue
			}
			if !follows(entry.Digest) {
				return fmt.Errorf("the archive names another image by this tag (manifest %s, published %s)", entry.Digest, want.manifest)
			}
		}
	}
	if named == 0 {
		return errors.New("the archive does not name it")
	}
	return nil
}

// verifyImageArchive checks the archive at path against the digests the
// release published for platform. Every image must be the release's.
func verifyImageArchive(path string, images []string, published []byte, tag, platform string) error {
	archive, err := readSavedImages(path)
	if err != nil {
		return err
	}
	for _, image := range images {
		want, ok := digestsFor(published, image, platform)
		if !ok {
			return fmt.Errorf("release %s publishes no digests for %s on %s (looked in its %s); no bundle was written", tag, image, platform, bundleDigests)
		}
		if err := archive.verify(image, want); err != nil {
			return fmt.Errorf("%s is not the image release %s published for %s: %w; no bundle was written\n\n"+
				"The registry serves another image under the tag than it did when the release was cut, or the archive was\n"+
				"changed while it was written. Try again; if it happens again, report it: https://github.com/%s/issues",
				image, tag, platform, err, releaseRepo)
		}
	}
	return nil
}
