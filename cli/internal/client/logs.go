package client

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// LogSource narrows what the archive lists and what a search reads. Zero
// values do not narrow. Deployment and Run are ids, not the numbers of
// `shipwick status`.
type LogSource struct {
	Deployment int64
	Replica    int
	Run        int64
}

func (s LogSource) values() url.Values {
	q := url.Values{}
	if s.Deployment != 0 {
		q.Set("deployment", strconv.FormatInt(s.Deployment, 10))
	}
	if s.Replica != 0 {
		q.Set("replica", strconv.Itoa(s.Replica))
	}
	if s.Run != 0 {
		q.Set("run", strconv.FormatInt(s.Run, 10))
	}
	return q
}

// LogArchive lists what the agent keeps of the application's ended
// containers, newest first and without their lines. kind is
// api.LogKindReplica, api.LogKindRun or empty for both.
func (c *Client) LogArchive(ctx context.Context, name, kind string, source LogSource, limit int) ([]api.LogArchiveEntry, error) {
	q := source.values()
	q.Set("limit", strconv.Itoa(limit))
	if kind != "" {
		q.Set("kind", kind)
	}
	return get[[]api.LogArchiveEntry](ctx, c, "/applications/"+url.PathEscape(name)+"/logs/archive", q)
}

// ArchivedLogs returns one entry of the archive with its lines; tail keeps
// the last that many, zero all of them.
func (c *Client) ArchivedLogs(ctx context.Context, name string, id int64, tail int) (api.LogArchiveDetail, error) {
	q := url.Values{}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	}
	return get[api.LogArchiveDetail](ctx, c, "/applications/"+url.PathEscape(name)+"/logs/archive/"+strconv.FormatInt(id, 10), q)
}

// LogSearch is one page of a search: see SearchLogs.
type LogSearch struct {
	Text         string
	Since, Until time.Time
	Source       LogSource
	Limit        int
	// Cursor is the Next of the page before; empty for the first.
	Cursor string
}

// SearchLogs answers one page of a search through the application's output,
// archived and not. The page's Next, when it is not empty, is the Cursor of
// the page after it; a page may be short, or empty, and still have one.
func (c *Client) SearchLogs(ctx context.Context, name string, s LogSearch) (api.LogSearchResult, error) {
	q := s.Source.values()
	q.Set("limit", strconv.Itoa(s.Limit))
	if s.Text != "" {
		q.Set("q", s.Text)
	}
	if !s.Since.IsZero() {
		q.Set("since", s.Since.UTC().Format(time.RFC3339Nano))
	}
	if !s.Until.IsZero() {
		q.Set("until", s.Until.UTC().Format(time.RFC3339Nano))
	}
	if s.Cursor != "" {
		q.Set("cursor", s.Cursor)
	}
	return get[api.LogSearchResult](ctx, c, "/applications/"+url.PathEscape(name)+"/logs/search", q)
}
