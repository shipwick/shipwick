package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Validate asks the agent whether it would accept a deploy.yaml document,
// without deploying it: before an image is built or a folder uploaded for a
// deployment that would then be refused. A nil error means it would; any
// other is the error Deploy would return for the document.
//
// supported is false when the agent predates the question (it answers
// ENDPOINT_NOT_FOUND). That is not an error: the caller goes on as it did
// before agents could be asked, and the deployment itself is the answer.
func (c *Client) Validate(ctx context.Context, name string, config []byte) (supported bool, err error) {
	_, err = call[api.Validation](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/validate", nil, config)
	if IsCode(err, api.CodeEndpointNotFound) {
		return false, nil
	}
	return true, err
}
