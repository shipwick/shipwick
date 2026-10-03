package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// An image built on a developer's machine shares most of itself with the one
// sent before it: the base image's layers, the dependencies'. The CLI asks
// which layers the server lacks and leaves the others out of the archive.
//
// A layer is identified by its diff ID, the digest of its uncompressed
// content, and it is on the server when an image there starts with the same
// diff IDs up to and including it. That is the rule Docker itself goes by when
// an archive is loaded, in both of its image stores: the classic one keeps
// layers by the chain of diff IDs beneath them, the containerd one keeps
// snapshots by the same chain, and neither opens the file of a layer it
// already has. So the answer is a suffix of the question.
//
// The answer is advice, not a promise: an image can be pruned between the
// question and the upload, and the client may be wrong or lying about what it
// leaves out. Either way the daemon finds a layer it cannot produce and the
// load fails with ErrIncompleteImage; nothing half-loaded stays.

// ErrIncompleteImage means an uploaded archive left out layers the server
// does not have.
var ErrIncompleteImage = fmt.Errorf("image: %w; send the whole image", docker.ErrImageIncomplete)

// MissingLayers returns the layers of an image — its diff IDs, base layer
// first — that the server's Docker does not have, in the same order.
func (e *Engine) MissingLayers(ctx context.Context, layers []string) ([]string, error) {
	have, err := e.rt.ImageLayers(ctx)
	if err != nil {
		return nil, err
	}
	return layers[sharedLayers(layers, have):], nil
}

// sharedLayers is how many of an image's layers, counted from the base, some
// image in have starts with.
func sharedLayers(layers []string, have [][]string) int {
	shared := 0
	for _, image := range have {
		n := 0
		for n < len(layers) && n < len(image) && layers[n] == image[n] {
			n++
		}
		shared = max(shared, n)
	}
	return shared
}

// incompleteImage turns the runtime's refusal of a reduced archive into the
// engine's error, and leaves every other error as it is.
func incompleteImage(err error) error {
	if errors.Is(err, docker.ErrImageIncomplete) {
		return ErrIncompleteImage
	}
	return err
}
