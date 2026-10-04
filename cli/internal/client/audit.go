package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
)

// ErrAuditFiltersUnknown means the agent is older than the filters by
// action, outcome and kind of actor: it would have ignored them and answered
// with entries nobody asked for.
var ErrAuditFiltersUnknown = errors.New("the agent does not know these audit filters")

// Audit returns a page of the audit trail, newest first. More is nil when
// the agent does not say whether older entries match.
func (c *Client) Audit(ctx context.Context, query AuditQuery) (api.AuditPage, error) {
	q := query.values()
	q.Set("limit", strconv.Itoa(query.Limit))
	if query.Before > 0 {
		q.Set("before", strconv.FormatInt(query.Before, 10))
	}
	resp, err := c.send(ctx, c.http, http.MethodGet, "/audit", q, nil)
	if err != nil {
		return api.AuditPage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return api.AuditPage{}, decodeError(resp)
	}
	var page api.AuditPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return api.AuditPage{}, fmt.Errorf("unexpected response from %s (is this a Shipwick agent?): %w", c.base, err)
	}
	// An agent that says nothing about more is one from before the filters.
	if page.More == nil && query.Narrowed() {
		return api.AuditPage{}, ErrAuditFiltersUnknown
	}
	return page, nil
}

// ErrAuditExportInterrupted means the trail stopped arriving before its end.
var ErrAuditExportInterrupted = errors.New("the audit export was interrupted")

// ExportAudit streams every entry that matches into w, as CSV or as one
// JSON object per line, and returns how many bytes that was. Limit and
// Before are not looked at: an export is the whole of what matches.
func (c *Client) ExportAudit(ctx context.Context, query AuditQuery, format string, w io.Writer) (int64, error) {
	q := query.values()
	q.Set("format", format)
	resp, err := c.send(ctx, c.stream, http.MethodGet, "/audit/export", q, nil)
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
		return n, fmt.Errorf("%w after %d bytes: %v", ErrAuditExportInterrupted, n, err)
	}
	if reason := resp.Trailer.Get(exportErrorTrailer); reason != "" {
		return n, fmt.Errorf("%w by the server: %s", ErrAuditExportInterrupted, reason)
	}
	return n, nil
}

// UpdateToken changes the applications a token is limited to, its end, or
// both, and returns the token as it is now.
func (c *Client) UpdateToken(ctx context.Context, name string, req api.UpdateTokenRequest) (api.Token, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return api.Token{}, err
	}
	return call[api.Token](ctx, c, http.MethodPut, "/tokens/"+url.PathEscape(name), nil, body)
}
