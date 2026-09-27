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

// UploadStatic sends the folder of a static application as a tar archive of
// the given size and returns what the agent kept of it: the digest that its
// deployment names, and what was in the archive.
func (c *Client) UploadStatic(ctx context.Context, name string, archive io.Reader, size int64) (api.StaticUpload, error) {
	u := c.base.JoinPath("/api/v1", "/applications/"+url.PathEscape(name)+"/static")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), archive)
	if err != nil {
		return api.StaticUpload{}, err
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
			return api.StaticUpload{}, ctx.Err()
		}
		return api.StaticUpload{}, &UnreachableError{URL: c.base.String(), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return api.StaticUpload{}, decodeError(resp)
	}
	var envelope api.Response[api.StaticUpload]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return api.StaticUpload{}, fmt.Errorf("unexpected response from %s (is this a Shipwick agent?): %w", c.base, err)
	}
	return envelope.Data, nil
}

// DeployStatic submits the deploy.yaml of a static application together with
// the digest of the folder UploadStatic sent, and returns the PENDING
// deployment.
func (c *Client) DeployStatic(ctx context.Context, name string, config []byte, digest string) (api.Deployment, error) {
	q := url.Values{"static": {digest}}
	return call[api.Deployment](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/deploy", q, config)
}
