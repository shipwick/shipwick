package commands

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// bundledImage is one image of a test archive: what a registry would hold of
// it, and what the classic image store would write instead.
type bundledImage struct {
	name             string
	config, manifest []byte
	// layers are the compressed layers the manifest lists; unpacked the
	// content the configuration lists by diff ID.
	layers, unpacked [][]byte
}

func newBundledImage(name string) bundledImage {
	img := bundledImage{name: name}
	var diffIDs, layers []string
	for i := range 2 {
		unpacked := []byte(fmt.Sprintf("%s layer %d, unpacked", name, i))
		layer := []byte(fmt.Sprintf("%s layer %d, compressed", name, i))
		img.unpacked, img.layers = append(img.unpacked, unpacked), append(img.layers, layer)
		diffIDs = append(diffIDs, fmt.Sprintf("%q", digestOf(unpacked)))
		layers = append(layers, fmt.Sprintf(`{"digest":%q}`, digestOf(layer)))
	}
	img.config = []byte(fmt.Sprintf(`{"architecture":"arm64","rootfs":{"type":"layers","diff_ids":[%s]}}`, strings.Join(diffIDs, ",")))
	img.manifest = []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[%s]}`, digestOf(img.config), strings.Join(layers, ",")))
	return img
}

// published is the image's lines in the release's image-digests.txt.
func (img bundledImage) published(platform string) string {
	return fmt.Sprintf("%[1]s index %[2]s\n%[1]s manifest %[3]s %[5]s\n%[1]s config %[4]s %[5]s\n",
		img.name, digestOf([]byte("index of "+img.name)), digestOf(img.manifest), digestOf(img.config), platform)
}

// archive writes images the way `docker save` does: with the containerd
// image store what the registry served, with the classic store the layers
// unpacked under a manifest of Docker's own.
func archive(t *testing.T, classic bool, images ...bundledImage) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, content []byte) {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(content)
	}
	blob := func(content []byte) string {
		name := "blobs/sha256/" + strings.TrimPrefix(digestOf(content), "sha256:")
		add(name, content)
		return name
	}
	type entry struct {
		Config   string
		RepoTags []string
	}
	var entries []entry
	var index []string
	for _, img := range images {
		manifest := img.manifest
		layers := img.layers
		if classic {
			var descriptors []string
			for _, layer := range img.unpacked {
				descriptors = append(descriptors, fmt.Sprintf(`{"digest":%q}`, digestOf(layer)))
			}
			manifest = []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[%s]}`, digestOf(img.config), strings.Join(descriptors, ",")))
			layers = img.unpacked
		}
		for _, layer := range layers {
			blob(layer)
		}
		blob(manifest)
		entries = append(entries, entry{Config: blob(img.config), RepoTags: []string{img.name}})
		index = append(index, fmt.Sprintf(`{"digest":%q,"annotations":{"io.containerd.image.name":%q}}`, digestOf(manifest), img.name))
	}
	manifestJSON, _ := json.Marshal(entries)
	add("manifest.json", manifestJSON)
	add("index.json", []byte(`{"schemaVersion":2,"manifests":[`+strings.Join(index, ",")+`]}`))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// provenRelease is a release that publishes the digests of its images, and
// the three images as its registry serves them.
func provenRelease() (*fakeRelease, []bundledImage) {
	release := newFakeRelease()
	var images []bundledImage
	for _, name := range []string{"agent", "caddy", "dashboard"} {
		img := newBundledImage("ghcr.io/shipwick/" + name + ":0.6.0")
		images = append(images, img)
		release.assets[bundleDigests] += img.published("linux/arm64") + img.published("linux/amd64")
	}
	return release, images
}

