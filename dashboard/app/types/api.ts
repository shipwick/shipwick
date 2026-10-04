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
  /** The token was valid until a moment that has passed; details: {name, expired_at}. Answered 401, only to its holder. */
  | 'TOKEN_EXPIRED'
  /** A person's session reached its end, ten hours after the sign-in; details: {name, expired_at}. Answered 401. */
  | 'SESSION_EXPIRED'
  /** A person's session was ended: their rule was removed or changed, or an admin signed them out; details: {name, reason}. Answered 401. */
  | 'SESSION_ENDED'
  /** The provider's answer to a sign-in was not accepted; details: {reason}. */
  | 'SIGN_IN_FAILED'
  /** Somebody signed in at the provider and no rule gives them a role; details: {email}. */
  | 'ACCESS_NOT_GRANTED'
  /** The agent has no sign-in provider: it takes API tokens only. */
  | 'SIGN_IN_NOT_CONFIGURED'
  /** The sign-in provider cannot be reached or used. */
  | 'SIGN_IN_UNAVAILABLE'
  /** A deploy token limited to some applications asked to change another, or something that is not about one application; details: {applications, application?}. */
  | 'TOKEN_LIMITED'
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
  /** Logs, metrics, jobs or a command were asked of a folder the proxy serves; it has no containers. */
  | 'STATIC_APPLICATION'
  /** The volume belongs to an application that still exists; details: {application}. */
  | 'VOLUME_IN_USE'
  /** 20 authentications from this address failed within a minute; answered without checking the token. */
  | 'RATE_LIMITED'
  /** An image archive left out layers the server lacks; only the CLI sends images. */
  | 'IMAGE_INCOMPLETE'
  /** A registry refused a credential, or could not be asked; details: {registry, refused}. Nothing was stored. */
  | 'REGISTRY_LOGIN_FAILED'
  /** The key was rotated since the agent started and its environment still holds the old one; details: {key_file}. */
  | 'KEY_ROTATION_PENDING'
  /** A supplied certificate or its key cannot serve the hostname; the message says why. */
  | 'INVALID_CERTIFICATE'
  /** The agent has no access log to read: no proxy, or not the caddy container of its compose project. */
  | 'TRAFFIC_UNAVAILABLE'
  /** The backup is still being taken, verified, restored or removed. */
  | 'BACKUP_BUSY'
  /** The backup failed, so nothing was kept of it; or its files do not decrypt. */
  | 'BACKUP_NOT_USABLE'
  /** A backup was asked of an application without volumes. */
  | 'NO_VOLUMES'
  /** A backup of the agent's state was asked for and SHIPWICK_BACKUP_PASSPHRASE is not set. */
  | 'BACKUPS_NOT_ENCRYPTED'
  /** The body is not an export, was written with another passphrase, or is damaged. */
  | 'INVALID_EXPORT'
  /** An import is running; a server takes one at a time. */
  | 'IMPORT_IN_PROGRESS'
  /** A scheduled or requested export is being written. */
  | 'EXPORT_IN_PROGRESS'
  /** The agent has no bucket to fetch exports from. */
  | 'STANDBY_NOT_CONFIGURED'
  /** A promotion is running: a second one, an import and a fetch from the bucket wait for it. */
  | 'PROMOTION_IN_PROGRESS'
  /** The bucket holds another installation's backups; nothing in it is adopted. */
  | 'FOREIGN_BUCKET'
  | 'RUNTIME_UNAVAILABLE'
  | 'INTERNAL_ERROR'
  // Added by the dashboard's server-side proxy, never by the agent:
  | 'AGENT_UNREACHABLE'
  | 'AGENT_TIMEOUT'
  | 'CSRF_REJECTED'
  | 'METHOD_NOT_ALLOWED'
  /** Several servers are configured and the request named none; details: {servers}. */
  | 'SERVER_REQUIRED'
  /** The request named a server the dashboard was not configured with; details: {servers}. */
  | 'UNKNOWN_SERVER'
  /** An export that was started for a download was not fetched in time, or was fetched already. */
  | 'DOWNLOAD_EXPIRED'

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
  /** The part of `domain` and the aliases the application serves ("/api"); absent when it serves all of it. */
  path?: string
  replicas: ReplicaCount
  deploying: boolean
  /** The deployment to follow while `deploying` is true. */
  in_flight_deployment_id: number | null
  created_at: string
  updated_at: string
  /** A folder the proxy serves itself: no containers, `replicas` all zero, `image` empty. */
  static: boolean
  /** The hostname whose certificate is worst off, or null when all are in order. Absent on agents before 0.6. */
  certificate_problem?: CertificateProblem | null
  /** The active alerts about this application. Absent on agents before 0.6. */
  alert_count?: number
  /** "critical" if any of them is, else "warning", else "". Absent on agents before 0.6. */
  alert_severity?: '' | AlertSeverity | string
}

