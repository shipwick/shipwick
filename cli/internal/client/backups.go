package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shipwick/shipwick/pkg/api"
)

func backupsPath(name string) string {
	return "/applications/" + url.PathEscape(name) + "/backups"
}

func backupPath(name string, id int64) string {
	return backupsPath(name) + "/" + strconv.FormatInt(id, 10)
}

// Backups lists the backups the agent took of an application, newest first.
func (c *Client) Backups(ctx context.Context, name string, limit int) ([]api.BackupRun, error) {
	return get[[]api.BackupRun](ctx, c, backupsPath(name), url.Values{"limit": {strconv.Itoa(limit)}})
}

// BackupRun returns one backup, with the output of its last verification.
func (c *Client) BackupRun(ctx context.Context, name string, id int64) (api.BackupRunDetail, error) {
	return get[api.BackupRunDetail](ctx, c, backupPath(name, id), nil)
}

// StartBackup takes a backup now and returns it while it is still running.
func (c *Client) StartBackup(ctx context.Context, name string) (api.BackupRun, error) {
	return call[api.BackupRun](ctx, c, http.MethodPost, backupsPath(name), nil, nil)
}

// VerifyBackup starts the verification of a backup; id 0 means the latest
// successful one. The answer says which backup that is.
func (c *Client) VerifyBackup(ctx context.Context, name string, id int64) (api.BackupRun, error) {
	path := backupsPath(name) + "/latest/verify"
	if id != 0 {
		path = backupPath(name, id) + "/verify"
	}
	return call[api.BackupRun](ctx, c, http.MethodPost, path, nil, nil)
}

// RestoreBackup starts replacing the application's volumes with a backup's.
// The application must be stopped.
func (c *Client) RestoreBackup(ctx context.Context, name string, id int64) (api.BackupRun, error) {
	return call[api.BackupRun](ctx, c, http.MethodPost, backupPath(name, id)+"/restore", nil, nil)
}

// DeleteBackup removes a backup: its files and its record.
func (c *Client) DeleteBackup(ctx context.Context, name string, id int64) error {
	_, err := call[struct{}](ctx, c, http.MethodDelete, backupPath(name, id), nil, nil)
	return err
}

// BackupArchive streams one volume of a backup into w, as a tar archive, and
// returns its size. Nothing is buffered.
func (c *Client) BackupArchive(ctx context.Context, name string, id int64, volume string, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, c.stream, http.MethodGet, backupPath(name, id)+"/volumes/"+url.PathEscape(volume)+"/archive", nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, decodeError(resp)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, fmt.Errorf("the download was interrupted after %d bytes: %w", n, err)
	}
	return n, nil
}

// StateBackups lists the backups of the agent's own state, newest first.
func (c *Client) StateBackups(ctx context.Context) ([]api.BackupRun, error) {
	return get[[]api.BackupRun](ctx, c, "/server/backups", nil)
}

// StateBackup returns one backup of the agent's state.
func (c *Client) StateBackup(ctx context.Context, id int64) (api.BackupRun, error) {
	return get[api.BackupRun](ctx, c, "/server/backups/"+strconv.FormatInt(id, 10), nil)
}

// StartStateBackup backs up the agent's database and encryption key now.
func (c *Client) StartStateBackup(ctx context.Context) (api.BackupRun, error) {
	return call[api.BackupRun](ctx, c, http.MethodPost, "/server/backups", nil, nil)
}

// AdoptBackups has the agent record the backups its destinations hold and its
// database does not know: those of one application, or with an empty name
// everything. The agent lists a bucket for it, which takes what it takes.
func (c *Client) AdoptBackups(ctx context.Context, application string) (api.BackupAdoption, error) {
	body, err := json.Marshal(api.BackupAdoptRequest{Application: application})
	if err != nil {
		return api.BackupAdoption{}, err
	}
	return callVia[api.BackupAdoption](ctx, c, c.stream, http.MethodPost, "/server/backups/adopt", nil, body)
}
