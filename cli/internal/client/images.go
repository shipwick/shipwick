package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// PushImage streams an image archive (the `docker save` format) to the server,
// which loads it for the application, and returns what arrived. size is the
// archive's length when it is known; 0 sends it chunked, since an image is
// saved and sent in one pass and its size is known only at the end.
func (c *Client) PushImage(ctx context.Context, name string, archive io.Reader, size int64) (api.LoadedImage, error) {
	u := c.base.JoinPath("/api/v1", "/applications/"+url.PathEscape(name)+"/images")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), archive)
	if err != nil {
		return api.LoadedImage{}, err
	}
	req.ContentLength = size
	req.Header.Set("User-Agent", "shipwick/"+version.Version)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-tar")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// No timeout: the upload takes as long as it takes.
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return api.LoadedImage{}, ctx.Err()
		}
		return api.LoadedImage{}, &UnreachableError{URL: c.base.String(), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return api.LoadedImage{}, decodeError(resp)
	}
	var envelope api.Response[api.LoadedImage]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return api.LoadedImage{}, fmt.Errorf("unexpected response from %s (is this a Shipwick agent?): %w", c.base, err)
	}
	return envelope.Data, nil
}
