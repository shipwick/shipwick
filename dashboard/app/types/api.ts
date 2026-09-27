/**
 * Wire types of the Shipwick agent API.
 *
 * These mirror pkg/api/types.go and pkg/spec/spec.go field by field. Keep them
 * in sync with the Go structs: the JSON tags there are the source of truth.
 */

export interface ApiErrorBody {
  code: string
  message: string
  details: Record<string, unknown>
}

export interface ApiErrorEnvelope {
  error: ApiErrorBody
}

export interface ApiEnvelope<T> {
  data: T
}

/** Error codes the agent produces, plus the ones the dashboard proxy adds. */
export type ApiErrorCode =
  | 'UNAUTHORIZED'
  /** The token's role does not cover the endpoint; details: {role, required}. */
  | 'FORBIDDEN'
  | 'INVALID_REQUEST'
  | 'INVALID_CONFIG'
  /** Unknown application, deployment or token. */
  | 'NOT_FOUND'
  /** The agent has no such operation: the dashboard is newer than the agent, or the path is wrong. */
  | 'ENDPOINT_NOT_FOUND'
  | 'DEPLOYMENT_IN_PROGRESS'
  | 'NOT_DEPLOYED'
  | 'NO_ROLLBACK_TARGET'
  /** A token with that name exists. */
  | 'TOKEN_EXISTS'
  /** A volume restore was asked of an application that is not stopped. */
  | 'APPLICATION_RUNNING'
  /** A run of this job has not finished yet; a job runs one at a time. */
  | 'JOB_ALREADY_RUNNING'
  | 'RUNTIME_UNAVAILABLE'
  | 'INTERNAL_ERROR'
  // Added by the dashboard's server-side proxy, never by the agent:
  | 'AGENT_UNREACHABLE'
  | 'AGENT_TIMEOUT'
  | 'CSRF_REJECTED'
  | 'METHOD_NOT_ALLOWED'

/** What a token may do. A role includes the ones below it: read < deploy < admin. */
export type Role = 'read' | 'deploy' | 'admin'

export type DeploymentStatus =
  | 'PENDING'
  | 'BUILDING'
  | 'STARTING'
  | 'HEALTH_CHECKING'
  | 'HEALTHY'
  | 'ACTIVE'
  | 'SUPERSEDED'
  | 'FAILED'
  | 'ROLLBACK'
  | 'RESTORING'
  | 'ROLLED_BACK'

export type ApplicationStatus =
  | 'HEALTHY'
  | 'DEGRADED'
  | 'DOWN'
  | 'CRASH_LOOP'
  | 'STOPPED'
  | 'DEPLOYING'
  | 'FAILED'

export type DesiredState = 'running' | 'stopped'

/** "" means the application defines no health check. */
export type ReplicaHealth = '' | 'unknown' | 'starting' | 'healthy' | 'unhealthy'

export interface ReplicaCount {
  desired: number
  running: number
  healthy: number
}

export interface Application {
  name: string
  status: ApplicationStatus
  desired_state: DesiredState
  /** Empty until the first deployment succeeds. */
  image: string
  version: string
  domain: string
  /** Further hostnames served exactly like `domain`; absent when there are none. */
  aliases?: string[]
  /** Hostnames answered with a redirect to `domain`; absent when there are none. */
  redirects?: string[]
  replicas: ReplicaCount
  deploying: boolean
  /** The deployment to follow while `deploying` is true. */
  in_flight_deployment_id: number | null
  created_at: string
  updated_at: string
}

/**
 * One of three checks: an HTTP GET of `path`, a TCP connection to port `tcp`,
 * or `command` run inside the replica and expected to exit 0. Exactly one of
 * the three is present.
 */
export interface SpecHealth {
  path?: string
  tcp?: number
  command?: string[]
  /** Go duration strings: "10s", "1m30s". */
  interval: string
  timeout: string
  retries: number
}

export interface SpecResources {
  /** Cores per replica; absent means unlimited. */
  cpu?: number
  /** Bytes per replica; absent means unlimited. */
  memory_bytes?: number
}

/** A container port published on a port of the server itself, outside the proxy. */
export interface SpecPublish {
  /** Inside the container. */
  port: number
  /** On the server. */
  host: number
  /** Server address the port is bound on; absent means every address. */
  address?: string
  protocol: 'tcp' | 'udp' | string
}

