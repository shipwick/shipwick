package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// MissingLayers asks the server which layers of an image — its diff IDs, base
// layer first — its Docker does not have. An agent older than the question
// answers ENDPOINT_NOT_FOUND.
func (c *Client) MissingLayers(ctx context.Context, name string, layers []string) ([]string, error) {
	body, err := json.Marshal(api.MissingLayersRequest{Layers: layers})
	if err != nil {
		return nil, err
	}
	answer, err := call[api.MissingLayers](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/images/missing", nil, body)
	return answer.Missing, err
}
