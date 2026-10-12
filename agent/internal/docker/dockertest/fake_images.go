package dockertest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The fake's image store knows images the way the daemon does: by ID, with
// tags that name an ID and can be moved from one to another. An ID is the
// digest of what was loaded, as the daemon's is of the image's content, so
// that a save and a load of an image give it the ID it had, and an archive
// with other content under the same tag gives the tag another image.

// LoadImage loads an image archive. The fake has no image store to unpack a
// real archive into, so the archive body stands for its manifest: a
// newline-separated list of the references it carries, each of which becomes
// a local image naming the one ID the whole body digests to. A line that
// starts with # is content and not a reference: what tells one archive from
// another, as the layers would. An empty body is what the daemon would call a
// bad archive.
func (f *Fake) LoadImage(_ context.Context, archive io.Reader) ([]string, error) {
	body, err := io.ReadAll(archive)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(string(body), "\n") {
		if ref := strings.TrimSpace(line); ref != "" && !strings.HasPrefix(ref, "#") {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil, errors.New("the archive holds no image")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LoadErr != nil {
		return nil, f.LoadErr
	}
	id := imageID(body)
	for _, ref := range refs {
		f.addImage(ref, id)
	}
	f.loaded = append(f.loaded, refs...)
	return refs, nil
}

// LoadedImages returns every reference loaded so far, in order.
func (f *Fake) LoadedImages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.loaded...)
}

// ListImages lists the local images tagged under one repository.
func (f *Fake) ListImages(_ context.Context, repository string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var refs []string
	for ref := range f.local {
		if strings.HasPrefix(ref, repository+":") {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

// ImageIDs maps every tag to the ID of the image it names.
func (f *Fake) ImageIDs(_ context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.ids))
	for ref, id := range f.ids {
		out[ref] = id
	}
	return out, nil
}

// TagImage points a tag at an image, moving it if it names another.
func (f *Fake) TagImage(_ context.Context, id, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[id] {
		return fmt.Errorf("tag image %s: no such image", id)
	}
	f.addImage(ref, id)
	return nil
}

// imageID is the ID of an image whose archive is content.
func imageID(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func isImageID(ref string) bool { return strings.HasPrefix(ref, "sha256:") }

// addImage makes ref name id. An image ref named before stays, untagged, as
// the daemon leaves it. Called with the lock held.
func (f *Fake) addImage(ref, id string) {
	f.local[ref] = true
	f.ids[ref] = id
	f.images[id] = true
}

// dropRef untags ref; an image with no tag left goes with it, as it does
// when the daemon untags by reference. Called with the lock held.
func (f *Fake) dropRef(ref string) {
	id := f.ids[ref]
	delete(f.local, ref)
	delete(f.ids, ref)
	if id != "" && len(f.refsOf(id)) == 0 {
		delete(f.images, id)
	}
}

// refsOf lists the tags that name id. Called with the lock held.
func (f *Fake) refsOf(id string) []string {
	var refs []string
	for ref, named := range f.ids {
		if named == id {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}