/** The Docker logging driver of the replicas, with its options. */
export interface SpecLogging {
  driver: string
  /** Absent, not empty, when there are none. */
  options?: Record<string, string>
}

export interface AppSpec {
  name: string
  image: string
  port?: number
  domain?: string
  aliases?: string[]
  redirects?: string[]
  replicas: number
  /** Values are always masked ("********") by the agent. */
  env?: Record<string, string>
  health?: SpecHealth | null
  resources: SpecResources
  /** Named volumes; absent when the application has none. */
  volumes?: SpecVolume[]
  /** Ports published on the server; absent when there are none. */
  publish?: SpecPublish[]
  /** Replace the image's ENTRYPOINT, CMD and USER; absent when the image's own apply. */
  entrypoint?: string[]
  command?: string[]
  user?: string
  /** A command run in a one-off container before any replica is replaced; the deployment fails if it does. */
  pre_deploy?: SpecHook
  /** Commands run on a schedule in one-off containers from the application's image. */
  jobs?: SpecJob[]
  logging?: SpecLogging
  restart: { policy: 'always' | 'on-failure' | 'never' | string }
  deploy: { strategy: 'rolling' | 'recreate' | string }
}

export interface SpecHook {
  command: string[]
  /** Go duration string: "10m0s". */
  timeout: string
}

export interface SpecJob {
  name: string
  /** Five cron fields, read in UTC. */
  schedule: string
  command: string[]
  timeout: string
}

export interface SpecVolume {
  name: string
  /** Absolute path inside the container. */
  path: string
}

export interface Container {
  id: string
  name: string
  deployment_id: number
  replica: number
  image: string
  /** Docker state: created, running, exited, ... */
  state: string
  exit_code: number
  oom_killed: boolean
  ip: string
  started_at: string | null
  health: ReplicaHealth
  restarts: number
  crash_loop: boolean
}

export interface Deployment {
  id: number
  application: string
  sequence: number
  version: string
  image: string
  status: DeploymentStatus
  error: string
  started_at: string
  /** Non-null once the engine is completely done with the deployment. */
  completed_at: string | null
  /** How the deployment came to be. */
  kind: DeploymentKind
  /** For a redeploy or rollback: the deployment whose stored configuration was re-used. */
  source_deployment_id: number | null
  /** Name of the token that started it ("root" for the agent's own); absent on deployments made before tokens had names. */
  by?: string
}

/**
 * deploy: a deploy.yaml was submitted. redeploy: the active configuration
 * again, possibly with another image. rollback: the configuration of an
 * earlier successful deployment.
 */
export type DeploymentKind = 'deploy' | 'redeploy' | 'rollback'

export type EventLevel = 'info' | 'warn' | 'error'
/** `job`: a scheduled job or one-off command that failed or timed out (level warn); successful runs record nothing. */
export type EventType = 'step' | 'state' | 'log' | 'app' | 'supervisor' | 'job'

export interface AgentEvent {
  id: number
  deployment_id: number | null
  level: EventLevel
  type: EventType
  message: string
  created_at: string
}

export interface DeploymentDetail extends Deployment {
  spec: AppSpec
  events: AgentEvent[]
}

export interface ApplicationDetail extends Application {
  spec: AppSpec | null
  active_deployment: Deployment | null
  containers: Container[]
}

export interface LogLine {
  replica: number
  container: string
  stream: 'stdout' | 'stderr'
  time: string
  message: string
}

export interface ProxyStatus {
  enabled: boolean
  reachable: boolean
  error: string
  routes: number
}

/** Who a request was made as: the token's name and role. */
export interface TokenIdentity {
  name: string
  role: Role
}

export interface NotificationStatus {
  /** SHIPWICK_WEBHOOK_URL is set on the agent. The URL itself is never exposed. */
  webhook: boolean
}

export interface Server {
  agent_version: string
  hostname: string
  os: string
  kernel: string
  architecture: string
  docker_version: string
  cpus: number
  memory_bytes: number
  applications: number
  containers: number
  /** The reverse proxy in front of the applications; `enabled` is false when none is configured. */
  proxy: ProxyStatus
  /** The caller's own token. Absent on agents before tokens had roles, which know a single admin token. */
  token?: TokenIdentity
  /** Absent on older agents; treat as no webhook. */
  notifications?: NotificationStatus
}

