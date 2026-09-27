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

	// The response is a stream of JSON messages, the same format a pull
	// produces: progress in "stream", a failure in "error".
	var msg struct {
		Stream string `json:"stream"`
		Error  string `json:"error"`
	}
	var refs []string
	dec := json.NewDecoder(res)
	for {
		msg.Stream, msg.Error = "", ""
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("load image: read the daemon's answer: %w", err)
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("load image: %s", msg.Error)
		}
		if ref, ok := strings.CutPrefix(strings.TrimSpace(msg.Stream), "Loaded image: "); ok {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
