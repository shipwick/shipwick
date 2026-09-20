// Package client is the HTTP client for the Shipwick agent API.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// requestTimeout covers the slowest regular call: stopping an application
// waits for every replica's graceful shutdown.
const requestTimeout = 90 * time.Second

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
	// stream has no overall timeout; log streams end through their context.
	stream *http.Client
}

// New creates a client for the agent at baseURL, e.g. "http://127.0.0.1:9000".
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid agent URL %q: expected something like http://127.0.0.1:9000", baseURL)
	}
	return &Client{
		base:   u,
		token:  token,
		http:   &http.Client{Timeout: requestTimeout},
		stream: &http.Client{},
	}, nil
}

// URL returns the agent's base URL.
func (c *Client) URL() string { return c.base.String() }

// SendsTokenInCleartext reports whether the token would cross a network
// unencrypted: plain HTTP to anything but the local machine.
func (c *Client) SendsTokenInCleartext() bool {
	if c.base.Scheme == "https" || c.token == "" {
		return false
	}
	host := c.base.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// APIError is an error response from the agent.
type APIError struct {
	Status  int // HTTP status
	Code    string
	Message string
	Details map[string]any
}

func (e *APIError) Error() string { return e.Message }

// IsCode reports whether err is an agent error with the given code.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// UnreachableError means no HTTP response was obtained at all.
type UnreachableError struct {
	URL string
	Err error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("cannot reach the Shipwick agent at %s\n  %v", e.URL, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

func (c *Client) Health(ctx context.Context) (api.Health, error) {
	return get[api.Health](ctx, c, "/health", nil)
}

func (c *Client) Server(ctx context.Context) (api.Server, error) {
	return get[api.Server](ctx, c, "/server", nil)
}

func (c *Client) Applications(ctx context.Context) ([]api.Application, error) {
	return get[[]api.Application](ctx, c, "/applications", nil)
}

func (c *Client) Application(ctx context.Context, name string) (api.ApplicationDetail, error) {
	return get[api.ApplicationDetail](ctx, c, "/applications/"+url.PathEscape(name), nil)
}

// Deploy submits a deploy.yaml document and returns the PENDING deployment.
func (c *Client) Deploy(ctx context.Context, name string, config []byte) (api.Deployment, error) {
	return call[api.Deployment](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/deploy", nil, config)
}

func (c *Client) Stop(ctx context.Context, name string) (api.ApplicationDetail, error) {
	return call[api.ApplicationDetail](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/stop", nil, nil)
}

func (c *Client) Start(ctx context.Context, name string) (api.ApplicationDetail, error) {
	return call[api.ApplicationDetail](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/start", nil, nil)
}

func (c *Client) Delete(ctx context.Context, name string) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, "/applications/"+url.PathEscape(name), nil, nil)
	return err
}

func (c *Client) Deployment(ctx context.Context, id int64) (api.DeploymentDetail, error) {
	return get[api.DeploymentDetail](ctx, c, "/deployments/"+strconv.FormatInt(id, 10), nil)
}

// Deployments lists deployments, newest first. An empty application lists all.
func (c *Client) Deployments(ctx context.Context, application string, limit int) ([]api.Deployment, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if application != "" {
		q.Set("application", application)
	}
	return get[[]api.Deployment](ctx, c, "/deployments", q)
}

func (c *Client) Logs(ctx context.Context, name string, tail int) ([]api.LogLine, error) {
	q := url.Values{"tail": {strconv.Itoa(tail)}}
	return get[[]api.LogLine](ctx, c, "/applications/"+url.PathEscape(name)+"/logs", q)
}

// FollowLogs streams log lines to fn until ctx is cancelled or the agent ends
// the stream (nil error in both cases).
func (c *Client) FollowLogs(ctx context.Context, name string, tail int, fn func(api.LogLine)) error {
	q := url.Values{"follow": {"true"}, "tail": {strconv.Itoa(tail)}}
	resp, err := c.send(ctx, c.stream, http.MethodGet, "/applications/"+url.PathEscape(name)+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeError(resp)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		var line api.LogLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return fmt.Errorf("malformed log stream: %w", err)
		}
		fn(line)
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("log stream interrupted: %w", err)
	}
	return nil
}

func get[T any](ctx context.Context, c *Client, path string, query url.Values) (T, error) {
	return call[T](ctx, c, http.MethodGet, path, query, nil)
}

func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, body []byte) (T, error) {
	var zero T
	resp, err := c.send(ctx, c.http, method, path, query, body)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return zero, decodeError(resp)
	}
	if resp.StatusCode == http.StatusNoContent {
		return zero, nil
	}
	var envelope api.Response[T]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return zero, fmt.Errorf("unexpected response from %s (is this a Shipwick agent?): %w", c.base, err)
	}
	return envelope.Data, nil
}

func (c *Client) send(ctx context.Context, httpClient *http.Client, method, path string, query url.Values, body []byte) (*http.Response, error) {
	u := c.base.JoinPath("/api/v1", path)
	u.RawQuery = query.Encode()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "deployctl/"+version.Version)
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		// deploy.yaml documents, or small JSON requests.
		contentType := "application/yaml"
		if json.Valid(body) {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// url.Error repeats the method and URL; the cause alone reads better.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, &UnreachableError{URL: c.base.String(), Err: err}
	}
	return resp, nil
}

func decodeError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var envelope api.ErrorResponse
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Error.Code == "" {
		return &APIError{
			Status:  resp.StatusCode,
			Code:    api.CodeInternal,
			Message: fmt.Sprintf("unexpected HTTP %d from the agent (is a proxy in between?)", resp.StatusCode),
		}
	}
	e := envelope.Error
	return &APIError{Status: resp.StatusCode, Code: e.Code, Message: e.Message, Details: e.Details}
}

// ValidationError rebuilds the field-by-field report from an INVALID_CONFIG
// response, so agent-side rejections render exactly like local ones.
func ValidationError(err error) (*spec.ValidationError, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != api.CodeInvalidConfig {
		return nil, false
	}
	raw, err := json.Marshal(apiErr.Details["fields"])
	if err != nil {
		return nil, false
	}
	var fields []spec.FieldError
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return nil, false
	}
	return &spec.ValidationError{Fields: fields}, true
}

// Events returns the application's own event feed, newest first: crashes,
// restarts, health changes, stops and starts.
func (c *Client) Events(ctx context.Context, name string, limit int) ([]api.Event, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	return get[[]api.Event](ctx, c, "/applications/"+url.PathEscape(name)+"/events", q)
}

// Redeploy deploys the active configuration again; image, when not empty,
// replaces the running image.
func (c *Client) Redeploy(ctx context.Context, name, image string) (api.Deployment, error) {
	body, err := json.Marshal(api.RedeployRequest{Image: image})
	if err != nil {
		return api.Deployment{}, err
	}
	return call[api.Deployment](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/redeploy", nil, body)
}

// Rollback deploys the configuration stored with an earlier successful
// deployment; deploymentID 0 lets the agent pick the most recent one.
func (c *Client) Rollback(ctx context.Context, name string, deploymentID int64) (api.Deployment, error) {
	body, err := json.Marshal(api.RollbackRequest{DeploymentID: deploymentID})
	if err != nil {
		return api.Deployment{}, err
	}
	return call[api.Deployment](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/rollback", nil, body)
}

// Metrics returns a point-in-time sample of the application's resource usage.
func (c *Client) Metrics(ctx context.Context, name string) (api.Metrics, error) {
	return get[api.Metrics](ctx, c, "/applications/"+url.PathEscape(name)+"/metrics", nil)
}
