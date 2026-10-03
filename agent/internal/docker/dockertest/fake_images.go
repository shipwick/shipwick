package dockertest

import (
	"context"
	"errors"
	"io"
	"sort"
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
	if f.LoadErr != nil {
		return nil, f.LoadErr
	}
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
