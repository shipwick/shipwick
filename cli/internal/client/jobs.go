package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
)

// Jobs lists the scheduled jobs of an application with their last and next run.
func (c *Client) Jobs(ctx context.Context, name string) ([]api.Job, error) {
	return get[[]api.Job](ctx, c, "/applications/"+url.PathEscape(name)+"/jobs", nil)
}

// Runs lists an application's runs, newest first and without output; job
// narrows them to one job ("pre-deploy" and "run" included).
func (c *Client) Runs(ctx context.Context, name, job string, limit int) ([]api.Run, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if job != "" {
		q.Set("job", job)
	}
	return get[[]api.Run](ctx, c, "/applications/"+url.PathEscape(name)+"/runs", q)
}

// Run returns one run with its output.
func (c *Client) Run(ctx context.Context, name string, id int64) (api.RunDetail, error) {
	return get[api.RunDetail](ctx, c, "/applications/"+url.PathEscape(name)+"/runs/"+strconv.FormatInt(id, 10), nil)
}

// RunJob starts a scheduled job now and returns the run, which is still going.
func (c *Client) RunJob(ctx context.Context, name, job string) (api.RunDetail, error) {
	return call[api.RunDetail](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/jobs/"+url.PathEscape(job)+"/run", nil, nil)
}

// RunCommand starts a one-off command in a container of the application and
// returns the run, which is still going.
func (c *Client) RunCommand(ctx context.Context, name string, command []string) (api.RunDetail, error) {
	body, err := json.Marshal(api.RunRequest{Command: command})
	if err != nil {
		return api.RunDetail{}, err
	}
	return call[api.RunDetail](ctx, c, http.MethodPost, "/applications/"+url.PathEscape(name)+"/run", nil, body)
}
