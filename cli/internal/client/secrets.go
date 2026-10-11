package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Secrets lists the secrets stored on the server: names, scopes and
// timestamps, never values.
func (c *Client) Secrets(ctx context.Context) ([]api.Secret, error) {
	return get[[]api.Secret](ctx, c, "/secrets", nil)
}

// SetSecret stores value under name, replacing what was there: for the
// application named, or for the whole server when application is "". The
// application travels in the body, which an agent that does not know it
// refuses; a parameter it would ignore, and store the secret for the server.
func (c *Client) SetSecret(ctx context.Context, name, value, application string) error {
	body, err := json.Marshal(api.SetSecretRequest{Value: value, Application: application})
	if err != nil {
		return err
	}
	_, err = call[struct{}](ctx, c, http.MethodPut, "/secrets/"+url.PathEscape(name), nil, body)
	return err
}

// DeleteSecret removes the application's secret, or the server's when
// application is "". An agent before 0.9 ignores the parameter and removes
// the server's: the caller sees in the listing whether the one it means is
// there before it asks.
func (c *Client) DeleteSecret(ctx context.Context, name, application string) error {
	var query url.Values
	if application != "" {
		query = url.Values{"application": {application}}
	}
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/secrets/"+url.PathEscape(name), query, nil)
	return err
}
