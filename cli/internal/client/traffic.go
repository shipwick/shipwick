package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
)

// Traffic returns what the proxy saw of the application's requests over the
// last `since`: "1h", "24h" or "7d".
func (c *Client) Traffic(ctx context.Context, name, since string) (api.Traffic, error) {
	q := url.Values{"since": {since}}
	return get[api.Traffic](ctx, c, "/applications/"+url.PathEscape(name)+"/traffic", q)
}

// Requests returns the application's most recent requests, oldest first: at
// most `tail` of the 200 the agent remembers.
func (c *Client) Requests(ctx context.Context, name string, tail int) ([]api.Request, error) {
	q := url.Values{"tail": {strconv.Itoa(tail)}}
	return get[[]api.Request](ctx, c, "/applications/"+url.PathEscape(name)+"/requests", q)
}
