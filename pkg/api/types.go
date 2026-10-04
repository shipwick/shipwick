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
	Path      string       `json:"path,omitempty"`      // the part of Domain and Aliases it serves; empty: all
	Replicas  ReplicaCount `json:"replicas"`
	Deploying bool         `json:"deploying"`
	// InFlightDeploymentID is the deployment to follow while Deploying is true.
	InFlightDeploymentID *int64    `json:"in_flight_deployment_id"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	// Static is true for an application the proxy serves from a folder: it has
	// no containers, and Replicas are all zero.
	Static bool `json:"static"`
	// CertificateProblem is null while no certificate of the application is
	// known to be out of order. AlertCount is how many alerts about it are
	// active, AlertSeverity the highest severity among them, "" for none.
	CertificateProblem *CertificateProblem `json:"certificate_problem"`
	AlertCount         int                 `json:"alert_count"`
	AlertSeverity      string              `json:"alert_severity"`
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
	// Certificates is the certificate status of every hostname the
	// application answers to; empty for an application without a domain.
	Certificates []HostnameCertificate `json:"certificates"`
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
	// Stopping is true for a container the agent is retiring in the
	// background: replaced by a deployment or left over, out of rotation, and
	// gone once its process has exited or its grace period is over. It is not
	// one of the application's replicas any more.
	Stopping bool `json:"stopping"`
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
	// Static describes the uploaded folder of a static deployment; absent for
	// a deployment that runs containers.
	Static *StaticFiles `json:"static,omitempty"`
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
	// DashboardURL is where the dashboard is served, "" when it has no
	// hostname.
	DashboardURL string `json:"dashboard_url"`
	// Alerts are the conditions that hold right now; Disk is null where the
	// agent cannot measure it (a development build off Linux).
	Alerts []Alert    `json:"alerts"`
	Disk   *DiskUsage `json:"disk"`
	// SwapBytes is how much swap the server has. Absent where the agent
	// cannot tell (a development build off Linux) and from agents older
	// than 0.8; zero is a server without swap.
	SwapBytes *int64 `json:"swap_bytes,omitempty"`
	// UnlimitedMemory names the running applications that have no memory
	// limit: each may use whatever the server has. Absent when there are
	// none, and from agents older than 0.8.
	UnlimitedMemory []string `json:"unlimited_memory,omitempty"`
	// Backups is absent from agents older than scheduled backups.
	Backups *BackupStatus `json:"backups,omitempty"`
	// Network is absent from agents older than 0.6.
	Network *NetworkStatus `json:"network,omitempty"`
	// SignIn says whether people can sign in with an OpenID Connect
	// provider; Token.Kind says whether this caller did.
	SignIn SignInStatus `json:"sign_in"`
	// LogArchive is what the agent keeps of ended containers' output; absent
	// from agents older than 0.7.
	LogArchive *LogArchiveStatus `json:"log_archive,omitempty"`
	// Update is absent from agents older than 0.7.
	Update *UpdateStatus `json:"update,omitempty"`
}

// ProxyStatus describes the reverse proxy in front of the applications.
type ProxyStatus struct {
	Enabled   bool   `json:"enabled"`   // false: no proxy configured, domains are not served
	Reachable bool   `json:"reachable"` // the last configuration sync succeeded
	Error     string `json:"error"`     // why it did not
	Routes    int    `json:"routes"`    // hostnames being served
	// DNSChallenge is true when certificates are obtained through a DNS
	// record (SHIPWICK_CLOUDFLARE_API_TOKEN): hostnames may stand behind
	// Cloudflare's proxy and may be wildcards.
	DNSChallenge bool `json:"dns_challenge"`
	// PlainLookups is true when the proxy is not Shipwick's image of this
	// version: it finds an application's replicas without keeping the last
	// answer, so a name lookup that goes unanswered holds every request.
	PlainLookups bool `json:"plain_lookups,omitempty"`
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
	// Kind is what kind of caller this is: ActorToken or ActorUser.
	Kind string `json:"kind"`
	// Applications are the ones a deploy token is limited to; empty: not
	// limited. See Allows.
	Applications []string `json:"applications"`
	// ExpiresAt is null for a token that does not expire.
	ExpiresAt *time.Time `json:"expires_at"`
}

// Token is a stored API token as GET /tokens lists it. The token value is not
// recoverable: only its hash is stored, and the hash is never returned.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"` // to the minute; null until first used
	// Applications are the ones a deploy token is limited to; empty: not
	// limited.
	Applications []string `json:"applications"`
	// ExpiresAt is null for a token that does not expire. A token whose
	// time has passed stays listed until it is revoked.
	ExpiresAt *time.Time `json:"expires_at"`
}

