package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
)

// Config returns the deploy.yaml that describes what the application runs,
// written for a file: a literal ${NAME} outside the secret values is escaped,
// so that `shipwick deploy` reads the file as the same configuration. An
// agent that predates the question answers ENDPOINT_NOT_FOUND.
func (c *Client) Config(ctx context.Context, name string) (api.ApplicationConfig, error) {
	return call[api.ApplicationConfig](ctx, c, http.MethodGet, "/applications/"+url.PathEscape(name)+"/config", url.Values{"escape": {"true"}}, nil)
}

// DeployWith is Deploy with a statement about the document: plain names the
// env values that stand in the file as they are sent — env.LOG_LEVEL — which
// the agent may then write again when it is asked for the document. An agent
// that predates the statement ignores it and masks them, as before.
func (c *Client) DeployWith(ctx context.Context, name string, config []byte, plain []string) (api.Deployment, error) {
	var q url.Values
	if len(plain) > 0 {
		q = url.Values{"plain": {strings.Join(plain, ",")}}
	}
	return call[api.Deployment](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/deploy", q, config)
}
