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
	Aliases   []string          `json:"aliases,omitempty"`   // more hostnames served exactly like Domain
	Redirects []string          `json:"redirects,omitempty"` // hostnames redirected to Domain
	Replicas  int               `json:"replicas"`
	Env       map[string]string `json:"env,omitempty"`
	Health    *Health           `json:"health,omitempty"`
	Resources Resources         `json:"resources"`
	Volumes   []Volume          `json:"volumes,omitempty"`
	Publish   []Publish         `json:"publish,omitempty"`
	// Entrypoint, Command and User override what the image declares.
	Entrypoint []string `json:"entrypoint,omitempty"`
	Command    []string `json:"command,omitempty"`
	User       string   `json:"user,omitempty"`
	PreDeploy  *Hook    `json:"pre_deploy,omitempty"`
	Jobs       []Job    `json:"jobs,omitempty"`
	Logging    *Logging `json:"logging,omitempty"`
	// Build says where the image comes from when it is built on the developer's
	// machine and sent to the server; Image is then filled in by the CLI.
	Build *Build `json:"build,omitempty"`
	// Static is a folder of files served by the proxy itself, with no container;
	// such an application has no image, port or replicas.
	Static *Static `json:"static,omitempty"`
	// Path limits the application to one path prefix of its domain, so that
	// several applications can share a hostname.
	Path string `json:"path,omitempty"`
	// Proxy is what the proxy does for this application beyond routing to it.
	Proxy *Proxy `json:"proxy,omitempty"`
	// Backups says when the application's volumes are backed up unasked.
	Backups *Backups `json:"backups,omitempty"`
	Restart Restart  `json:"restart"`
	Deploy  Deploy   `json:"deploy"`
	// Init puts Docker's init process in front of the image's own, in every
	// container of the application: it passes signals on and reaps children,
	// which a process running as PID 1 does not do for itself.
	Init bool `json:"init,omitempty"`
	// Security is what the application's containers go without, beyond what
	// every container goes without.
	Security *Security `json:"security,omitempty"`
}

// Volume is a named Docker volume mounted into every replica. It belongs to
// the application, not to a deployment: the data outlives redeployments and
// rollbacks, and is kept when the application is deleted.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute path inside the container
}

// Health configures the health check run against every replica: an HTTP GET
// of Path, a TCP connection to port TCP, or Command run inside the replica
// and expected to exit 0. Exactly one of the three is set; Kind says which.
type Health struct {
	Path     string   `json:"path,omitempty"`
	TCP      int      `json:"tcp,omitempty"`
	Command  []string `json:"command,omitempty"`
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
	Retries  int      `json:"retries"`
	// StartPeriod is how long a replica may take before failed checks count,
	// on top of interval × retries; zero means no extra time.
	StartPeriod Duration `json:"start_period,omitempty"`
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
	// StopTimeout is how long a replica that is being replaced gets to finish
	// what it has in hand, once it has left the proxy; zero means the agent's
	// default.
	StopTimeout Duration `json:"stop_timeout,omitempty"`
}

// Health check kinds, derived from which of Health's fields is set.
const (
	HealthHTTP    = "http"
	HealthTCP     = "tcp"
	HealthCommand = "command"
)

// Publish maps a container port to a port on the server itself, for services
// that are not HTTP and so cannot go through the proxy: a database reached
// from outside, a game server. The application must use the recreate
// strategy with one replica: a host port cannot be shared.
type Publish struct {
	Port     int    `json:"port"`              // inside the container
	Host     int    `json:"host"`              // on the server
	Address  string `json:"address,omitempty"` // server address to bind; empty = all
	Protocol string `json:"protocol"`          // tcp or udp
}

// Hook is a command run in a one-off container from the application's image,
// with its environment, before its replicas are replaced.
type Hook struct {
	Command []string `json:"command"`
	Timeout Duration `json:"timeout"`
}

// Job is a command run on a schedule in a one-off container from the
// application's image, with its environment.
type Job struct {
	Name     string   `json:"name"`
	Schedule string   `json:"schedule"` // five-field cron expression
	Command  []string `json:"command"`
	Timeout  Duration `json:"timeout"`
}

// Logging selects the Docker logging driver for the replicas, for shipping
// their logs to a collector instead of the server's disk.
type Logging struct {
	Driver  string            `json:"driver"`
	Options map[string]string `json:"options,omitempty"`
}

// Redacted returns a copy that is safe to expose through the API: environment
// variable names are kept, their values are masked, and so are the passwords
// of the proxy block.
func (a App) Redacted() App {
	a.Proxy = a.Proxy.redacted()
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

// Kind reports which check Health describes.
func (h Health) Kind() string {
	switch {
	case len(h.Command) > 0:
		return HealthCommand
	case h.TCP != 0:
		return HealthTCP
	}
	return HealthHTTP
}

// Build describes an image built where the developer runs `shipwick deploy`
// and sent to the server: the build context and, relative to it, the
// Dockerfile.
type Build struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}

// Static is a folder served by the proxy as it is: a built frontend.
type Static struct {
	Dir string `json:"dir"` // relative to deploy.yaml, on the developer's machine
	// Fallback is the file, relative to Dir, answered for a path that names
	// no file: index.html for a single-page application. Empty means 404.
	Fallback string `json:"fallback,omitempty"`
}
