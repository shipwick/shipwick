package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Secrets lists the secrets stored on the server: names and timestamps, never
// values.
func (c *Client) Secrets(ctx context.Context) ([]api.Secret, error) {
	return get[[]api.Secret](ctx, c, "/secrets", nil)
}

// SetSecret stores value under name, replacing what was there.
func (c *Client) SetSecret(ctx context.Context, name, value string) error {
	body, err := json.Marshal(api.SetSecretRequest{Value: value})
	if err != nil {
		return err
	}
	_, err = call[struct{}](ctx, c, http.MethodPut, "/secrets/"+url.PathEscape(name), nil, body)
	return err
}

func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/secrets/"+url.PathEscape(name), nil, nil)
	return err
}