// CreateTokenRequest is the body of POST /tokens.
type CreateTokenRequest struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
	// Applications limits a deploy token to these applications; left out,
	// the token is not limited.
	Applications []string `json:"applications,omitempty"`
	// ExpiresAt is when the token stops being accepted; left out, never.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreatedToken is the answer to POST /tokens, the one time the token value
// itself is shown.
type CreatedToken struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token"`

	Applications []string   `json:"applications"`
	ExpiresAt    *time.Time `json:"expires_at"`
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

// Secrets kept on the server.

// Secret is a stored secret as GET /secrets lists it: its name and when it
// was set. The value is never returned.
type Secret struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetSecretRequest is the body of PUT /secrets/:name.
type SetSecretRequest struct {
	Value string `json:"value"`
}

// Images built where the developer runs shipwick deploy.

// LoadedImage is the answer to POST /applications/:name/images: the image the
// archive carried, now on the server, and how many bytes were received.
type LoadedImage struct {
	Image     string `json:"image"`
	SizeBytes int64  `json:"size_bytes"`
}

// Static applications: a folder served by the proxy, with no container.

// CodeStaticApplication: logs, metrics, jobs and one-off commands were asked
// of an application that is a folder served by the proxy; the answer is 409.
const CodeStaticApplication = "STATIC_APPLICATION"

// StaticUpload is the answer to PUT /applications/:name/static: the archive
// as the agent received it, and what was in it.
type StaticUpload struct {
	Digest    string `json:"digest"`     // sha256:<64 hex characters> of the archive
	SizeBytes int64  `json:"size_bytes"` // the files' sizes added up
	Files     int    `json:"files"`
}

// StaticFiles describes the folder a static deployment serves; the same
// numbers as its upload. Deployment.Static carries it for static deployments
// and is absent for the others.
type StaticFiles struct {
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
	Files     int    `json:"files"`
}

// Volumes of deleted applications, and rate limiting.

// VolumeInfo is a volume Shipwick created, whether or not the application it
// belongs to still exists.
type VolumeInfo struct {
	Name        string `json:"name"`        // Docker's name: shipwick_<application>_<volume>
	Application string `json:"application"` // the application it was created for
	Volume      string `json:"volume"`      // the name in its deploy.yaml
	SizeBytes   int64  `json:"size_bytes"`  // -1 when the daemon does not report it
	// Orphan is true when the application has been deleted: the volume was
	// kept on purpose, and removing it is now the operator's call.
	Orphan bool `json:"orphan"`
}

const (
	// CodeVolumeInUse means the volume belongs to an application that still
	// exists; details: {application}.
	CodeVolumeInUse = "VOLUME_IN_USE"
	// CodeRateLimited means too many authentications from the caller's address
	// failed within a minute; the Retry-After header says when to try again.
	CodeRateLimited = "RATE_LIMITED"
)

// Sending only the layers the server lacks.

// MissingLayersRequest is the body of POST /applications/:name/images/missing:
// the layers of the image about to be sent, as diff IDs (sha256:<64 hex
// characters>, the RootFS.Layers of `docker image inspect`), base layer first.
type MissingLayersRequest struct {
	Layers []string `json:"layers"`
}

// MissingLayers is the answer: the layers of the request the server's Docker
// does not have, in the request's order. The others can be left out of the
// archive.
type MissingLayers struct {
	Missing []string `json:"missing"`
}

// CodeImageIncomplete means an image archive left out layers the server does
// not have; nothing was loaded, and the whole image is to be sent.
const CodeImageIncomplete = "IMAGE_INCOMPLETE"

// Alerts and disk usage.

// Alert kinds.
const (
	AlertMemory    = "memory"    // a replica close to its memory limit
	AlertDisk      = "disk"      // the disk that holds the agent's data is filling up
	AlertRestarts  = "restarts"  // a replica that keeps being restarted
	AlertUnhealthy = "unhealthy" // an application that has not been healthy for a while
)

// Alert severities.
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// EventAlert marks an alert about the application being raised or cleared.
const EventAlert = "alert"

// Alert is a condition that holds right now and that somebody should look
// at. It is raised once, when the condition becomes true, and is gone once it
// stops being true.
type Alert struct {
	Kind     string `json:"kind"`     // see the Alert* constants
	Severity string `json:"severity"` // warning | critical
	// Application and Replica say what the alert is about: both for memory
	// and restarts, the application alone for unhealthy (replica 0), neither
	// for disk.
	Application string    `json:"application"`
	Replica     int       `json:"replica"`
	Message     string    `json:"message"`
	Since       time.Time `json:"since"`
}

