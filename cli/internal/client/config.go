package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Config returns the deploy.yaml that describes what the application runs,
// written for a file: a literal ${NAME} outside the secret values is escaped,
// so that `shipwick deploy` reads the file as the same configuration. An
// agent that predates the question answers ENDPOINT_NOT_FOUND.
func (c *Client) Config(ctx context.Context, name string) (api.ApplicationConfig, error) {
	return call[api.ApplicationConfig](ctx, c, http.MethodGet, "/applications/"+url.PathEscape(name)+"/config", url.Values{"escape": {"true"}}, nil)
}
