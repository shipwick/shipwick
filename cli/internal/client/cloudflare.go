package client

import (
	"context"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
)

// VerifyCloudflare has the agent check its Cloudflare token now and reports
// what it found. The agent refuses a check within a minute of the last with
// CLOUDFLARE_CHECKED_RECENTLY — GET /server carries that one — and an agent
// without a token with CLOUDFLARE_NOT_CONFIGURED; an agent older than the
// check answers ENDPOINT_NOT_FOUND.
func (c *Client) VerifyCloudflare(ctx context.Context) (api.CloudflareStatus, error) {
	return call[api.CloudflareStatus](ctx, c, http.MethodPost, "/server/cloudflare/verify", nil, nil)
}
