package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
)

// AccessRules lists the rules that say who may sign in, and as what.
func (c *Client) AccessRules(ctx context.Context) ([]api.AccessRule, error) {
	return get[[]api.AccessRule](ctx, c, "/access/rules", nil)
}

// GrantAccess stores a rule; one for the same kind and subject is replaced.
func (c *Client) GrantAccess(ctx context.Context, req api.GrantAccessRequest) (api.AccessRule, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return api.AccessRule{}, err
	}
	return call[api.AccessRule](ctx, c, http.MethodPost, "/access/rules", nil, body)
}

func (c *Client) RevokeAccess(ctx context.Context, id int64) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/access/rules/"+strconv.FormatInt(id, 10), nil, nil)
	return err
}

// Sessions lists the people signed in right now.
func (c *Client) Sessions(ctx context.Context) ([]api.Session, error) {
	return get[[]api.Session](ctx, c, "/access/sessions", nil)
}

// EndSessions signs a person out, and returns how many sessions that ended.
func (c *Client) EndSessions(ctx context.Context, email string) (int, error) {
	out, err := call[api.SignedOut](ctx, c, http.MethodDelete, "/access/sessions/"+url.PathEscape(email), nil, nil)
	return out.Sessions, err
}