/** One hostname of an application whose certificate is not in order, with the status and message of its entry in `certificates`. */
export interface CertificateProblem {
  hostname: string
  /** Worst first: waiting_for_dns, expiring, obtaining. Never ok or unknown. */
  status: CertificateStatus | string
  message: string
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
  /** Time a replica gets to come up before failed checks count; absent when zero. */
  start_period?: string
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

/** The image is built where `shipwick deploy` runs and sent to the server; the agent never builds. */
export interface SpecBuild {
  /** Relative to deploy.yaml. */
  context: string
  /** Relative to the context. */
  dockerfile: string
}

/** A folder served by the proxy as it is, with no container. */
export interface SpecStatic {
  /** Relative to deploy.yaml, on the developer's machine. */
  dir: string
  /** The page served with 200 for a path that names no file (a single-page application's index.html); absent without one. */
  fallback?: string
}

/** An account the proxy asks for before it lets a request through. The password is always masked ("********"). */
export interface SpecBasicAuth {
  /** The path the account protects, as the visitor asks for it; absent for the whole application. */
  path?: string
  username: string
  password: string
}

/** A redirect the proxy answers itself. `from` lies under the application's path; `to` may point anywhere on the host. */
export interface SpecPathRedirect {
  from: string
  to: string
  status: number
}

/** What the proxy does with the application's requests besides passing them on. Every part is absent when unset. */
export interface SpecProxy {
  /** The application's `path` is removed before the request reaches it. */
  strip_prefix?: boolean
  /** Added to every response. */
  headers?: Record<string, string>
  basic_auth?: SpecBasicAuth[]
  redirects?: SpecPathRedirect[]
}

/** Backups of the application's volumes the agent takes by itself. */
export interface SpecBackups {
  /** Five cron fields, read in UTC. */
  schedule: string
  /** How many successful backups are kept. */
  keep: number
  /** Run in the replica before the volumes are archived (a database dump or checkpoint). */
  before?: string[]
  /** How long `before` gets, a Go duration ("1h0m0s"). Present with `before` on deployments made by 0.6; absent means one hour. */
  before_timeout?: string
  /** The application is stopped while the archive is taken, and started again. */
  stop?: boolean
  /**
   * Where `before` runs: "container" for a container of its own beside the
   * replica, which is ended at `before_timeout`; absent for inside the
   * replica. Absent on agents before 0.7.
   */
  before_in?: 'container' | string
}

export interface AppSpec {
  name: string
  /** Empty for a static application. `shipwick.local/<name>:<tag>` for one with `build`. */
  image: string
  build?: SpecBuild
  static?: SpecStatic
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
  /** Replicas, jobs and commands run under an init process that passes signals on and reaps what they leave behind; absent when unset. */
  init?: boolean
  /** The part of the domain the application serves; absent when it serves all of it. */
  path?: string
  proxy?: SpecProxy
  backups?: SpecBackups
  restart: { policy: 'always' | 'on-failure' | 'never' | string }
  /** `stop_timeout`: how long a replica gets after SIGTERM before it is killed, a Go duration ("2m0s"); absent means the agent's default. */
  deploy: { strategy: 'rolling' | 'recreate' | string, stop_timeout?: string }
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
  /**
   * The agent is retiring the container in the background: replaced by a
   * rollout, or a leftover. Its `state` stays "running" until the process
   * exits; `health` and `restarts` mean nothing for it. Absent on agents before 0.6.
   */
  stopping?: boolean
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
  /** The folder a static deployment serves; absent for a deployment that runs containers. */
  static?: StaticFiles
}

/** What PUT /applications/:name/static received; the same numbers reappear as `Deployment.static`. */
export interface StaticUpload {
  /** sha256:<64 hex characters> of the archive; the deployment's version is its first twelve. */
  digest: string
  /** The files' sizes added up. */
  size_bytes: number
  files: number
}

export type StaticFiles = StaticUpload

/** The answer to POST /applications/:name/images: the image the archive carried, now on the server. */
export interface LoadedImage {
  image: string
  size_bytes: number
}

/**
 * deploy: a deploy.yaml was submitted. redeploy: the active configuration
 * again, possibly with another image. rollback: the configuration of an
 * earlier successful deployment. import: the configuration an export carried,
 * deployed on another server. standby: the same, deployed stopped on a
 * standby; a promotion starts it.
 */
export type DeploymentKind = 'deploy' | 'redeploy' | 'rollback' | 'import' | 'standby'

export type EventLevel = 'info' | 'warn' | 'error'
/**
 * `job`: a scheduled job or one-off command that failed or timed out (level warn); successful runs record nothing.
 * `alert`: an alert about the application raised (warn, or error when critical) or cleared (info).
 * `backup`: a scheduled backup that failed, a verification, a restore.
 */
export type EventType = 'step' | 'state' | 'log' | 'app' | 'supervisor' | 'job' | 'alert' | 'backup'

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
  /**
   * After a deployment completed this may still list a container of the
   * previous one: it is being given its `stop_timeout` to exit, and is not
   * counted in `replicas`.
   */
  containers: Container[]
  /** One entry per hostname, in the order domain, aliases, redirects; empty without a domain. Absent on agents before 0.5. */
  certificates?: HostnameCertificate[]
}

