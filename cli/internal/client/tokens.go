package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
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
	// ActorKind is api.ActorToken or api.ActorUser.
	ActorKind string
	// Actions and Outcomes match when any of theirs does.
	Actions  []string
	Outcomes []string
	Since    time.Time
	Before   int64
	Limit    int
}

// Narrowed reports whether the query uses a filter that agents older than
// 0.7 do not know, and ignore.
func (q AuditQuery) Narrowed() bool {
	return q.ActorKind != "" || len(q.Actions) > 0 || len(q.Outcomes) > 0
}

// values is the query as the agent reads it; limit and before are the
// caller's to add.
func (query AuditQuery) values() url.Values {
	q := url.Values{}
	if query.ActorKind != "" {
		q.Set("actor_kind", query.ActorKind)
	}
	if len(query.Actions) > 0 {
		q.Set("action", strings.Join(query.Actions, ","))
	}
	if len(query.Outcomes) > 0 {
		q.Set("outcome", strings.Join(query.Outcomes, ","))
	}
	if query.Application != "" {
		q.Set("application", query.Application)
	}
	if query.Actor != "" {
		q.Set("actor", query.Actor)
	}
	if !query.Since.IsZero() {
		q.Set("since", query.Since.UTC().Format(time.RFC3339))
	}
	return q
}
