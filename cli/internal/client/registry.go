package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Registries lists the registries the agent holds a credential for:
// usernames and timestamps, never passwords.
func (c *Client) Registries(ctx context.Context) ([]api.Registry, error) {
	return get[[]api.Registry](ctx, c, "/registries", nil)
}

// SetRegistry stores the credential for a registry, replacing what was
// there. The agent checks it against the registry first.
func (c *Client) SetRegistry(ctx context.Context, registry, username, password string) error {
	body, err := json.Marshal(api.SetRegistryRequest{Username: username, Password: password})
	if err != nil {
		return err
	}
	_, err = call[struct{}](ctx, c, http.MethodPut, "/registries/"+url.PathEscape(registry), nil, body)
	return err
}

func (c *Client) DeleteRegistry(ctx context.Context, registry string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/registries/"+url.PathEscape(registry), nil, nil)
	return err
}

// RotateKey has the agent replace its encryption key. The result carries the
// new key only when the agent cannot put it where it reads it from.
func (c *Client) RotateKey(ctx context.Context) (api.KeyRotation, error) {
	return call[api.KeyRotation](ctx, c, http.MethodPost, "/server/rotate-key", nil, nil)
}
