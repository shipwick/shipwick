package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// exportErrorTrailer is where the agent says why an export stopped after
// its first byte.
const exportErrorTrailer = "X-Shipwick-Export-Error"

// ErrExportInterrupted means an export ended before it was whole.
var ErrExportInterrupted = errors.New("the export was interrupted")

// Export streams an export of the server, encrypted with passphrase, into w
// and returns its size. Nothing is buffered. What arrives is whole only if
// the error is nil; the caller checks it once more by reading it.
func (c *Client) Export(ctx context.Context, passphrase string, applications []string, w io.Writer) (int64, error) {
	body, err := json.Marshal(api.ExportRequest{Passphrase: passphrase, Applications: applications})
	if err != nil {
		return 0, err
	}
	resp, err := c.send(ctx, c.stream, http.MethodPost, "/export", nil, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, decodeError(resp)
	}
	n, err := io.Copy(w, resp.Body)
	switch {
	case err != nil && ctx.Err() != nil:
		return n, ctx.Err()
	case err != nil:
		return n, fmt.Errorf("%w after %d bytes: %v", ErrExportInterrupted, n, err)
	}
	if reason := resp.Trailer.Get(exportErrorTrailer); reason != "" {
		return n, fmt.Errorf("%w by the server: %s", ErrExportInterrupted, reason)
	}
	return n, nil
}

// Exports lists the exports the agent wrote to where its backups go.
func (c *Client) Exports(ctx context.Context, limit int) ([]api.BackupRun, error) {
	return get[[]api.BackupRun](ctx, c, "/exports", url.Values{"limit": {strconv.Itoa(limit)}})
}

func (c *Client) ExportRun(ctx context.Context, id int64) (api.BackupRun, error) {
	return get[api.BackupRun](ctx, c, "/exports/"+strconv.FormatInt(id, 10), nil)
}

// StartExport writes an export to where backups go and returns it while it
// is still being written.
func (c *Client) StartExport(ctx context.Context) (api.BackupRun, error) {
	return call[api.BackupRun](ctx, c, http.MethodPost, "/exports", nil, nil)
}

// Import streams an export to the server, which imports it as it arrives,
// and returns what became of it. size is the file's length.
func (c *Client) Import(ctx context.Context, archive io.Reader, size int64, passphrase string, stopped, overwrite bool) (api.Import, error) {
	u := c.base.JoinPath("/api/v1", "/import")
	u.RawQuery = url.Values{"stopped": {strconv.FormatBool(stopped)}, "overwrite": {strconv.FormatBool(overwrite)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), archive)
	if err != nil {
		return api.Import{}, err
	}
	req.ContentLength = size
	req.Header.Set("User-Agent", "shipwick/"+version.Version)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/octet-stream")
	// Base64: a header holds ASCII, a passphrase whatever was typed.
	req.Header.Set(api.PassphraseHeader, base64.StdEncoding.EncodeToString([]byte(passphrase)))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// No timeout: the server reads the upload as it deploys what is in it.
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return api.Import{}, ctx.Err()
		}
		return api.Import{}, &UnreachableError{URL: c.base.String(), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return api.Import{}, decodeError(resp)
	}
	var envelope api.Response[api.Import]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return api.Import{}, fmt.Errorf("unexpected response from %s (is this a Shipwick agent?): %w", c.base, err)
	}
	return envelope.Data, nil
}

// ImportStatus returns the import the server is running, or ran last.
func (c *Client) ImportStatus(ctx context.Context) (api.Import, error) {
	return get[api.Import](ctx, c, "/import", nil)
}

// Standby reports what the server holds for a promotion.
func (c *Client) Standby(ctx context.Context) (api.Standby, error) {
	return get[api.Standby](ctx, c, "/standby", nil)
}

// StandbyPull starts an import of the newest export in the bucket.
func (c *Client) StandbyPull(ctx context.Context) (api.Import, error) {
	return call[api.Import](ctx, c, http.MethodPost, "/standby/pull", nil, nil)
}

// StartPromotion begins starting every application that was imported stopped
// and returns the promotion as it begins; Promotion follows it. With nothing
// to start, the promotion returned has completed.
func (c *Client) StartPromotion(ctx context.Context) (api.Promotion, error) {
	return call[api.Promotion](ctx, c, http.MethodPost, "/standby/promote", url.Values{"wait": {"false"}}, nil)
}

// Promotion returns the promotion the server is running, or ran last. An
// agent that keeps no record of one answers ENDPOINT_NOT_FOUND.
func (c *Client) Promotion(ctx context.Context) (api.Promotion, error) {
	return get[api.Promotion](ctx, c, "/standby/promotion", nil)
}

// Promote is the promotion as an agent without a record of it runs one: the
// request is held until every application is ready or has spent its startup
// budget, and the answer is lost with the connection.
func (c *Client) Promote(ctx context.Context) (api.Promotion, error) {
	return callVia[api.Promotion](ctx, c, c.stream, http.MethodPost, "/standby/promote", nil, nil)
}
