// Package updates asks GitHub which release of Shipwick is the latest.
//
// The question is one request, and the request is all GitHub is told: no
// identifier of the server, nothing about what it runs, and a User-Agent
// that names the program without its version. What GitHub learns is that a
// Shipwick agent runs at the address the request came from.
package updates

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/outbound"
	"github.com/shipwick/shipwick/pkg/version"
)

// LatestURL answers with a redirect to the page of the latest release that
// is not a pre-release. Unlike GitHub's API it has no limit of requests per
// address, which servers behind one address would share.
const LatestURL = "https://github.com/shipwick/shipwick/releases/latest"

// UserAgent is the whole of what the request says about its sender.
const UserAgent = "shipwick-agent"

// timeout bounds one question. A server that cannot reach GitHub finds out
// in this time, once a day, and says nothing about it.
const timeout = 15 * time.Second

// Client asks. The zero value is not usable: see New.
type Client struct {
	url  string
	http *http.Client
}

// New returns a client that asks GitHub through the proxy the environment
// names and with the certificate authorities the operator added.
func New() *Client {
	return newClient(LatestURL, outbound.Transport())
}

func newClient(url string, transport http.RoundTripper) *Client {
	return &Client{url: url, http: &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// The answer is the redirect itself; the page it leads to is not wanted.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Latest returns the tag of the latest release, such as "v0.7.1".
func (c *Client) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || !strings.Contains(location, "/releases/tag/") {
		return "", fmt.Errorf("%s did not point at a release (HTTP %d)", c.url, resp.StatusCode)
	}
	tag := path.Base(location)
	if v, ok := version.Parse(tag); !ok || v.Prerelease != "" {
		return "", fmt.Errorf("%s points at %q, which is not a release version", c.url, tag)
	}
	return tag, nil
}