func TestServerBundleProvesItsImagesAgainstTheReleasesDigests(t *testing.T) {
	for name, classic := range map[string]bool{"as the containerd image store saves them": false, "as the classic image store saves them": true} {
		t.Run(name, func(t *testing.T) {
			release, images := provenRelease()
			docker := &bundleDocker{saved: archive(t, classic, images...)}
			f := newFakeAgent(t)
			f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
			dir := t.TempDir()

			out, errOut, err := f.run(dir, "server", "bundle", "--arch", "arm64")
			if err != nil {
				t.Fatalf("server bundle: %v\n%s", err, out)
			}
			if !strings.Contains(out, "The three images are the ones release v0.6.0 published for linux/arm64") || errOut != "" {
				t.Errorf("the images were not said to be proven:\n%s%s", out, errOut)
			}
			sums := sha256.Sum256([]byte(readBundleFile(t, dir, "checksums.txt")))
			if !strings.Contains(out, "checksums.txt of the release: sha256 "+hex.EncodeToString(sums[:])) {
				t.Errorf("no line to compare the checksums by:\n%s", out)
			}
			if got := readBundleFile(t, dir, bundleDigests); got != release.assets[bundleDigests] {
				t.Errorf("the bundle does not carry the release's %s: %q", bundleDigests, got)
			}
		})
	}
}

func readBundleFile(t *testing.T, dir, name string) string {
	t.Helper()
	files, _ := readBundle(t, filepath.Join(dir, "shipwick-v0.6.0-linux-arm64.tar.gz"))
	return files["shipwick-v0.6.0-linux-arm64/"+name]
}

func TestServerBundleRefusesImagesTheReleaseDidNotPublish(t *testing.T) {
	release, images := provenRelease()
	tampered := func(change func(img *bundledImage)) []bundledImage {
		changed := append([]bundledImage(nil), images...)
		change(&changed[1])
		return changed
	}
	cases := map[string]struct {
		saved []byte
		want  string
	}{
		"another image under the release's tag": {
			saved: archive(t, false, tampered(func(img *bundledImage) { *img = newBundledImage(img.name); img.config = append(img.config, ' ') })...),
			want:  "ghcr.io/shipwick/caddy:0.6.0 is not the image release v0.6.0 published for linux/arm64: the archive does not hold the configuration the release published",
		},
		"a layer that is not the configuration's": {
			saved: archive(t, true, tampered(func(img *bundledImage) { img.unpacked = [][]byte{img.unpacked[0], []byte("something else")} })...),
			want:  "ghcr.io/shipwick/caddy:0.6.0 is not the image release v0.6.0 published for linux/arm64: the archive does not hold every layer",
		},
		"the release's image, and the tag on another": {
			saved: func() []byte {
				other := newBundledImage("ghcr.io/shipwick/caddy:0.6.0")
				other.config = append(other.config, ' ')
				other.manifest = []byte(strings.Replace(string(other.manifest), digestOf(images[1].config), digestOf(other.config), 1))
				hidden := images[1]
				hidden.name = "ghcr.io/shipwick/caddy:hidden"
				return archive(t, false, images[0], hidden, other, images[2])
			}(),
			want: "ghcr.io/shipwick/caddy:0.6.0 is not the image release v0.6.0 published for linux/arm64: the archive names another image by this tag",
		},
		"an image the archive does not name": {
			saved: archive(t, false, images[0], images[2]),
			want:  "ghcr.io/shipwick/caddy:0.6.0 is not the image release v0.6.0 published for linux/arm64: the archive does not hold the configuration",
		},
		"a file that is not what its name says": {
			saved: bytes.Replace(archive(t, false, images...), []byte("layer 1, compressed"), []byte("layer 1, compromised"), 1),
			want:  "the archive docker saved is damaged: blobs/sha256/",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docker := &bundleDocker{saved: tc.saved}
			f := newFakeAgent(t)
			f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
			dir := t.TempDir()

			_, _, err := f.run(dir, "server", "bundle", "--arch", "arm64")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
			if left, _ := os.ReadDir(dir); len(left) != 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

func TestServerBundleRefusesImagesOfAnotherPlatform(t *testing.T) {
	release, images := provenRelease()
	release.assets[bundleDigests] = strings.ReplaceAll(release.assets[bundleDigests], images[0].published("linux/amd64"), "")
	docker := &bundleDocker{saved: archive(t, false, images...)}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}

	_, _, err := f.run(t.TempDir(), "server", "bundle", "--arch", "amd64")
	if err == nil || !strings.Contains(err.Error(), "release v0.6.0 publishes no digests for ghcr.io/shipwick/agent:0.6.0 on linux/amd64") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerBundleSaysWhenItsImagesAreNotProven(t *testing.T) {
	proven, images := provenRelease()
	cases := map[string]struct {
		release *fakeRelease
		args    []string
		want    string
	}{
		"images the machine already had": {
			release: proven, args: []string{"--no-pull"},
			want: "The images are the ones this machine had under the release's names (--no-pull): they were not checked against the release's digests",
		},
		"a release from before the digests": {
			release: newFakeRelease(),
			want:    "Release v0.6.0 does not publish the digests of its images (0.7.0 and later do)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docker := &bundleDocker{saved: archive(t, false, images...)}
			f := newFakeAgent(t)
			f.upgrade, f.build = tc.release.serve(t), buildTools{run: docker.run}
			dir := t.TempDir()

			out, errOut, err := f.run(dir, append([]string{"server", "bundle", "--arch", "arm64"}, tc.args...)...)
			if err != nil {
				t.Fatalf("server bundle: %v\n%s", err, out)
			}
			if !strings.Contains(errOut, tc.want) || strings.Contains(out, "published for") {
				t.Errorf("out:\n%s\nerr:\n%s", out, errOut)
			}
			// Its absence is how the installer knows.
			if files, _ := readBundle(t, filepath.Join(dir, "shipwick-v0.6.0-linux-arm64.tar.gz")); files["shipwick-v0.6.0-linux-arm64/"+bundleDigests] != "" {
				t.Errorf("the bundle carries digests nothing was checked against")
			}
		})
	}
}

func TestServerBundleRefusesDigestsThatAreNotTheReleases(t *testing.T) {
	release, images := provenRelease()
	release.wrong = bundleDigests
	docker := &bundleDocker{saved: archive(t, false, images...)}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}

	_, _, err := f.run(t.TempDir(), "server", "bundle")
	if err == nil || !strings.Contains(err.Error(), "image-digests.txt does not match the checksum published with the release; no bundle was written") {
		t.Fatalf("err = %v", err)
	}
	if docker.ran("pull") != 0 {
		t.Errorf("images were pulled before the digests were known to be the release's: %v", docker.calls)
	}
}

// withFile returns the archive with one of its files replaced.
func withFile(t *testing.T, archive []byte, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for tr := tar.NewReader(bytes.NewReader(archive)); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		if h.Name == name {
			data = content
			h.Size = int64(len(content))
		}
		tw.WriteHeader(h)
		tw.Write(data)
	}
	tw.Close()
	return buf.Bytes()
}

