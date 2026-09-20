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
  | 'INVALID_REQUEST'
  | 'INVALID_CONFIG'
  /** Unknown application or deployment. */
  | 'NOT_FOUND'
  /** The agent has no such operation: the dashboard is newer than the agent, or the path is wrong. */
  | 'ENDPOINT_NOT_FOUND'
  | 'DEPLOYMENT_IN_PROGRESS'
  | 'NOT_DEPLOYED'
  | 'NO_ROLLBACK_TARGET'
  | 'RUNTIME_UNAVAILABLE'
  | 'INTERNAL_ERROR'
  // Added by the dashboard's server-side proxy, never by the agent:
  | 'AGENT_UNREACHABLE'
  | 'AGENT_TIMEOUT'
  | 'CSRF_REJECTED'
  | 'METHOD_NOT_ALLOWED'

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
  replicas: ReplicaCount
  deploying: boolean
  /** The deployment to follow while `deploying` is true. */
  in_flight_deployment_id: number | null
  created_at: string
  updated_at: string
}

export interface SpecHealth {
  path: string
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

export interface AppSpec {
  name: string
  image: string
  port?: number
  domain?: string
  replicas: number
  /** Values are always masked ("********") by the agent. */
  env?: Record<string, string>
  health?: SpecHealth | null
  resources: SpecResources
  restart: { policy: 'always' | 'on-failure' | 'never' | string }
  deploy: { strategy: string }
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
}

/**
 * deploy: a deploy.yaml was submitted. redeploy: the active configuration
 * again, possibly with another image. rollback: the configuration of an
 * earlier successful deployment.
 */
export type DeploymentKind = 'deploy' | 'redeploy' | 'rollback'

export type EventLevel = 'info' | 'warn' | 'error'
export type EventType = 'step' | 'state' | 'log' | 'app' | 'supervisor'

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

export interface RollbackRequest {
  deployment_id?: number
}

export interface RedeployRequest {
  image?: string
}