// DiskUsage describes the filesystem that holds the agent's data directory —
// in the standard installation the disk Docker keeps images and volumes on.
// UsedBytes / TotalBytes is the percentage `df` shows: TotalBytes leaves out
// the blocks the filesystem reserves for root.
type DiskUsage struct {
	TotalBytes int64 `json:"total_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
}

// Registry credentials the agent keeps, and rotating the encryption key.

// Registry is a stored registry credential as GET /registries lists it. The
// password is never returned.
type Registry struct {
	Registry  string    `json:"registry"` // a hostname with an optional port, as image references name it
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetRegistryRequest is the body of PUT /registries/:registry.
type SetRegistryRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Where the agent's encryption key comes from, in KeyRotation.KeySource.
const (
	KeySourceFile        = "file"
	KeySourceEnvironment = "environment"
)

// KeyRotation is the answer to POST /server/rotate-key.
type KeyRotation struct {
	// Values counts the re-encrypted values outside deployments: secrets,
	// registry passwords and the keys of supplied certificates. Deployments counts the deployment records whose
	// environment values were re-encrypted.
	Values      int `json:"values"`
	Deployments int `json:"deployments"`
	// KeySource is "file" when the agent keeps its key in the data directory
	// and has replaced it there, and "environment" when the key is set in the
	// agent's environment, which the agent cannot change.
	KeySource string `json:"key_source"`
	// KeyFile is the file on the server that holds the new key: the key file,
	// or, for "environment", the file the agent keeps it in until it has been
	// started with it.
	KeyFile string `json:"key_file"`
	// Key is the new key, present for "environment" only: this response is
	// the one time the API shows it.
	Key string `json:"key,omitempty"`
}

const (
	// CodeRegistryLoginFailed means the registry refused the credential, or
	// could not be asked; details: {registry, refused}. Nothing was stored.
	CodeRegistryLoginFailed = "REGISTRY_LOGIN_FAILED"
	// CodeKeyRotationPending means the key was already rotated since the
	// agent started, and the agent's environment still holds the old one;
	// details: {key_file}.
	CodeKeyRotationPending = "KEY_ROTATION_PENDING"
)

// Certificates supplied by the operator, and the DNS challenge.

// Certificate is a certificate the operator supplied for a hostname, as
// GET /certificates lists it: what the chain says about itself. Neither the
// key nor the PEM is ever returned.
type Certificate struct {
	Hostname string `json:"hostname"` // the name it is stored under
	// Subjects are the DNS names of the certificate; the hostnames it covers
	// are served with it, and no authority is asked for those.
	Subjects  []string  `json:"subjects"`
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetCertificateRequest is the body of PUT /certificates/:hostname: the chain,
// the server's own certificate first, and its key, both PEM.
type SetCertificateRequest struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

const (
	// MaxCertificatePEMBytes bounds the chain, and the key, of one supplied
	// certificate.
	MaxCertificatePEMBytes = 64 * 1024
	// MaxCertificates bounds how many certificates can be supplied: each one
	// travels to the proxy with every configuration.
	MaxCertificates = 50

	// CodeInvalidCertificate means the certificate or key in a
	// PUT /certificates/:hostname cannot serve that hostname; the message
	// says why.
	CodeInvalidCertificate = "INVALID_CERTIFICATE"
)

// Traffic as the proxy saw it, and certificate status.

// CodeTrafficUnavailable means the agent cannot read the proxy's access log:
// there is no proxy, or it is not a container of the agent's compose project.
const CodeTrafficUnavailable = "TRAFFIC_UNAVAILABLE"

// Traffic is GET /applications/:name/traffic: what the proxy's access log
// says about the application's requests over a window.
type Traffic struct {
	Application string         `json:"application"`
	Since       time.Time      `json:"since"`        // start of the window
	StepSeconds int            `json:"step_seconds"` // bucket width: 60, 300 or 3600
	Totals      TrafficCounts  `json:"totals"`
	Points      []TrafficPoint `json:"points"` // buckets without a request are left out
}

// TrafficCounts are the requests of one stretch of time. The percentiles are
// estimated from a histogram of the proxy's durations and are zero when there
// were no requests.
type TrafficCounts struct {
	Requests  int64   `json:"requests"`
	Status2xx int64   `json:"status_2xx"`
	Status3xx int64   `json:"status_3xx"`
	Status4xx int64   `json:"status_4xx"`
	Status5xx int64   `json:"status_5xx"`
	Bytes     int64   `json:"bytes"` // response bodies, as sent
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// TrafficPoint is one bucket of a Traffic series; T is its start.
type TrafficPoint struct {
	T time.Time `json:"t"`
	TrafficCounts
}

// Request is one line of the proxy's access log. The path carries no query
// string, and no headers are kept.
type Request struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMs float64   `json:"duration_ms"`
	Bytes      int64     `json:"bytes"`
	Client     string    `json:"client"`
}

// Certificate states of a hostname.
const (
	CertOK            = "ok"              // a certificate for this name, not close to its end
	CertExpiring      = "expiring"        // 14 days or less left, or expired: renewal is failing
	CertObtaining     = "obtaining"       // the proxy serves the hostname but has no certificate for it yet
	CertWaitingForDNS = "waiting_for_dns" // the hostname does not point at this server and is not served
	CertUnknown       = "unknown"         // there is no proxy to ask, or it does not answer
)

// HostnameCertificate is the certificate the proxy presents for one of an
// application's hostnames. Issuer and NotAfter are set when there is one;
// Message says, for every status but ok, what is going on.
type HostnameCertificate struct {
	Hostname string     `json:"hostname"`
	Status   string     `json:"status"`
	Issuer   string     `json:"issuer"`
	NotAfter *time.Time `json:"not_after"`
	Message  string     `json:"message"`
}

// Validating a deploy.yaml before deploying it.

// Validation is the answer to POST /applications/:name/validate for a
// document that a deployment would accept. One that it would refuse is
// answered with the error the deployment would get.
type Validation struct {
	Valid bool `json:"valid"`
}

// Scheduled backups and the agent's own state.

const (
	// CodeBackupBusy means the backup is being verified or restored, or still
	// being taken; the answer is 409.
	CodeBackupBusy = "BACKUP_BUSY"
	// CodeBackupNotUsable means a restore, a verification or a download was
	// asked of a backup that did not succeed; the answer is 409.
	CodeBackupNotUsable = "BACKUP_NOT_USABLE"
	// CodeNoVolumes means a backup was asked of an application that has no
	// volumes; the answer is 409.
	CodeNoVolumes = "NO_VOLUMES"
	// CodeBackupsNotEncrypted means a backup of the agent's state was asked
	// for and SHIPWICK_BACKUP_PASSPHRASE is not set: the encryption key is
	// never written unencrypted. The answer is 409.
	CodeBackupsNotEncrypted = "BACKUPS_NOT_ENCRYPTED"
)

// EventBackup marks a backup that failed. Backups that succeed record no
// event; a verification and a restore report on the backup itself.
const EventBackup = "backup"

// BackupRunStatus is how a backup stands.
type BackupRunStatus string

const (
	BackupRunning   BackupRunStatus = "running"
	BackupSucceeded BackupRunStatus = "succeeded"
	BackupFailed    BackupRunStatus = "failed" // nothing was kept of it
)

// Backup triggers.
const (
	BackupTriggerSchedule = "schedule" // `backups.schedule` in deploy.yaml; daily for the agent's state
	BackupTriggerManual   = "manual"   // POST …/backups
)

// What is being done with a backup that exists.
const (
	BackupActivityVerify  = "verify"
	BackupActivityRestore = "restore"
)

// Backup destinations.
const (
	BackupDestinationNone  = "none"
	BackupDestinationLocal = "local"
	BackupDestinationS3    = "s3"
)

// BackupVolume is one archive of a backup: a volume of the application, or
// for the agent's state one of its two files.
type BackupVolume struct {
	Volume    string `json:"volume"`
	SizeBytes int64  `json:"size_bytes"` // of the archive, before encryption
}

// BackupRun is one backup of an application's volumes, or of the agent's
// state. Poll it until completed_at is set; a verification or a restore of it
// until activity is empty again.
type BackupRun struct {
	ID           int64           `json:"id"`
	Trigger      string          `json:"trigger"`
	Status       BackupRunStatus `json:"status"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  *time.Time      `json:"completed_at"`
	Volumes      []BackupVolume  `json:"volumes"`
	Destinations []string        `json:"destinations"` // "local", "s3"
	Encrypted    bool            `json:"encrypted"`
	Error        string          `json:"error"`
	// Activity is "verify" or "restore" while one is in progress, else "".
	Activity string `json:"activity"`
	// VerifiedAt is when the backup last proved to restore into a container
	// that came up; VerifyError is why the last verification failed. At most
	// one of them is set.
	VerifiedAt  *time.Time `json:"verified_at"`
	VerifyError string     `json:"verify_error"`
	// RestoredAt and RestoreError say the same of the last restore.
	RestoredAt   *time.Time `json:"restored_at"`
	RestoreError string     `json:"restore_error"`
}

