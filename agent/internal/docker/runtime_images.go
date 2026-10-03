package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/client"
)

// LoadImage loads an image archive written by `docker save` and returns the
// references it carried. The daemon reports each as a progress line,
// "Loaded image: <ref>"; an image the archive does not tag is reported by ID
// and not returned, since nothing in a deployment could name it.
func (r *Runtime) LoadImage(ctx context.Context, archive io.Reader) ([]string, error) {
	res, err := r.cli.ImageLoad(ctx, archive, client.ImageLoadWithQuiet(false))
	if err != nil {
		return nil, fmt.Errorf("load image: %w", err)
	}
	defer res.Close()

	refs, err := loadedImages(res)
	if errors.Is(err, ErrImageIncomplete) {
		r.discardUnpacked(ctx, refs)
		return nil, err
	}
	return refs, err
}

// loadedImages reads the daemon's answer to a load: a stream of JSON
// messages, the same format a pull produces, with progress in "stream" and a
// failure in "error". An archive without all of its image's layers ends in
// ErrImageIncomplete, next to the references the daemon recorded all the
// same; see runtime_layers.go.
func loadedImages(answer io.Reader) ([]string, error) {
	var msg struct {
		Stream string `json:"stream"`
		Error  string `json:"error"`
	}
	var refs []string
	var incomplete bool
	dec := json.NewDecoder(answer)
	for {
		msg.Stream, msg.Error = "", ""
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("load image: read the daemon's answer: %w", err)
		}
		if msg.Error != "" {
			if missingBlob(msg.Error) {
				return nil, fmt.Errorf("load image: %w", ErrImageIncomplete)
			}
			return nil, fmt.Errorf("load image: %s", msg.Error)
		}
		line := strings.TrimSpace(msg.Stream)
		if ref, ok := strings.CutPrefix(line, "Loaded image: "); ok {
			refs = append(refs, ref)
		}
		if strings.HasPrefix(line, unpackFailure) {
			incomplete = true
		}
	}
	if incomplete {
		return refs, fmt.Errorf("load image: %w", ErrImageIncomplete)
	}
	return refs, nil
}

// ListImages returns the references tagged under one repository, in no
// particular order. The daemon's reference filter takes the repository as
// it is and matches every tag of it.
func (r *Runtime) ListImages(ctx context.Context, repository string) ([]string, error) {
	filters := client.Filters{}
	filters.Add("reference", repository)
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list images of %s: %w", repository, err)
	}
	var refs []string
	for _, img := range res.Items {
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, repository+":") {
				refs = append(refs, tag)
			}
		}
	}
	return refs, nil
}
