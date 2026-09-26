// Package spec defines the application specification described by deploy.yaml,
// together with its parser and validator. It is shared by the CLI (which
// validates before sending) and the agent (which never trusts the client and
// validates again).
package spec

import (
	"encoding/json"
	"fmt"
	"time"
)

// Restart policies.
const (
	RestartAlways    = "always"
	RestartOnFailure = "on-failure"
	RestartNever     = "never"
)

// Deploy strategies.
const (
	// StrategyRolling replaces replicas one at a time; the application keeps
	// serving throughout.
	StrategyRolling = "rolling"
	// StrategyRecreate stops the running version before it starts the new
	// one, for applications whose two versions cannot run side by side —
	// anything with a volume. The application is down while the new version
	// starts; if it fails, the old one is started again.
	StrategyRecreate = "recreate"
)

// ReservedNames cannot be application names: an application is reachable
// under its name on the services network, and these belong to Shipwick's own
// containers there.
var ReservedNames = []string{"agent", "caddy", "dashboard", "localhost"}

// Defaults applied by Parse when a field is omitted.
const (
	DefaultReplicas       = 1
	DefaultHealthInterval = 10 * time.Second
	DefaultHealthTimeout  = 3 * time.Second
	DefaultHealthRetries  = 3
)

// App is a validated, normalized application specification. Values of this
// type are only produced by Parse, so holders may assume every field is valid.
//
// The JSON form is what the agent persists with each deployment record.
type App struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Port      int               `json:"port,omitempty"`
	Domain    string            `json:"domain,omitempty"`
	Replicas  int               `json:"replicas"`
	Env       map[string]string `json:"env,omitempty"`
	Health    *Health           `json:"health,omitempty"`
	Resources Resources         `json:"resources"`
	Volumes   []Volume          `json:"volumes,omitempty"`
	Restart   Restart           `json:"restart"`
	Deploy    Deploy            `json:"deploy"`
}

// Volume is a named Docker volume mounted into every replica. It belongs to
// the application, not to a deployment: the data outlives redeployments and
// rollbacks, and is kept when the application is deleted.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute path inside the container
}

// Health configures the HTTP health check run against every replica.
type Health struct {
	Path     string   `json:"path"`
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
	Retries  int      `json:"retries"`
}

// Resources are per-replica limits. A zero value means "unlimited".
type Resources struct {
	CPU         float64 `json:"cpu,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
}

// NanoCPUs converts the CPU limit to Docker's unit (1e-9 CPUs).
func (r Resources) NanoCPUs() int64 {
	return int64(r.CPU * 1e9)
}

type Restart struct {
	Policy string `json:"policy"`
}

type Deploy struct {
	Strategy string `json:"strategy"`
}

// Redacted returns a copy that is safe to expose through the API: environment
// variable names are kept, their values are masked.
func (a App) Redacted() App {
	if len(a.Env) == 0 {
		return a
	}
	env := make(map[string]string, len(a.Env))
	for k := range a.Env {
		env[k] = "********"
	}
	a.Env = env
	return a
}

// Duration is a time.Duration that marshals to JSON as a string such as "10s".
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"10s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