// BackupRunDetail is a backup with the last output of the container its
// verification started.
type BackupRunDetail struct {
	BackupRun
	VerifyOutput string `json:"verify_output"`
}

// BackupStatus is the `backups` object of GET /server: where backups go, and
// how the agent's own state — its database and the key that encrypts the
// secrets in it — is doing.
type BackupStatus struct {
	Destination string `json:"destination"` // "none", "local" or "s3"
	Encrypted   bool   `json:"encrypted"`   // SHIPWICK_BACKUP_PASSPHRASE is set
	// StateLastAt is when the agent's state was last backed up; null if never.
	StateLastAt *time.Time `json:"state_last_at"`
	// StateError is why the state is not backed up, or why the last attempt
	// failed; empty when the last attempt succeeded.
	StateError string `json:"state_error"`
}

// Export, import and the standby server.

// Deployment kinds an import adds.
const (
	KindImport  = "import"  // the configuration an export carried, deployed on another server
	KindStandby = "standby" // the same, deployed stopped on a standby: `shipwick standby promote` starts it
)

const (
	// CodeImportInProgress: an import is running; a server takes one at a time.
	CodeImportInProgress = "IMPORT_IN_PROGRESS"
	// CodeInvalidExport: the body is not an export, was written with another
	// passphrase, or is damaged.
	CodeInvalidExport = "INVALID_EXPORT"
	// CodeExportInProgress: a scheduled or requested export is being written.
	CodeExportInProgress = "EXPORT_IN_PROGRESS"
	// CodeStandbyNotConfigured: the agent has no bucket to fetch exports from.
	CodeStandbyNotConfigured = "STANDBY_NOT_CONFIGURED"
)

