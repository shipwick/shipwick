package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/shipwick/shipwick/pkg/api"
)

// Certificates lists the certificates supplied to the server: what each
// chain says about itself, never a key.
func (c *Client) Certificates(ctx context.Context) ([]api.Certificate, error) {
	return get[[]api.Certificate](ctx, c, "/certificates", nil)
}

// SetCertificate stores a certificate chain and its key, both PEM, under
// hostname, replacing what was there. The agent checks them first.
func (c *Client) SetCertificate(ctx context.Context, hostname, certPEM, keyPEM string) (api.Certificate, error) {
	body, err := json.Marshal(api.SetCertificateRequest{Certificate: certPEM, Key: keyPEM})
	if err != nil {
		return api.Certificate{}, err
	}
	return call[api.Certificate](ctx, c, http.MethodPut, "/certificates/"+url.PathEscape(hostname), nil, body)
}

func (c *Client) DeleteCertificate(ctx context.Context, hostname string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/certificates/"+url.PathEscape(hostname), nil, nil)
	return err
}
