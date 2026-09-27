package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// Volumes lists the volumes of the application's active deployment.
func (c *Client) Volumes(ctx context.Context, name string) ([]api.Volume, error) {
	return get[[]api.Volume](ctx, c, "/applications/"+url.PathEscape(name)+"/volumes", nil)
}

// Backup streams a tar archive of the volume into w and returns its size.
// Nothing is buffered: a backup can be larger than memory.
func (c *Client) Backup(ctx context.Context, name, volume string, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, c.stream, http.MethodGet, archivePath(name, volume), nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, decodeError(resp)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, fmt.Errorf("the backup was interrupted after %d bytes: %w", n, err)
	}
	return n, nil
}

// Restore uploads a tar archive of the given size to replace the volume's
// contents. The application must be stopped.
func (c *Client) Restore(ctx context.Context, name, volume string, archive io.Reader, size int64) error {
	u := c.base.JoinPath("/api/v1", archivePath(name, volume))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), archive)
	if err != nil {
		return err
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
			return ctx.Err()
		}
		return &UnreachableError{URL: c.base.String(), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	return nil
}

func archivePath(name, volume string) string {
	return "/applications/" + url.PathEscape(name) + "/volumes/" + url.PathEscape(volume) + "/archive"
}