export type CertificateStatus = 'ok' | 'expiring' | 'obtaining' | 'waiting_for_dns' | 'unknown'

/** The certificate the proxy presents for one of an application's hostnames. */
export interface HostnameCertificate {
  hostname: string
  status: CertificateStatus | string
  /** Set once a certificate has been seen; "" before. */
  issuer: string
  not_after: string | null
  /** Says what is going on for every status but ok, where it is "". */
  message: string
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
  /** Certificates are obtained through a DNS record, so hostnames may be proxied by Cloudflare and may be wildcards. Absent on agents before 0.5. */
  dns_challenge?: boolean
  /** The proxy is not Shipwick's image of this version: a name lookup that goes unanswered holds every request. Absent unless true, and on agents before 0.8. */
  plain_lookups?: boolean
}

/** Who a request was made as: the token's name and role, and what narrows it. */
export interface TokenIdentity {
  name: string
  role: Role
  /** "token"; a person signed in through an identity provider is "user". Absent on agents before 0.6. */
  kind?: ActorKind | string
  /** The applications a deploy token is limited to; [] means not limited. Absent on agents before 0.6. */
  applications?: string[]
  /** When the token stops working; null for one that does not expire. Absent on agents before 0.6. */
  expires_at?: string | null
}

export type ActorKind = 'token' | 'user'

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
  /** How much swap the server has; 0 is none. Absent where the agent cannot tell, and on agents before 0.8. */
  swap_bytes?: number
  /** The running applications without a memory limit. Absent when there are none, and on agents before 0.8. */
  unlimited_memory?: string[]
  applications: number
  containers: number
  /** The reverse proxy in front of the applications; `enabled` is false when none is configured. */
  proxy: ProxyStatus
  /** The caller's own token. Absent on agents before tokens had roles, which know a single admin token. */
  token?: TokenIdentity
  /** Absent on older agents; treat as no webhook. */
  notifications?: NotificationStatus
  /** `https://<SHIPWICK_DASHBOARD_DOMAIN>`, or "" when the dashboard has no hostname. Absent on agents before 0.5. */
  dashboard_url?: string
  /** The conditions that hold right now, oldest first; [] when there are none. Absent on agents before 0.5. */
  alerts?: Alert[]
  /** The filesystem that holds the agent's data; null where it cannot be measured. Absent on agents before 0.5. */
  disk?: DiskUsage | null
  /** Where backups go and how the agent's own state is doing. Absent on agents before 0.5. */
  backups?: BackupStatus
  /** How the agent leaves the server. Absent on agents before 0.6. */
  network?: NetworkStatus
  /** Whether people can sign in through an OpenID Connect provider. Absent on agents before 0.6. */
  sign_in?: SignInStatus
  /** What the log archive holds and how much it may. Absent on agents before 0.7. */
  log_archive?: LogArchiveStatus
  /** Whether a newer release exists, as the agent last heard. Absent on agents before 0.7: treated as a check that is off. */
  update?: UpdateStatus
}

