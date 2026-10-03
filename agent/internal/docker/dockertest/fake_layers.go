package dockertest

import "context"

// AddImageLayers puts an image with these layers (diff IDs, base layer
// first) into the fake's store.
func (f *Fake) AddImageLayers(layers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.layers = append(f.layers, layers)
}

// ImageLayers returns the layers of every image added with AddImageLayers.
func (f *Fake) ImageLayers(context.Context) ([][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LayersErr != nil {
		return nil, f.LayersErr
	}
	return append([][]string(nil), f.layers...), nil
}
