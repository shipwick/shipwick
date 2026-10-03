package dockertest

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// SaveImage writes a local image out in the form the fake's LoadImage reads:
// the reference itself. SaveErr, when set, is what the daemon answers.
func (f *Fake) SaveImage(_ context.Context, image string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SaveErr != nil {
		return nil, f.SaveErr
	}
	if !f.local[image] {
		return nil, fmt.Errorf("save image %s: no such image", image)
	}
	return io.NopCloser(strings.NewReader(image + "\n")), nil
}