export interface SignInStatus {
  configured: boolean
  /** The provider's issuer URL; "" when none is configured. */
  issuer: string
  /** The ID token claim people are named by; "" when sign-in is not configured. Absent on agents before 0.7, which name people by `email`. */
  name_claim?: string
}

/** `name`: one person by the name the provider's claim holds, for accounts without an address. From 0.7. */
export type AccessRuleKind = 'email' | 'group' | 'domain' | 'name'

/** Who gets which role when they sign in through the provider: an address, a group of the provider's, a whole domain, or a name. */
export interface AccessRule {
  id: number
  kind: AccessRuleKind | string
  /** The address, the group's name as the provider sends it, the domain without "*@", or the name exactly as the claim holds it. */
  subject: string
  role: Role
  /** For the deploy role; [] means every application. */
  applications: string[]
  created_at: string
  /** The token or person that granted it. */
  created_by: string
}

/** Body of POST /access/rules; granting again for the same kind and subject replaces the rule. */
export interface GrantAccessRequest {
  kind: AccessRuleKind
  subject: string
  role: Role
  applications?: string[]
}

/** One session of a person who signed in and would be served right now: GET /access/sessions. */
export interface PersonSession {
  id: number
  /** The person's name: an address with the `email` claim, otherwise the claim's value as issued. */
  email: string
  role: Role
  applications: string[]
  created_at: string
  expires_at: string
  /** Kept to the minute; null until first used. */
  last_used_at: string | null
}

/** The answer of DELETE /access/sessions/:email: how many sessions were ended. */
export interface SignedOut {
  sessions: number
}

/** The `network` object of GET /server: what was configured for a server behind a proxy or without a way out. */
export interface NetworkStatus {
  /** host:port of the proxy the agent uses, never its credentials; "" when none. */
  proxy: string
  /** The Docker daemon has a proxy configured: images are pulled by the daemon, not by the agent. */
  docker_proxy: boolean
  /** SHIPWICK_CA_FILE adds certificate authorities to the system's. */
  ca_file: boolean
  /** [] for the public resolvers, ["system"], or addresses. */
  dns_resolvers: string[]
  /** "" for Let's Encrypt. */
  acme_directory: string
}

export type AlertKind = 'memory' | 'disk' | 'restarts' | 'unhealthy'
export type AlertSeverity = 'warning' | 'critical'

/** A condition that holds right now and that somebody should look at. */
export interface Alert {
  kind: AlertKind | string
  /** Only `disk` and `unhealthy` become critical. */
  severity: AlertSeverity | string
  /** "" for `disk`. */
  application: string
  /** 0 for `disk` and `unhealthy`. */
  replica: number
  /** A complete sentence, with what to do about it. */
  message: string
  /** When it was raised; unchanged when a warning turns critical. */
  since: string
}

