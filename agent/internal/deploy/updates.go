package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// The agent asks once a day whether a newer release exists, so that the
// dashboard and `shipwick server status` can say so without a connection of
// their own. A server that cannot ask — a firewall, no way out at all — must
// not notice: the question has a short timeout, a failure is a debug line,
// and the next attempt is a day later like any other. What was learned is
// kept in a file, so a restart, or a row of them, asks nothing.

// updateInterval is how often the agent asks, whatever the answer was.
const updateInterval = 24 * time.Hour

// UpdateOptions are the agent's question about newer releases.
type UpdateOptions struct {
	// Latest returns the tag of the latest release; nil: the agent does not
	// ask (SHIPWICK_UPDATE_CHECK=off).
	Latest func(ctx context.Context) (string, error)
	// StateFile keeps the last answer between runs; "": it is not kept.
	StateFile string
}

// updateState is what the agent knows, and the content of the state file.
type updateState struct {
	// AskedAt is the last attempt, CheckedAt the last one that was answered.
	AskedAt       time.Time `json:"asked_at"`
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

type updateCheck struct {
	mu    sync.Mutex
	state updateState
}

// StartUpdateCheck starts the loop that asks; it runs until the engine shuts
// down. Call it once.
func (e *Engine) StartUpdateCheck() {
	if e.opts.Updates.Latest == nil {
		return
	}
	e.loadUpdateState()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.bg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.bg.Done()
		e.checkForUpdate(e.baseCtx, time.Now())
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-e.baseCtx.Done():
				return
			case now := <-ticker.C:
				e.checkForUpdate(e.baseCtx, now)
			}
		}
	}()
}

// checkForUpdate asks when a day has passed since it last did, answered or
// not.
func (e *Engine) checkForUpdate(ctx context.Context, now time.Time) {
	u := &e.updates
	u.mu.Lock()
	// A clock that was set back must not silence the question for good.
	due := u.state.AskedAt.IsZero() || !now.Before(u.state.AskedAt.Add(updateInterval)) || now.Before(u.state.AskedAt)
	if due {
		u.state.AskedAt = now
	}
	u.mu.Unlock()
	if !due {
		return
	}

	tag, err := e.opts.Updates.Latest(ctx)
	u.mu.Lock()
	if err == nil {
		u.state.LatestVersion, u.state.CheckedAt = tag, now
	}
	state := u.state
	u.mu.Unlock()
	if err != nil {
		// Not a warning: a server without a way to GitHub is a normal one.
		e.log.Debug("could not ask for the latest release; asking again in a day", "error", err)
	}
	e.saveUpdateState(state)
}

func (e *Engine) loadUpdateState() {
	if e.opts.Updates.StateFile == "" {
		return
	}
	data, err := os.ReadFile(e.opts.Updates.StateFile)
	if err != nil {
		return
	}
	var state updateState
	// A file that cannot be read is replaced by the next answer.
	if json.Unmarshal(data, &state) != nil {
		return
	}
	if _, ok := version.Parse(state.LatestVersion); !ok {
		state.LatestVersion, state.CheckedAt = "", time.Time{}
	}
	e.updates.mu.Lock()
	e.updates.state = state
	e.updates.mu.Unlock()
}

func (e *Engine) saveUpdateState(state updateState) {
	file := e.opts.Updates.StateFile
	if file == "" {
		return
	}
	data, err := json.Marshal(state)
	if err == nil {
		// Renamed into place: a file cut short would be asked about again.
		staged := filepath.Join(filepath.Dir(file), "."+filepath.Base(file)+".new")
		if err = os.WriteFile(staged, data, 0o600); err == nil {
			err = os.Rename(staged, file)
		}
	}
	if err != nil {
		e.log.Debug("could not keep what was learned about the latest release", "error", err)
	}
}

// updateStatus is what GET /server says about newer releases.
func (e *Engine) updateStatus() *api.UpdateStatus {
	status := &api.UpdateStatus{Enabled: e.opts.Updates.Latest != nil}
	if !status.Enabled {
		return status
	}
	e.updates.mu.Lock()
	state := e.updates.state
	e.updates.mu.Unlock()
	if state.LatestVersion == "" {
		return status
	}
	status.LatestVersion = state.LatestVersion
	status.CheckedAt = &state.CheckedAt
	// A development build is not older than anything.
	_, released := version.Parse(version.Version)
	status.Available = released && version.Compare(version.Version, state.LatestVersion) < 0
	return status
}