// PassphraseHeader carries the passphrase of an uploaded export, base64
// encoded: the body is the archive itself, and headers are never logged.
const PassphraseHeader = "X-Shipwick-Passphrase"

// MinPassphraseLength is the shortest passphrase an export is written with.
const MinPassphraseLength = 12

// ExportRequest is the body of POST /export.
type ExportRequest struct {
	// Passphrase encrypts the archive; the agent keeps nothing of it.
	Passphrase string `json:"passphrase"`
	// Applications limits the export to the named ones; empty means all.
	Applications []string `json:"applications,omitempty"`
}

// Import statuses, of the whole and of one application.
const (
	ImportRunning   = "running"
	ImportSucceeded = "succeeded"
	ImportFailed    = "failed"

	ImportAppPending  = "pending"
	ImportAppRunning  = "importing"
	ImportAppImported = "imported"
	ImportAppSkipped  = "skipped"
	ImportAppFailed   = "failed"
)

// Import is the import a server is running, or ran last. An agent that
// restarts still knows it; an import it cut off by restarting has failed.
type Import struct {
	Status string `json:"status"`
	// Source says where the export came from: "upload", or the export in the
	// bucket a standby fetched.
	Source      string     `json:"source"`
	Stopped     bool       `json:"stopped"`
	Overwrite   bool       `json:"overwrite"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	// ExportedAt is when the export was written; null until it has been read.
	ExportedAt   *time.Time `json:"exported_at"`
	Secrets      int        `json:"secrets"`
	Registries   int        `json:"registries"`
	Certificates int        `json:"certificates"`
	// Applications are in the order they are deployed.
	Applications []ImportedApplication `json:"applications"`
	// Warnings are what was kept as it was, or could not be taken over.
	Warnings []string `json:"warnings"`
	Error    string   `json:"error"`
}

// ImportedApplication is one application of an import.
type ImportedApplication struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Version      string   `json:"version"`
	DeploymentID *int64   `json:"deployment_id"`
	Volumes      []string `json:"volumes"` // restored before the application first started
	// Message says why it was skipped or failed, and what to do about it.
	Message string `json:"message"`
}

// DNSRecord is a record to create or change so that a hostname reaches this
// server. Value is empty when the agent does not know its own address.
type DNSRecord struct {
	Hostname string `json:"hostname"`
	Type     string `json:"type"` // "A" or "AAAA"
	Value    string `json:"value"`
}

// Standby is what a server holds for the day it has to take over.
type Standby struct {
	// Applications were imported stopped and wait for a promotion.
	Applications []StandbyApplication `json:"applications"`
	// Records are what a promotion will ask for.
	Records []DNSRecord `json:"records"`
	// Pull is null when the agent does not fetch exports on a schedule.
	Pull *StandbyPull `json:"pull"`
	// Promotion is the promotion that runs or ran last; null when this server
	// was never promoted.
	Promotion *Promotion `json:"promotion"`
}

type StandbyApplication struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Hostnames  []string  `json:"hostnames"`
	ImportedAt time.Time `json:"imported_at"`
}

// StandbyPull is how the scheduled import from the bucket is doing.
type StandbyPull struct {
	Schedule   string     `json:"schedule"`
	LastAt     *time.Time `json:"last_at"`
	LastExport int64      `json:"last_export"` // the export imported last; 0: none yet
	LastError  string     `json:"last_error"`
}

// Statuses of one application of a promotion.
const (
	PromotedPending  = "pending"  // not reached yet
	PromotedStarting = "starting" // being started, or waited for
	PromotedRunning  = "running"  // started and ready
	PromotedStarted  = "started"  // started, not ready within its startup budget
	PromotedFailed   = "failed"   // could not be started
)

// Statuses of a promotion. One that started every application has succeeded,
// whether or not each was ready in time; the applications say which were not.
const (
	PromotionRunning   = "running"
	PromotionSucceeded = "succeeded"
	PromotionFailed    = "failed" // at least one application could not be started
)

// CodePromotionInProgress: a promotion is running; GET /standby/promotion
// follows it.
const CodePromotionInProgress = "PROMOTION_IN_PROGRESS"

// Promotion is the promotion a standby is running, or ran last: the answer of
// POST /standby/promote and of GET /standby/promotion. It is done when
// CompletedAt is set. An agent that restarts while one runs goes on with it.
type Promotion struct {
	// Applications are in the order they are started.
	Applications []PromotedApplication `json:"applications"`
	Records      []DNSRecord           `json:"records"`
	// ID counts the promotions of this server; a client that lost the answer
	// to its request tells by it whether the request arrived.
	ID          int64      `json:"id"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type PromotedApplication struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// What the list of applications says about certificates and alerts.

// CertificateProblem is the hostname of an application whose certificate is
// furthest from in order, with the status and message that
// ApplicationDetail.Certificates carries for it. Status is waiting_for_dns,
// expiring or obtaining, the worst first; "unknown" is not a problem.
type CertificateProblem struct {
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

// Access: tokens limited to applications, tokens that expire, the audit trail.

const (
	// CodeTokenExpired means the token is a real one whose time is up;
	// details: {name, expired_at}. It is answered 401, like a wrong token, and only
	// to a caller who presented the token itself.
	CodeTokenExpired = "TOKEN_EXPIRED"
	// CodeTokenLimited means the role would do, and the token is limited to
	// applications this operation is not about; details: {applications}, and
	// {application} when the operation was about one.
	CodeTokenLimited = "TOKEN_LIMITED"
)

// MaxTokenApplications is how many applications a token can be limited to.
const MaxTokenApplications = 50

// Kinds of caller: a token, or a person who signed in (ActorUser, signin.go).
const ActorToken = "token"

// Actor is who did something: what kind of caller, and its name.
type Actor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Outcomes of an audited action.
const (
	AuditOK      = "ok"      // answered 2xx; for what runs in the background: accepted
	AuditRefused = "refused" // the caller's role or application limit did not allow it
	AuditFailed  = "failed"  // anything else
)

// AuditEntry is one action that changed something, or tried to, as GET /audit
// lists it. Nothing in it comes from a request body except names.
type AuditEntry struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	Actor Actor     `json:"actor"`
	// Address is where the connection came from; behind the proxy that is
	// the proxy, and ForwardedFor is the client it reported.
	Address      string `json:"address"`
	ForwardedFor string `json:"forwarded_for"`
	Action       string `json:"action"`
	// Application is "" for what is not about one application.
	Application string `json:"application"`
	// Target is what was acted on besides the application: a token, a
	// secret, a registry, a hostname, a volume, a job, a backup's id.
	Target  string `json:"target"`
	Outcome string `json:"outcome"`
	Status  int    `json:"status"` // the HTTP status the request was answered with
	Code    string `json:"code"`   // the error code of a request that was not answered 2xx
	Detail  string `json:"detail"`
}

// Adopting backups the database has forgotten.

// BackupTriggerAdopted is the trigger of a backup that was recorded by
// POST /server/backups/adopt from the files it left: what started it is not
// known any more.
const BackupTriggerAdopted = "adopted"

// CodeForeignBucket means the bucket holds the backups of another
// installation under the agent's prefix, and nothing is adopted from it; the
// answer is 409.
const CodeForeignBucket = "FOREIGN_BUCKET"

// What a backup found in the destinations is a backup of.
const (
	BackupKindApplication = "application" // an application's volumes
	BackupKindState       = "state"       // the agent's database and encryption key
	BackupKindExport      = "export"      // a scheduled export
)

// BackupAdoptRequest is the body of POST /server/backups/adopt. Without an
// application, everything the destinations hold is looked at: every
// application's backups, the agent's state and the exports.
type BackupAdoptRequest struct {
	Application string `json:"application"`
}

// BackupAdoption is the answer: the backups that are recorded now and were
// not before, and the ones that were found and left alone.
type BackupAdoption struct {
	Adopted []AdoptedBackup `json:"adopted"`
	Skipped []SkippedBackup `json:"skipped"`
}

// AdoptedBackup is a backup the database knows from now on. Application is
// empty unless Kind is "application".
type AdoptedBackup struct {
	Kind        string    `json:"kind"`
	Application string    `json:"application"`
	Backup      BackupRun `json:"backup"`
}

// SkippedBackup is a run whose files were found and are not a backup that
// can be recorded; Reason says why.
type SkippedBackup struct {
	Kind        string `json:"kind"`
	Application string `json:"application"`
	ID          int64  `json:"id"`
	Reason      string `json:"reason"`
}

// Outbound connections: what stands between the server and the internet.

// NetworkStatus is how the agent and the Docker daemon reach what is outside
// the server. It is absent from agents older than 0.6.
type NetworkStatus struct {
	// Proxy is the proxy the agent's own requests go through — the webhook,
	// the bucket — as host:port; "" when it has none.
	Proxy string `json:"proxy"`
	// DockerProxy says whether the Docker daemon has a proxy configured.
	// Images are pulled by the daemon: the agent's proxy does nothing for them.
	DockerProxy bool `json:"docker_proxy"`
	// CAFile is true when certificate authorities of the operator's own are
	// trusted in addition to the system's (SHIPWICK_CA_FILE).
	CAFile bool `json:"ca_file"`
	// DNSResolvers are the name servers asked whether a hostname points at
	// the server: addresses, or "system". Empty: the public ones.
	DNSResolvers []string `json:"dns_resolvers"`
	// ACMEDirectory is the certificate authority the proxy obtains
	// certificates from; "" is Let's Encrypt.
	ACMEDirectory string `json:"acme_directory"`
}

// The configuration of an application, as a document.

// ApplicationConfig is the answer of GET /applications/{name}/config: the
// deploy.yaml that describes the application's active deployment.
type ApplicationConfig struct {
	Application string `json:"application"`
	// DeploymentID, Sequence and Version say which deployment the document
	// describes: the active one.
	DeploymentID int64  `json:"deployment_id"`
	Sequence     int    `json:"sequence"`
	Version      string `json:"version"`
	// Document is the deploy.yaml. A secret value that was written as a
	// reference to a secret stored on the server is that reference again; any
	// other secret value is spec.Mask.
	Document string `json:"document"`
	// Masked names the fields whose value is the mask: "env.API_KEY",
	// "proxy.basic_auth[0].password". A deployment of the document is refused
	// until each has a value or a reference. Empty: it deploys as it is.
	Masked []string `json:"masked"`
	// StaticDigest is set for a static application: the folder the active
	// deployment serves, which a deployment of the document names in ?static=.
	StaticDigest string `json:"static_digest,omitempty"`
}

// The log archive: output that outlives its container.

// What an archived output is the output of.
const (
	LogKindReplica = "replica" // one run of a replica's container, from a start to the stop after it
	LogKindRun     = "run"     // a run of a job, a pre-deploy command or a one-off command
)

// Why a replica's run ended, as far as the agent knows. For a run of a job
// the reason is the run's status: succeeded, failed, timed_out, interrupted.
const (
	LogReasonCrashed          = "crashed"           // the process exited by itself with a code other than 0
	LogReasonExited           = "exited"            // the process exited by itself with code 0
	LogReasonOOMKilled        = "oom_killed"        // killed for exceeding its memory limit
	LogReasonUnhealthy        = "unhealthy"         // restarted by the agent after failing its health check
	LogReasonStopped          = "stopped"           // the application was stopped
	LogReasonReplaced         = "replaced"          // a newer deployment took its place
	LogReasonDeploymentFailed = "deployment_failed" // a replica of a deployment that failed, removed with it
	LogReasonRemoved          = "removed"           // removed for another reason: a leftover, a rollback that failed
	LogReasonRestarted        = "restarted"         // found running again before the run that ended was archived; why it ended is not known
)

// LogArchiveEntry is the kept output of one run of a container: what
// GET /applications/:name/logs/archive lists.
type LogArchiveEntry struct {
	ID          int64  `json:"id"`
	Application string `json:"application"`
	Kind        string `json:"kind"`
	// DeploymentID is the deployment the container belonged to, Deployment
	// its number in the application's history (the #N of `shipwick status`)
	// and Version its version; null, 0 and "" once the deployment is gone.
	DeploymentID *int64 `json:"deployment_id"`
	Deployment   int    `json:"deployment"`
	Version      string `json:"version"`
	// Replica is the replica's index; 0 for a run. Job and RunID are the
	// job's name and the run for a run; "" and null for a replica.
	Replica   int    `json:"replica"`
	Job       string `json:"job"`
	RunID     *int64 `json:"run_id"`
	Container string `json:"container"`
	Reason    string `json:"reason"`
	// ExitCode is null when the process was still running as its container
	// was removed, or when the agent did not see it exit.
	ExitCode  *int `json:"exit_code"`
	OOMKilled bool `json:"oom_killed"`
	// EndedAt is when the run ended. FirstLineAt and LastLineAt are the
	// times of the first and the last line kept; null when Lines is 0.
	EndedAt     time.Time  `json:"ended_at"`
	FirstLineAt *time.Time `json:"first_line_at"`
	LastLineAt  *time.Time `json:"last_line_at"`
	Lines       int        `json:"lines"`
	// Bytes is the size of the lines kept, StoredBytes what they take on
	// the server's disk. Truncated: the run printed more than is kept, and
	// these are its last lines.
	Bytes       int64 `json:"bytes"`
	StoredBytes int64 `json:"stored_bytes"`
	Truncated   bool  `json:"truncated"`
}

// LogArchiveDetail is an entry with its lines, oldest first.
type LogArchiveDetail struct {
	LogArchiveEntry
	Output []LogLine `json:"output"`
}

// LogMatch is one line found by GET /applications/:name/logs/search, with
// where it comes from: an archive entry, or a container that still exists.
type LogMatch struct {
	LogLine
	// ArchiveID is the entry the line is kept in; null for a line read from
	// a container that exists.
	ArchiveID    *int64 `json:"archive_id"`
	DeploymentID *int64 `json:"deployment_id"`
	Deployment   int    `json:"deployment"`
	Job          string `json:"job"`
	RunID        *int64 `json:"run_id"`
}

// LogSearchResult is one page of a search, newest source first and within a
// source the newest line first. Next continues it: "" when everything that
// matches the question has been looked at. A page may hold fewer lines than
// asked for, none included, and still have a Next: a request reads a bounded
// amount.
type LogSearchResult struct {
	Lines []LogMatch `json:"lines"`
	Next  string     `json:"next"`
	// Sources and Bytes are how many containers' output this request read,
	// and how much of it.
	Sources int   `json:"sources"`
	Bytes   int64 `json:"bytes"`
}

// LogArchiveStatus is what the archive holds and may hold, for GET /server.
type LogArchiveStatus struct {
	// Enabled is false when SHIPWICK_LOG_RETENTION_SIZE is 0: nothing is kept.
	Enabled       bool  `json:"enabled"`
	Entries       int   `json:"entries"`
	Bytes         int64 `json:"bytes"`
	MaxBytes      int64 `json:"max_bytes"`
	RetentionDays int   `json:"retention_days"`
}

// Releases: whether a newer one exists.

// UpdateStatus is what the agent knows about newer releases. It is absent
// from agents older than 0.7.
type UpdateStatus struct {
	// Enabled is false when the agent was told not to ask
	// (SHIPWICK_UPDATE_CHECK=off).
	Enabled bool `json:"enabled"`
	// LatestVersion is the latest release, "v0.7.1"; "" until the agent has
	// been able to ask. CheckedAt is when it was last told, null until then.
	LatestVersion string     `json:"latest_version"`
	CheckedAt     *time.Time `json:"checked_at"`
	// Available is true when LatestVersion is newer than the agent itself.
	Available bool `json:"available"`
}
