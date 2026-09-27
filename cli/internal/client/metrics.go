package client

import (
	"context"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// MetricsHistory returns the sampled resource usage of the application over
// the last `since` — "1h", "24h" or "7d" — one series per replica.
func (c *Client) MetricsHistory(ctx context.Context, name, since string) (api.MetricsHistory, error) {
	q := url.Values{"since": {since}}
	return get[api.MetricsHistory](ctx, c, "/applications/"+url.PathEscape(name)+"/metrics/history", q)
}
