// Package api defines the JSON wire types of the agent HTTP API. They are
// shared by the agent, which produces them, and by the CLI, which consumes them.
package api

import (
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

// Response is the envelope of every successful response.
type Response[T any] struct {
	Data T `json:"data"`
}

// ErrorResponse is the envelope of every failed response.
type ErrorResponse struct {
	Error Error `json:"error"`
}

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// Error codes.
const (
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeInvalidRequest       = "INVALID_REQUEST"
	CodeInvalidConfig        = "INVALID_CONFIG"
	CodeNotFound             = "NOT_FOUND"
	CodeEndpointNotFound     = "ENDPOINT_NOT_FOUND" // this agent has no such operation: an older version, or a typo
	CodeDeploymentInProgress = "DEPLOYMENT_IN_PROGRESS"
	CodeNotDeployed          = "NOT_DEPLOYED"
	CodeNoRollbackTarget     = "NO_ROLLBACK_TARGET"
	CodeRuntimeUnavailable   = "RUNTIME_UNAVAILABLE"
	CodeInternal             = "INTERNAL_ERROR"
)

// DeploymentStatus is a state of the deployment state machine.
type DeploymentStatus string

const (
	StatusPending        DeploymentStatus = "PENDING"
	StatusBuilding       DeploymentStatus = "BUILDING"
	StatusStarting       DeploymentStatus = "STARTING"
	StatusHealthChecking DeploymentStatus = "HEALTH_CHECKING"
	StatusHealthy        DeploymentStatus = "HEALTHY"
	StatusActive         DeploymentStatus = "ACTIVE"
	StatusSuperseded     DeploymentStatus = "SUPERSEDED"
	StatusFailed         DeploymentStatus = "FAILED"
	StatusRollback       DeploymentStatus = "ROLLBACK"
	StatusRestoring      DeploymentStatus = "RESTORING"
	StatusRolledBack     DeploymentStatus = "ROLLED_BACK"
)

// ApplicationStatus summarizes what an application is doing right now. It is
// derived from the deployment history and the live container states.
type ApplicationStatus string

const (
	AppHealthy   ApplicationStatus = "HEALTHY"    // every desired replica is running and passes its health check
	AppDegraded  ApplicationStatus = "DEGRADED"   // some, but not all, replicas are healthy
	AppDown      ApplicationStatus = "DOWN"       // no replica is healthy
	AppCrashLoop ApplicationStatus = "CRASH_LOOP" // a replica keeps dying; restarts are being rate-limited
	AppStopped   ApplicationStatus = "STOPPED"    // stopped on request
	AppDeploying ApplicationStatus = "DEPLOYING"  // first deployment still in flight
	AppFailed    ApplicationStatus = "FAILED"     // never had a successful deployment
)

// Desired states of an application.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

type Application struct {
	Name         string            `json:"name"`
	Status       ApplicationStatus `json:"status"`
	DesiredState string            `json:"desired_state"`
	// Image, Version and Domain describe the active deployment and are empty
	// until the first deployment succeeds.
	Image     string       `json:"image"`
	Version   string       `json:"version"`
	Domain    string       `json:"domain"`
	Replicas  ReplicaCount `json:"replicas"`
	Deploying bool         `json:"deploying"`
	// InFlightDeploymentID is the deployment to follow while Deploying is true.
	InFlightDeploymentID *int64    `json:"in_flight_deployment_id"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type ReplicaCount struct {
	Desired int `json:"desired"`
	Running int `json:"running"`
	// Healthy counts running replicas that are not failing their health
	// check. Without a health check configured it equals Running.
	Healthy int `json:"healthy"`
}

// Replica health, as observed by the supervisor.
const (
	HealthNone      = ""          // the application defines no health check
	HealthUnknown   = "unknown"   // not probed yet (e.g. the agent just started); assumed fine
	HealthStarting  = "starting"  // (re)started, still within its startup budget
	HealthHealthy   = "healthy"   //
	HealthUnhealthy = "unhealthy" // failed `retries` consecutive checks, or never came up
)

type ApplicationDetail struct {
	Application
	// Spec is the active configuration with env values masked.
	Spec             *spec.App   `json:"spec"`
	ActiveDeployment *Deployment `json:"active_deployment"`
	Containers       []Container `json:"containers"`
}

type Container struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	DeploymentID int64      `json:"deployment_id"`
	Replica      int        `json:"replica"`
	Image        string     `json:"image"`
	State        string     `json:"state"` // Docker state: created, running, exited, ...
	ExitCode     int        `json:"exit_code"`
	OOMKilled    bool       `json:"oom_killed"`
	IP           string     `json:"ip"`
	StartedAt    *time.Time `json:"started_at"`
	Health       string     `json:"health"`     // see the Health* constants
	Restarts     int        `json:"restarts"`   // restarts performed by the supervisor
	CrashLoop    bool       `json:"crash_loop"` // restarts are being rate-limited
}

type Deployment struct {
	ID          int64            `json:"id"`
	Application string           `json:"application"`
	Sequence    int              `json:"sequence"` // per-application counter: #1, #2, ...
	Version     string           `json:"version"`
	Image       string           `json:"image"`
	Status      DeploymentStatus `json:"status"`
	Error       string           `json:"error"`
	StartedAt   time.Time        `json:"started_at"`
	CompletedAt *time.Time       `json:"completed_at"`
	// Kind says how the deployment came to be; for a redeploy or rollback,
	// SourceDeploymentID is the deployment whose configuration it re-used.
	Kind               string `json:"kind"`
	SourceDeploymentID *int64 `json:"source_deployment_id"`
}

type DeploymentDetail struct {
	Deployment
	Spec   spec.App `json:"spec"`
	Events []Event  `json:"events"`
}

// Event levels.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event types.
const (
	// EventStep marks a completed deployment step; the CLI renders these as
	// its "✓ Pulling image" progress lines.
	EventStep = "step"
	// EventState marks a state machine transition.
	EventState = "state"
	// EventLog carries diagnostic output, e.g. the last log lines of a
	// replica that crashed during deployment.
	EventLog = "log"
	// EventApp marks application-level actions: stop, start.
	EventApp = "app"
	// EventSupervisor marks what the supervisor observed and did: crashes,
	// restarts, failed health checks, crash loops, recoveries.
	EventSupervisor = "supervisor"
)

type Event struct {
	ID           int64     `json:"id"`
	DeploymentID *int64    `json:"deployment_id"`
	Level        string    `json:"level"`
	Type         string    `json:"type"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
}

type LogLine struct {
	Replica   int       `json:"replica"`
	Container string    `json:"container"`
	Stream    string    `json:"stream"` // stdout | stderr
	Time      time.Time `json:"time"`
	Message   string    `json:"message"`
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type Server struct {
	AgentVersion  string      `json:"agent_version"`
	Hostname      string      `json:"hostname"`
	OS            string      `json:"os"`
	Kernel        string      `json:"kernel"`
	Architecture  string      `json:"architecture"`
	DockerVersion string      `json:"docker_version"`
	CPUs          int         `json:"cpus"`
	MemoryBytes   int64       `json:"memory_bytes"`
	Applications  int         `json:"applications"`
	Containers    int         `json:"containers"` // running Shipwick-managed containers
	Proxy         ProxyStatus `json:"proxy"`
}

// ProxyStatus describes the reverse proxy in front of the applications.
type ProxyStatus struct {
	Enabled   bool   `json:"enabled"`   // false: no proxy configured, domains are not served
	Reachable bool   `json:"reachable"` // the last configuration sync succeeded
	Error     string `json:"error"`     // why it did not
	Routes    int    `json:"routes"`    // hostnames being served
}

// Deployment kinds.
const (
	KindDeploy   = "deploy"   // a deploy.yaml was submitted
	KindRedeploy = "redeploy" // the active configuration again, possibly with another image
	KindRollback = "rollback" // the configuration of an earlier successful deployment
)

// RedeployRequest is the optional body of POST /applications/:name/redeploy.
type RedeployRequest struct {
	Image string `json:"image"`
}

// RollbackRequest is the optional body of POST /applications/:name/rollback.
type RollbackRequest struct {
	DeploymentID int64 `json:"deployment_id"`
}

// Metrics is a point-in-time sample of an application's resource usage.
// Clients build history by polling.
type Metrics struct {
	Application string    `json:"application"`
	CollectedAt time.Time `json:"collected_at"`
	// The application-level numbers are sums over the replicas.
	CPUPercent       float64          `json:"cpu_percent"`        // percent of one core: two busy cores = 200
	CPULimitPercent  float64          `json:"cpu_limit_percent"`  // same unit; 0 = unlimited
	MemoryBytes      int64            `json:"memory_bytes"`       // working set, as `docker stats` shows it
	MemoryLimitBytes int64            `json:"memory_limit_bytes"` // 0 = unlimited
	Replicas         []ReplicaMetrics `json:"replicas"`
}

type ReplicaMetrics struct {
	Replica          int     `json:"replica"`
	Container        string  `json:"container"`
	CPUPercent       float64 `json:"cpu_percent"`
	CPULimitPercent  float64 `json:"cpu_limit_percent"`
	MemoryBytes      int64   `json:"memory_bytes"`
	MemoryLimitBytes int64   `json:"memory_limit_bytes"`
}
