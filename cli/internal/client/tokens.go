package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Tokens lists the stored API tokens; the root token is not among them.
func (c *Client) Tokens(ctx context.Context) ([]api.Token, error) {
	return get[[]api.Token](ctx, c, "/tokens", nil)
}

// CreateToken creates a token. The value in the result is the only copy
// there will ever be.
func (c *Client) CreateToken(ctx context.Context, name string, role api.Role) (api.CreatedToken, error) {
	body, err := json.Marshal(api.CreateTokenRequest{Name: name, Role: role})
	if err != nil {
		return api.CreatedToken{}, err
	}
	return call[api.CreatedToken](ctx, c, http.MethodPost, "/tokens", nil, body)
}

func (c *Client) RevokeToken(ctx context.Context, name string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/tokens/"+url.PathEscape(name), nil, nil)
	return err
}