/** `used_bytes / total_bytes` is the percentage `df` shows. */
export interface DiskUsage {
  total_bytes: number
  used_bytes: number
}

export type BackupDestination = 'none' | 'local' | 's3'

/** The `backups` object of GET /server. */
export interface BackupStatus {
  destination: BackupDestination | string
  /** SHIPWICK_BACKUP_PASSPHRASE is set. The agent's state is only ever backed up encrypted. */
  encrypted: boolean
  /** The last successful backup of the agent's state; null if there has been none. */
  state_last_at: string | null
  /** Why the state is not backed up, or why the last attempt failed; "" when the last attempt succeeded. */
  state_error: string
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

/** A volume Shipwick created, whether or not its application still exists, as GET /volumes lists it. */
export interface VolumeInfo {
  /** Docker's name: shipwick_<application>_<volume>. */
  name: string
  application: string
  /** The name in its deploy.yaml. */
  volume: string
  /** -1 when the daemon does not report a size. */
  size_bytes: number
  /** The application has been deleted; the volume was kept on purpose and removing it is the operator's call. */
  orphan: boolean
}

/** A secret kept on the server as GET /secrets lists it: its name and dates, never its value. */
export interface Secret {
  name: string
  created_at: string
  updated_at: string
}

/** Body of PUT /secrets/:name, for creating and replacing alike. */
export interface SetSecretRequest {
  value: string
}

/** A stored API token as GET /tokens lists it: never its value or hash. */
export interface Token {
  id: number
  name: string
  role: Role
  created_at: string
  /** Kept to the minute; null until first used. */
  last_used_at: string | null
  /** The applications a deploy token may change, sorted; [] means all. Absent on agents before 0.6. */
  applications?: string[]
  /** Null for a token that does not expire; one whose time has passed stays listed until it is revoked. Absent on agents before 0.6. */
  expires_at?: string | null
}

/** Both optional fields are left out when unused: an agent before 0.6 refuses a field it does not know. */
export interface CreateTokenRequest {
  name: string
  role: Role
  /** Only with the deploy role; at most 50. */
  applications?: string[]
  /** RFC 3339, in the future. */
  expires_at?: string
}

/**
 * Body of PUT /tokens/:name: only what changes. `applications: []` lifts the
 * limit; `never_expires` takes the end away and cannot stand next to
 * `expires_at`. The role, the name and the value never change.
 */
export interface UpdateTokenRequest {
  applications?: string[]
  /** RFC 3339, in the future; a token that has expired works again. */
  expires_at?: string
  never_expires?: true
}

/** The answer to POST /tokens: the one time the token value itself is shown. */
export interface CreatedToken {
  id: number
  name: string
  role: Role
  created_at: string
  token: string
  applications?: string[]
  expires_at?: string | null
}

export interface Actor {
  kind: ActorKind | string
  name: string
}

export type AuditOutcome = 'ok' | 'refused' | 'failed'

/** One request that changed something, or tried to: GET /audit, newest first. */
export interface AuditEntry {
  id: number
  at: string
  actor: Actor
  /** The address the agent saw: the dashboard's, for what was done here. */
  address: string
  /** What the proxy in front said the request came from; "" without one. */
  forwarded_for: string
  /** deploy, rollback, token.create, secret.set, … */
  action: string
  /** "" for what is not about one application. */
  application: string
  /** The token, secret, registry, hostname, volume, job or backup the action names; "" otherwise. */
  target: string
  /** ok: answered 2xx (for background work: accepted). refused: 403. failed: anything else. */
  outcome: AuditOutcome | string
  status: number
  /** The error code of a refusal or failure. */
  code: string
  /** "deployment 12", "run 3", "role deploy, limited to a b", … */
  detail: string
}

/**
 * GET /audit as it is answered: `more` stands next to `data`, not inside it.
 * Absent on agents before 0.7, where a full page is the only hint of another.
 */
export interface AuditPage {
  data: AuditEntry[]
  more?: boolean
}

export type AuditExportFormat = 'csv' | 'json'

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

/** Accepted values of `since` on the traffic endpoint: the same three as the metrics history. */
export type TrafficRange = MetricsRange

/** The requests of one stretch of time. The percentiles are 0 when there were no requests. */
export interface TrafficCounts {
  requests: number
  status_2xx: number
  status_3xx: number
  status_4xx: number
  status_5xx: number
  /** Response bodies as sent, after compression. */
  bytes: number
  p50_ms: number
  p95_ms: number
  p99_ms: number
}

export interface TrafficPoint extends TrafficCounts {
  /** Start of the step. */
  t: string
}

/** GET /applications/:name/traffic: what the proxy's access log says about the application's requests. */
export interface Traffic {
  application: string
  /** Start of the window, on a step boundary. */
  since: string
  /** 60, 300 or 3600. */
  step_seconds: number
  totals: TrafficCounts
  /** Ordered by time. Sparse: a step without a request has no point, and a missing point is zero. */
  points: TrafficPoint[]
}

/** One line of the proxy's access log. The path carries no query string, and no headers are kept. */
export interface RequestLine {
  time: string
  method: string
  path: string
  status: number
  duration_ms: number
  bytes: number
  /** The address the proxy saw. */
  client: string
}

/** A stored registry credential as GET /registries lists it: never the password. */
export interface Registry {
  /** A hostname with an optional port, as image references name it. */
  registry: string
  username: string
  created_at: string
  updated_at: string
}

/** Body of PUT /registries/:registry, for creating and replacing alike. */
export interface SetRegistryRequest {
  username: string
  password: string
}

/** The answer to POST /server/rotate-key. */
export interface KeyRotation {
  /** Re-encrypted secrets and registry passwords. */
  values: number
  /** Deployment records whose values were re-encrypted. */
  deployments: number
  /** `environment`: the key is set in the agent's environment, which the agent cannot change. */
  key_source: 'file' | 'environment' | string
  /** The file on the server that holds the new key. */
  key_file: string
  /** The new key, for `environment` only: this response is the one time the API shows it. */
  key?: string
}

/** A certificate the operator supplied, as GET /certificates lists it: what the chain says about itself, never the key or the PEM. */
export interface Certificate {
  /** The name it is stored under; a wildcard certificate under the wildcard. */
  hostname: string
  /** The DNS names of the chain's first certificate: the hostnames served with it. */
  subjects: string[]
  issuer: string
  not_before: string
  not_after: string
  created_at: string
  updated_at: string
}

/** Body of PUT /certificates/:hostname: the chain, the hostname's own certificate first, and its key, both PEM. */
export interface SetCertificateRequest {
  certificate: string
  key: string
}

/** The answer to POST /validate and POST /applications/:name/validate for a document a deployment would accept. */
export interface Validation {
  valid: boolean
}

export type BackupRunStatus = 'running' | 'succeeded' | 'failed'

/** One archive of a backup: a volume, or for the agent's state one of its two files. */
export interface BackupVolume {
  volume: string
  /** Of the tar archive, before encryption. */
  size_bytes: number
}

/**
 * One backup of an application's volumes, or of the agent's state. Poll it
 * until `completed_at` is set; a verification or a restore of it until
 * `activity` is empty again.
 */
export interface BackupRun {
  id: number
  /** adopted: found in the backup destination and recorded afterwards; how it was taken is not known. */
  trigger: 'schedule' | 'manual' | 'adopted' | string
  status: BackupRunStatus
  started_at: string
  completed_at: string | null
  /** [] while running and when failed. */
  volumes: BackupVolume[]
  /** Of "local" and "s3"; [] while running and when failed. */
  destinations: string[]
  encrypted: boolean
  error: string
  /** "verify" or "restore" while one is in progress, else "". */
  activity: '' | 'verify' | 'restore' | string
  /** When the backup last proved to restore; at most one of this and `verify_error` is set. */
  verified_at: string | null
  verify_error: string
  restored_at: string | null
  restore_error: string
}

export interface BackupRunDetail extends BackupRun {
  /** The last 200 lines (64 KB) the verification's container wrote; "" if never verified. */
  verify_output: string
}

export type ImportStatus = 'running' | 'succeeded' | 'failed'
export type ImportedStatus = 'pending' | 'importing' | 'imported' | 'skipped' | 'failed'

/** One application of an import. */
export interface ImportedApplication {
  name: string
  status: ImportedStatus | string
  version: string
  deployment_id: number | null
  /** Restored before the application first started. */
  volumes: string[]
  /** Why it was skipped or failed, and what to do about it; "" otherwise. */
  message: string
}

/**
 * The import a server is running, or ran last: GET /import. It is kept in the
 * agent's memory, so an agent that restarts has forgotten it (404 NOT_FOUND).
 * Poll it until `completed_at` is set.
 */
export interface Import {
  status: ImportStatus | string
  /** "upload", or "export #<id> from the bucket" for one a standby fetched. */
  source: string
  /** Deployed stopped, as a standby holds applications. */
  stopped: boolean
  overwrite: boolean
  started_at: string
  completed_at: string | null
  /** When the export was written; null until it has been read. */
  exported_at: string | null
  secrets: number
  registries: number
  certificates: number
  /** In the order they are deployed. */
  applications: ImportedApplication[]
  /** What was kept as it was, or could not be taken over. */
  warnings: string[]
  error: string
}

/** A record to create or change so that a hostname reaches this server. */
export interface DNSRecord {
  hostname: string
  type: 'A' | 'AAAA' | string
  /** "" when the agent does not know its own address. */
  value: string
}

export interface StandbyApplication {
  name: string
  version: string
  hostnames: string[]
  imported_at: string
}

/** How the scheduled import from the bucket is doing. */
export interface StandbyPull {
  /** Five cron fields, read in UTC. */
  schedule: string
  last_at: string | null
  /** The export imported last; 0 when there has been none. */
  last_export: number
  last_error: string
}

/** GET /standby: what a server holds for the day it has to take over. */
export interface Standby {
  /** Imported stopped, waiting for a promotion. */
  applications: StandbyApplication[]
  /** What a promotion will ask for. */
  records: DNSRecord[]
  /** Null when the agent does not fetch exports on a schedule. */
  pull: StandbyPull | null
  /** The promotion that runs or ran last; null on a server that was never promoted. Absent on agents before 0.6. */
  promotion?: Promotion | null
}

export interface PromotedApplication {
  name: string
  /**
   * pending: its turn has not come. starting: being started. running: started
   * and ready. started: not ready within its startup budget. failed: could not
   * be started.
   */
  status: 'pending' | 'starting' | 'running' | 'started' | 'failed' | string
  message: string
}

export type PromotionStatus = 'running' | 'succeeded' | 'failed'

/**
 * A promotion: POST /standby/promote?wait=false begins one (202) and
 * GET /standby/promotion follows it until `completed_at` is set. An agent
 * before 0.6 answers the POST when everything has been started, with
 * `applications` and `records` only.
 */
export interface Promotion {
  applications: PromotedApplication[]
  records: DNSRecord[]
  /** Counts the server's promotions from 1; 0 when there was nothing to promote. */
  id?: number
  /** failed: at least one application could not be started. */
  status?: PromotionStatus | string
  started_at?: string
  completed_at?: string | null
}

export type BackupKind = 'application' | 'state' | 'export'

/** A backup found in the backup destination and recorded. */
export interface AdoptedBackup {
  kind: BackupKind | string
  /** "" unless `kind` is application. */
  application: string
  backup: BackupRun
}

/** A backup that was found and left alone, with the reason. */
export interface SkippedBackup {
  kind: BackupKind | string
  application: string
  id: number
  reason: string
}

/** The answer of POST /server/backups/adopt; both lists are always arrays. */
export interface BackupAdoption {
  adopted: AdoptedBackup[]
  skipped: SkippedBackup[]
}

/** Body of POST /server/backups/adopt; without an application, everything that is found. */
export interface BackupAdoptRequest {
  application?: string
}

/** Body of POST /export: the passphrase encrypts the file and is kept nowhere. */
export interface ExportRequest {
  passphrase: string
  /** Limits the export to these; left out for every application. */
  applications?: string[]
}

/**
 * GET /applications/:name/config: the deploy.yaml of the active deployment,
 * for those who may deploy the application. A value that referred to a stored
 * secret comes back as that reference; every other env value and basic-auth
 * password is "********", and a document that still holds one is refused.
 */
export interface ApplicationConfig {
  application: string
  deployment_id: number
  sequence: number
  version: string
  /** YAML text, in the order and style `shipwick init` writes. */
  document: string
  /** The fields whose value is the mask, in document order: `env.<NAME>`, `proxy.basic_auth[<i>].password`. Always an array. */
  masked: string[]
  /** Only for a static application: what POST /applications?static= takes to serve the same folder again. */
  static_digest?: string
}

export type LogKind = 'replica' | 'run'

/** The output of one container run that ended, as the archive lists it: without its lines. */
export interface LogArchiveEntry {
  id: number
  application: string
  kind: LogKind | string
  /** Null once the deployment is gone from the history. */
  deployment_id: number | null
  /** The deployment's sequence (#N); 0 when it is gone. */
  deployment: number
  /** "" when the deployment is gone. */
  version: string
  /** 0 for a run. */
  replica: number
  /** "" for a replica; "pre-deploy" and "run" included. */
  job: string
  run_id: number | null
  container: string
  /**
   * For a replica: crashed, exited, oom_killed, unhealthy, stopped, replaced,
   * deployment_failed, removed, restarted. For a run its status: succeeded,
   * failed, timed_out, interrupted.
   */
  reason: string
  /** Null when the container was still running when it was removed, or its exit was not seen. */
  exit_code: number | null
  oom_killed: boolean
  ended_at: string
  /** Null when `lines` is 0. */
  first_line_at: string | null
  last_line_at: string | null
  lines: number
  /** Size of the text kept. */
  bytes: number
  /** On disk, compressed. */
  stored_bytes: number
  /** The run printed more; these are its last lines. */
  truncated: boolean
}

/** GET …/logs/archive/:id: the entry with its lines, oldest first. */
export interface LogArchiveDetail extends LogArchiveEntry {
  output: LogLine[]
}

/** One line a search found, with where it was read from. */
export interface LogMatch extends LogLine {
  /** Null for a line read from a container that exists. */
  archive_id: number | null
  deployment_id: number | null
  deployment: number
  job: string
  run_id: number | null
}

/**
 * One page of GET …/logs/search. Ordered by source, newest first, and within
 * a source newest line first. A page may hold fewer lines than asked for, even
 * none, and still name a `next`: ask again with `cursor` until it is "".
 */
export interface LogSearchResult {
  lines: LogMatch[]
  next: string
  /** Containers whose output this request read. */
  sources: number
  /** Bytes of output this request read. */
  bytes: number
}

/** The `log_archive` object of GET /server. */
export interface LogArchiveStatus {
  /** False when SHIPWICK_LOG_RETENTION_SIZE is 0: nothing is kept. */
  enabled: boolean
  entries: number
  bytes: number
  max_bytes: number
  retention_days: number
}

/** The `update` object of GET /server: what the agent heard about newer releases. The dashboard never asks itself. */
export interface UpdateStatus {
  /** False when SHIPWICK_UPDATE_CHECK is off. */
  enabled: boolean
  /** A tag with its v, never a pre-release; "" until the question was answered once. */
  latest_version: string
  /** When it was last answered; a failed attempt does not move it. */
  checked_at: string | null
  /** `latest_version` is newer than the agent's own. */
  available: boolean
}
