package dockertest

import (
	"context"
	"errors"
	"io"
	"strings"
)

// LoadImage loads an image archive. The fake has no image store to unpack a
// real archive into, so the archive body stands for its manifest: a
// newline-separated list of the references it carries, each of which becomes
// a local image. An empty body is what the daemon would call a bad archive.
func (f *Fake) LoadImage(_ context.Context, archive io.Reader) ([]string, error) {
	body, err := io.ReadAll(archive)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(string(body), "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil, errors.New("the archive holds no image")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ref := range refs {
		f.local[ref] = true
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
