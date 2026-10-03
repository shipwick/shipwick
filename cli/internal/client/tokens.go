package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// Tokens lists the stored API tokens; the root token is not among them.
func (c *Client) Tokens(ctx context.Context) ([]api.Token, error) {
	return get[[]api.Token](ctx, c, "/tokens", nil)
}

// CreateToken creates a token. The value in the result is the only copy
// there will ever be.
func (c *Client) CreateToken(ctx context.Context, req api.CreateTokenRequest) (api.CreatedToken, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return api.CreatedToken{}, err
	}
	return call[api.CreatedToken](ctx, c, http.MethodPost, "/tokens", nil, body)
}

func (c *Client) RevokeToken(ctx context.Context, name string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/tokens/"+url.PathEscape(name), nil, nil)
	return err
}

// AuditQuery narrows Audit. Zero values do not filter.
type AuditQuery struct {
	Application string
	Actor       string
	Since       time.Time
	Before      int64
	Limit       int
}

// Audit returns the audit trail, newest first.
func (c *Client) Audit(ctx context.Context, query AuditQuery) ([]api.AuditEntry, error) {
	q := url.Values{"limit": {strconv.Itoa(query.Limit)}}
	if query.Application != "" {
		q.Set("application", query.Application)
	}
	if query.Actor != "" {
		q.Set("actor", query.Actor)
	}
	if !query.Since.IsZero() {
		q.Set("since", query.Since.UTC().Format(time.RFC3339))
	}
	if query.Before > 0 {
		q.Set("before", strconv.FormatInt(query.Before, 10))
	}
	return get[[]api.AuditEntry](ctx, c, "/audit", q)
}
