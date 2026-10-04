package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// forgetPath is the endpoint Shipwick's Caddy adds to the admin API
// (caddy/admin.go in this repository).
const forgetPath = "/shipwick/forget"

// errNotKept: the routes Caddy runs were not loaded by this agent with
// Shipwick's source of replicas, so what it remembers about a name is not
// the source's to forget.
var errNotKept = errors.New("the proxy does not find replicas through Shipwick's source")

// Forget makes the proxy drop what it last learned about who carries names,
// so that it asks Docker again before the next request to any of them. The
// agent calls it when a replica that carried them has given up its address.
//
// Nil means the proxy has confirmed it: it holds no address for these names
// that it learned before the call. Anything else — a proxy that cannot be
// reached, one that is not Shipwick's image of this version and answers 404,
// one that runs the routes with Caddy's own source — is an error, and the
// caller must assume the proxy still uses the address.
func (c *Caddy) Forget(ctx context.Context, names []string) error {
	if !c.kept.Load() {
		return errNotKept
	}
	if names == nil {
		names = []string{}
	}
	body, err := json.Marshal(struct {
		Names []string `json:"names"`
	}{names})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+forgetPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the proxy did not forget (HTTP %d)", resp.StatusCode)
	}
	return nil
}
