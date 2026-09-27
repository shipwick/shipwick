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

// Role is what a token may do. Every endpoint requires one of them; a role
// includes the ones below it.
type Role string

const (
	RoleRead   Role = "read"   // see everything: status, logs, history, metrics
	RoleDeploy Role = "deploy" // and change what runs: deploy, redeploy, roll back, stop, start
	RoleAdmin  Role = "admin"  // and everything else: delete applications, manage tokens, restore backups
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
	Aliases   []string     `json:"aliases,omitempty"`   // served like Domain
	Redirects []string     `json:"redirects,omitempty"` // redirected to Domain
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
	// By is the name of the token that started the deployment; absent for
	// deployments recorded before tokens had names.
	By string `json:"by,omitempty"`
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
	// Token identifies the token the request was made with, so a client
	// knows what it may do before it tries.
	Token         TokenIdentity      `json:"token"`
	Notifications NotificationStatus `json:"notifications"`
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

// Metrics is a point-in-time sample of an application's resource usage; the
// record over time is MetricsHistory.
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

// API tokens and roles.

const (
	CodeForbidden   = "FORBIDDEN"    // the token's role does not cover the operation; details: {role, required}
	CodeTokenExists = "TOKEN_EXISTS" // a token with that name exists
)

// RootTokenName is the name of the token the agent is configured with
// (SHIPWICK_AGENT_TOKEN, or the one generated on first start). It has the
// admin role, is not stored in the database and cannot be revoked through the
// API.
const RootTokenName = "root"

// TokenIdentity is who a request was made as: the token's name and role.
type TokenIdentity struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// Token is a stored API token as GET /tokens lists it. The token value is not
// recoverable: only its hash is stored, and the hash is never returned.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"` // to the minute; null until first used
}

// CreateTokenRequest is the body of POST /tokens.
type CreateTokenRequest struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// CreatedToken is the answer to POST /tokens, the one time the token value
// itself is shown.
type CreatedToken struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token"`
}

// Volume backups.

// CodeApplicationRunning: a restore was asked of an application that is not
// stopped; the answer is 409.
const CodeApplicationRunning = "APPLICATION_RUNNING"

// Volume is one of an application's volumes, as the active deployment mounts
// it: GET /applications/:name/volumes.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Notifications and metrics history.

// NotificationStatus says where the agent reports deployment outcomes and
// applications going down or recovering.
type NotificationStatus struct {
	Webhook bool `json:"webhook"` // SHIPWICK_WEBHOOK_URL is set
}

// MetricsHistory is the sampled resource usage of an application over a
// window, aggregated into buckets of Step: the average CPU and the peak
// memory of each replica per bucket. Buckets without a sample are absent.
type MetricsHistory struct {
	Application string          `json:"application"`
	Since       time.Time       `json:"since"` // start of the window
	Step        string          `json:"step"`  // bucket width: "30s", "5m" or "1h"
	Series      []MetricsSeries `json:"series"`
	Limits      MetricsLimits   `json:"limits"`
}

type MetricsSeries struct {
	Replica int            `json:"replica"`
	Points  []MetricsPoint `json:"points"`
}

type MetricsPoint struct {
	At          time.Time `json:"at"`
	CPUPercent  float64   `json:"cpu_percent"`  // average over the bucket, percent of one core
	MemoryBytes int64     `json:"memory_bytes"` // peak working set in the bucket
}

// MetricsLimits are the active deployment's per-replica limits, as in
// deploy.yaml: cores and bytes, 0 when unlimited.
type MetricsLimits struct {
	CPU         float64 `json:"cpu"`
	MemoryBytes int64   `json:"memory_bytes"`
}

// Jobs: the pre-deploy hook, scheduled jobs and one-off commands.

// CodeJobAlreadyRunning: a run of this job has not finished yet.
const CodeJobAlreadyRunning = "JOB_ALREADY_RUNNING"

// EventJob marks what a job did: a scheduled job or a one-off command that
// failed or timed out. Successful runs record no event.
const EventJob = "job"

// RunStatus is how a run of a job stands.
type RunStatus string

const (
	RunRunning     RunStatus = "running"
	RunSucceeded   RunStatus = "succeeded"   // exit code 0
	RunFailed      RunStatus = "failed"      // any other exit code, or the container could not be started
	RunTimedOut    RunStatus = "timed_out"   // stopped when its timeout ran out
	RunInterrupted RunStatus = "interrupted" // the agent was restarted while it ran
)

// Run kinds.
const (
	RunKindHook      = "hook"      // the pre-deploy command of a deployment
	RunKindScheduled = "scheduled" // started by the schedule
	RunKindManual    = "manual"    // started by hand: `shipwick run`, `shipwick jobs run`
)

// Job is a scheduled job of an application and how it has been doing.
type Job struct {
	Name     string        `json:"name"`
	Schedule string        `json:"schedule"` // five cron fields, UTC
	Command  []string      `json:"command"`
	Timeout  spec.Duration `json:"timeout"`
	LastRun  *Run          `json:"last_run"`
	// NextRunAt is the next time the schedule fires, in UTC; null while the
	// application is stopped, since stopped applications run no jobs.
	NextRunAt *time.Time `json:"next_run_at"`
}

// Run is one execution of a job or one-off command.
type Run struct {
	ID          int64     `json:"id"`
	Application string    `json:"application"`
	Job         string    `json:"job"` // the job's name; "pre-deploy" for the hook, "run" for an ad-hoc command
	Kind        string    `json:"kind"`
	Command     []string  `json:"command"`
	Status      RunStatus `json:"status"`
	ExitCode    *int      `json:"exit_code"` // null until the process has exited
	// DeploymentID is the deployment whose image and environment the run
	// used; null once that deployment is gone from the history.
	DeploymentID *int64     `json:"deployment_id"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// RunDetail is a run with the tail of its output.
type RunDetail struct {
	Run
	Output string `json:"output"` // the last lines the container wrote, at most 64 KB
}

// RunRequest is the body of POST /applications/:name/run.
type RunRequest struct {
	Command []string `json:"command"`
}