func fileOf(t *testing.T, archive []byte, name string) []byte {
	t.Helper()
	for tr := tar.NewReader(bytes.NewReader(archive)); ; {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("%s is not in the archive: %v", name, err)
		}
		if h.Name == name {
			data, _ := io.ReadAll(tr)
			return data
		}
	}
}

// Docker loads by manifest.json with the classic image store and by
// index.json with containerd's: an archive that is right in one and wrong in
// the other would be the release's image on one server and not on the next.
func TestServerBundleRefusesAnArchiveThatNamesTheImageInOneTableOnly(t *testing.T) {
	release, images := provenRelease()
	good := archive(t, false, images...)
	other := newBundledImage("ghcr.io/shipwick/caddy:0.6.0")
	other.config = append(other.config, ' ')
	other.manifest = []byte(strings.Replace(string(other.manifest), digestOf(images[1].config), digestOf(other.config), 1))
	hidden := images[1]
	hidden.name = "ghcr.io/shipwick/caddy:hidden"
	swapped := archive(t, false, images[0], hidden, other, images[2])

	cases := map[string]struct {
		saved []byte
		want  string
	}{
		"manifest.json puts the tag on another image": {
			saved: withFile(t, swapped, "index.json", fileOf(t, good, "index.json")),
			want:  "the archive names another image by this tag (configuration " + digestOf(other.config),
		},
		"index.json puts the tag on another image": {
			saved: withFile(t, swapped, "manifest.json", fileOf(t, good, "manifest.json")),
			want:  "the archive names another image by this tag (manifest " + digestOf(other.manifest),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docker := &bundleDocker{saved: tc.saved}
			f := newFakeAgent(t)
			f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
			_, _, err := f.run(t.TempDir(), "server", "bundle", "--arch", "arm64")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
		})
	}
}