export interface ReplicaMetrics {
  replica: number
  container: string
  cpu_percent: number
  /** Same unit as cpu_percent; 0 when unlimited. */
  cpu_limit_percent: number
  memory_bytes: number
  memory_limit_bytes: number
}

export interface ApplicationMetrics {
  application: string
  collected_at: string
  /** Percent of one core, summed over replicas: two busy cores are 200. */
  cpu_percent: number
  /** Sum of the replicas' limits, in the same unit; 0 when unlimited. */
  cpu_limit_percent: number
  memory_bytes: number
  /** 0 when unlimited. */
  memory_limit_bytes: number
  replicas: ReplicaMetrics[]
}

/** Accepted values of `since` on the metrics history endpoint. */
export type MetricsRange = '1h' | '24h' | '7d'

export interface MetricsPoint {
  /** Start of the bucket. */
  at: string
  /** Average over the bucket, percent of one core. */
  cpu_percent: number
  /** Peak working set in the bucket. */
  memory_bytes: number
}

export interface MetricsSeries {
  replica: number
  /** Ordered by time. Sparse: a bucket in which the replica had no sample is absent, not zero. */
  points: MetricsPoint[]
}

/** The active deployment's per-replica limits as written in deploy.yaml; 0 when unlimited. */
export interface MetricsLimits {
  /** Cores. A CPU chart's limit line is `cpu * 100`. */
  cpu: number
  memory_bytes: number
}

export interface MetricsHistory {
  application: string
  /** Start of the window (RFC 3339). */
  since: string
  /** Bucket width chosen by the agent: "30s", "5m" or "1h". */
  step: string
  /** One entry per replica that has at least one sample in the window, ordered by replica. */
  series: MetricsSeries[]
  limits: MetricsLimits
}

/** One of an application's volumes, as the active deployment mounts it. */
export interface Volume {
  name: string
  path: string
}

/** A stored API token as GET /tokens lists it: never its value or hash. */
export interface Token {
  id: number
  name: string
  role: Role
  created_at: string
  /** Kept to the minute; null until first used. */
  last_used_at: string | null
}

export interface CreateTokenRequest {
  name: string
  role: Role
}

/** The answer to POST /tokens: the one time the token value itself is shown. */
export interface CreatedToken {
  id: number
  name: string
  role: Role
  created_at: string
  token: string
}

/**
 * running · succeeded (exit 0) · failed (any other exit code, or the container
 * could not be started) · timed_out (stopped when its timeout ran out) ·
 * interrupted (the agent was restarted while it ran).
 */
export type RunStatus = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'interrupted'

/** hook: the pre-deploy command of a deployment. scheduled: started by the schedule. manual: started by hand. */
export type RunKind = 'hook' | 'scheduled' | 'manual'

/** One execution of a job, the pre-deploy hook or a one-off command. */
export interface Run {
  id: number
  application: string
  /** The job's name; "pre-deploy" for the hook, "run" for a one-off command. */
  job: string
  kind: RunKind
  command: string[]
  status: RunStatus
  /** Null while running, after a timeout or interruption, or when the container could not be started. */
  exit_code: number | null
  /** The deployment whose image and environment the run used; null once it is gone from the history. */
  deployment_id: number | null
  started_at: string
  /** Set once the run is over: the only signal to stop polling. */
  finished_at: string | null
}

export interface RunDetail extends Run {
  /** The last 200 lines the container wrote (at most 64 KB), joined with newlines; may start with "… (truncated)". */
  output: string
}

/** A scheduled job of the active deployment and how it has been doing. */
export interface Job {
  name: string
  /** Five cron fields, read in UTC. */
  schedule: string
  command: string[]
  timeout: string
  last_run: Run | null
  /** UTC; null while the application is stopped, since a stopped application runs no jobs. */
  next_run_at: string | null
}

/** Body of POST /applications/:name/run: an argv, never a shell string. */
export interface RunRequest {
  command: string[]
}

export interface RollbackRequest {
  deployment_id?: number
}

export interface RedeployRequest {
  image?: string
}
