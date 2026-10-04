// A stand-in for the Shipwick agent, for developing and testing the dashboard.
//
//   npm run mock            → http://127.0.0.1:9100, token mock-token-0123456789abcdef
//
// It lets the UI be developed without Docker or a real agent. It implements the
// API described in docs/api.md, has no dependencies, keeps its state in memory,
// and makes that state move: deployments progress, a replica keeps
// crash-looping, logs flow, metrics wander.
//
// Deployments roll, like the real agent's: one replica at a time, each new one
// taking over from its predecessor. POST …/deploy and …/validate take the
// deploy.yaml as the agent does, as YAML or JSON (mock/yaml.mjs reads the part
// of YAML such a file is written in), validate the hostnames, ports and health
// block, and refuse a hostname or server port another application holds with
// the agent's INVALID_CONFIG shape.
//
// Also served, with the agent's shapes: tokens (GET/POST /tokens, DELETE
// /tokens/:name; a token created here signs in with its role), roles (403
// FORBIDDEN with {role, required}), `by` on deployments, volumes and their
// archives (GET streams a small real tar; PUT needs a stopped application),
// metrics history (since=1h|24h|7d with the agent's step, sparse series with
// gaps, limits), `token` and `notifications` on GET /server, and jobs: GET
// …/jobs (last and next run, cron read in UTC), GET …/runs?job=&limit=, GET
// …/runs/:id with output, POST …/jobs/:job/run (202; a run finishes by itself
// after a few seconds, 409 JOB_ALREADY_RUNNING while one is going) and POST
// …/run {"command": [...]}. A run whose command mentions "fail" fails with exit
// 1, "timeout" times out, anything else succeeds; failures add a `job` event.
// A deployment with `pre_deploy` records the two hook steps and a hook run.
//
// Also: secrets (GET /secrets, PUT and DELETE /secrets/:name; a deploy whose
// env refers to `${NAME}` of a secret that is not stored is refused with the
// agent's INVALID_CONFIG shape), volumes of the whole server (GET /volumes
// with `orphan` for a deleted application's, DELETE /volumes/:name → 204, 409
// VOLUME_IN_USE, 404), static applications (PUT …/static takes a tar archive
// and answers its digest; POST …/deploy?static=<digest> deploys a `static`
// spec; logs, metrics, jobs and run answer 409 STATIC_APPLICATION), images
// built by the CLI (POST …/images → 201; a `build` spec needs a
// shipwick.local/ image), and 429 RATE_LIMITED after 20 failed
// authentications within a minute from one address.
//
// And what 0.5 added: `path`, `proxy`, `backups`, `deploy.stop_timeout` and
// `static.fallback` in specs (basic-auth passwords masked; hostname conflicts
// are by hostname and path; a wildcard hostname needs a supplied certificate
// or the DNS challenge), POST …/validate, `certificates` on every application
// detail, traffic (GET …/traffic?since=1h|24h|7d, sparse points; GET
// …/requests?tail=), registries (GET /registries, PUT and DELETE
// /registries/:registry; a registry named refused.* refuses the login, one
// under .invalid cannot be asked), POST /server/rotate-key, supplied
// certificates (GET /certificates, PUT and DELETE /certificates/:hostname;
// the PEM is looked at, not parsed), backups (the ten endpoints: a backup
// runs for a few seconds, a verification and a restore are followed through
// `activity`), and `dashboard_url`, `alerts`, `disk`, `backups` and
// `proxy.dns_challenge` on GET /server; and export, import and the standby
// (POST /export answers a small file, POST /exports writes one to the backups,
// GET /exports lists them, POST and GET /import, GET /standby, POST
// /standby/pull and /standby/promote). A replica replaced by a deployment
// stays listed for a few seconds after the deployment completed, as it does
// while the real one is given its stop_timeout.
//
// And what 0.6 added: `init` and `backups.before_timeout` in specs,
// `stopping` on containers, `certificate_problem`, `alert_count` and
// `alert_severity` on applications, `network` and `sign_in` on GET /server;
// tokens limited to applications and tokens that expire (403 TOKEN_LIMITED,
// 401 TOKEN_EXPIRED), the audit trail (GET /audit, written for every request
// that changes something), POST /server/backups/adopt, the promotion that is
// started and followed (POST /standby/promote?wait=false, GET
// /standby/promotion), and signing in through a provider: GET /auth, POST
// /auth/exchange, sessions (`sws_…`), the /access endpoints, and a stand-in
// for the provider itself at /mock-idp/authorize.
//
// And what 0.7 added: the log archive (GET …/logs/archive, …/logs/archive/:id
// and …/logs/search; an entry is written when a replica crashes, is restarted,
// stopped or replaced, when a deployment fails and when a run ends), GET
// …/config with the deploy.yaml of what runs (mock/document.mjs; references
// to stored secrets come back, every other secret value is the mask, and a
// document that carries the mask is refused), POST /applications and POST
// /validate for a document whose name is not in the address,
// `backups.before_in`, PUT /tokens/:name, the audit trail's `action`,
// `outcome` and `actor_kind` filters with `more` next to `data`, GET
// /audit/export, rules of kind `name` and `sign_in.name_claim`, `update` and
// `log_archive` on GET /server, and an export that is streamed, with its
// error trailer.
//
// Magic image tags for POST /applications/:name/redeploy {"image": ...}:
//   *:fail      replica 1 crashes: FAILED, nothing of the old version was touched
//   *:rollback  replica 1 is replaced, replica 2 crashes: FAILED → ROLLBACK →
//               RESTORING → ROLLED_BACK (needs an application with 2+ replicas;
//               with one replica it behaves like *:fail)
//   *:local     the pull fails but a local copy exists (a `warn` step)
//   *:hookfail  the pre-deploy command exits 1: FAILED with its output as a
//               `log` event, no replica touched (needs an app with pre_deploy)
//   *:restart   the agent goes away for a few seconds while replica 1 is
//               checked (connections are dropped, as when it restarts), then
//               resumes the deployment: "Resumed after the agent restarted"
//   *:sigterm   the replaced replica ignores SIGTERM: it stays listed for its
//               whole grace period and is killed, with the `warn` event
//
// Environment:
//   MOCK_PORT (9100), MOCK_HOST (127.0.0.1), MOCK_TOKEN
//   MOCK_ROLE=read|deploy|admin  the role of MOCK_TOKEN (default admin, as the
//                    root token); endpoints above it answer 403 FORBIDDEN
//   MOCK_WEBHOOK=1   server.notifications.webhook is true
//   MOCK_NO_PROXY=1  server.proxy.enabled is false, and deploying an application
//                    with a domain produces the "No reverse proxy" warn step;
//                    traffic is 409 TRAFFIC_UNAVAILABLE, certificates `unknown`
//   MOCK_NO_TRAFFIC=1    traffic and requests answer 409 TRAFFIC_UNAVAILABLE
//   MOCK_ALERTS=none|warning|critical   the alerts of GET /server: none, two
//                    warnings (the default), or those plus a critical disk
//                    and a critical unhealthy alert
//   MOCK_KEY_ENV=1   the encryption key is "in the environment": rotate-key
//                    answers the new key once, then 409 KEY_ROTATION_PENDING
//   MOCK_NO_PASSPHRASE=1 backups are not encrypted and the agent's state is
//                    not backed up: POST /server/backups is 409
//                    BACKUPS_NOT_ENCRYPTED
//   MOCK_NO_BUCKET=1 backups stay on the server's disk (`destination: local`)
//   MOCK_VERIFY_FAILS=1  a backup verification ends with `verify_error`
//   MOCK_DNS_CHALLENGE=1 server.proxy.dns_challenge is true, and wildcard
//                    hostnames deploy without a supplied certificate
//   MOCK_STANDBY=1   this server is a standby: postgres, shop and docs were
//                    imported stopped and wait for POST /standby/promote, and
//                    exports are fetched on a schedule (POST /standby/pull
//                    imports one over a few seconds; follow it with GET /import)
//   MOCK_OLD_AGENT=1 answers like an agent before 0.5: the new endpoints are
//                    404 ENDPOINT_NOT_FOUND and the new fields are absent
//   MOCK_AGENT=0.5   answers like an agent before 0.6 (MOCK_OLD_AGENT=1 does too)
//   MOCK_LIMIT=a,b   MOCK_TOKEN is limited to these applications (with
//                    MOCK_ROLE=deploy): changing another is 403 TOKEN_LIMITED
//   MOCK_EXPIRES=9d  MOCK_TOKEN expires that long after the mock started (s, m,
//                    h or d); `expired` for one that has: 401 TOKEN_EXPIRED
//   MOCK_NETWORK=1   the agent is behind a proxy the Docker daemon lacks, with
//                    authorities of its own, the system's resolver and an ACME
//                    directory of its own: `network` on GET /server
//   MOCK_ADOPT=foreign   POST /server/backups/adopt answers 409 FOREIGN_BUCKET
//   MOCK_PROMOTING=1 (with MOCK_STANDBY=1) a promotion is running when the mock
//                    starts, 20 seconds an application
//   MOCK_NO_SIGN_IN=1    no sign-in provider: GET /auth says configured false
//   MOCK_DASHBOARD_URL   where the dashboard is, for the sign-in's redirect_uri
//                    (default http://localhost:3000)
//   MOCK_IDP_USER=ada@example.com   the stand-in provider signs this address in
//                    without showing its page of accounts
//   MOCK_HOSTNAME    the server's hostname (default shipwick-fsn1-01)
//   MOCK_AGENT=0.6   answers like an agent before 0.7: what it added is 404
//                    ENDPOINT_NOT_FOUND, its fields are absent and its filters are
//                    ignored
//   MOCK_UPDATE=available|current|unknown|off   `update` on GET /server: a
//                    newer release (the default), none, GitHub never answered,
//                    or SHIPWICK_UPDATE_CHECK=off
//   MOCK_LOG_ARCHIVE=off   SHIPWICK_LOG_RETENTION_SIZE=0: nothing is kept
//   MOCK_NAME_CLAIM=preferred_username   people are named by that claim
//                    instead of their address: rules of kind `name`
//   MOCK_EXPORT_MB=64    the size of the file POST /export streams (default
//                    a quarter of a megabyte)
//   MOCK_EXPORT=trailer|breaks   the export stops half-way: with the agent's
//                    error trailer, or with the connection cut
//   MOCK_IMPORT_MS=1500  how long an import takes per application

import { createServer } from 'node:http'
import { createHash, randomBytes } from 'node:crypto'
import { parseYaml } from './yaml.mjs'
import { documentOf, maskedFields, referencesOf } from './document.mjs'

const PORT = Number(process.env.MOCK_PORT || 9100)
const HOST = process.env.MOCK_HOST || '127.0.0.1'
const TOKEN = process.env.MOCK_TOKEN || 'mock-token-0123456789abcdef'
const PROXY_ENABLED = process.env.MOCK_NO_PROXY !== '1'
const WEBHOOK = process.env.MOCK_WEBHOOK === '1'
const ROLES = ['read', 'deploy', 'admin']
const ROLE = ROLES.includes(process.env.MOCK_ROLE) ? process.env.MOCK_ROLE : 'admin'
// The configured token is the root token when it is admin; a lesser role gets a plausible name.
const TOKEN_NAME_OF_ROLE = ROLE === 'admin' ? 'root' : ROLE === 'deploy' ? 'ci' : 'viewer'
// Hostnames and server ports Shipwick itself holds: the dashboard's route, the proxy's and the agent's ports.
const OWN_HOSTNAMES = ['shipwick.example.com']
const RESERVED_PORTS = [80, 443, 8080, 8443, 9000]
const LOG_BUFFER = 5000
const MASK = '********'
const OLD_AGENT = process.env.MOCK_OLD_AGENT === '1'
// An agent before 0.6: no audit trail, no limits or expiry on tokens, no promotion record, no sign-in.
const BEFORE_06 = OLD_AGENT || process.env.MOCK_AGENT === '0.5'
// An agent before 0.7: no log archive, no document to edit, no token update, no update notice.
const BEFORE_07 = BEFORE_06 || process.env.MOCK_AGENT === '0.6'
const VERSION = OLD_AGENT ? 'v0.4.2' : BEFORE_06 ? 'v0.5.1' : BEFORE_07 ? 'v0.6.0' : 'v0.7.0'
const UPDATE = ['available', 'current', 'unknown', 'off'].includes(process.env.MOCK_UPDATE) ? process.env.MOCK_UPDATE : 'available'
const LOG_ARCHIVE = process.env.MOCK_LOG_ARCHIVE !== 'off'
// The claim people are named by; anything but `email` names them by what stands before the @ of the stand-in provider's accounts.
const NAME_CLAIM = /^[a-z_]{1,40}$/.test(process.env.MOCK_NAME_CLAIM ?? '') ? process.env.MOCK_NAME_CLAIM : 'email'
const EXPORT_BYTES = (Number(process.env.MOCK_EXPORT_MB) > 0 ? Number(process.env.MOCK_EXPORT_MB) : 0.25) * 1024 * 1024
const EXPORT_FAILS = ['trailer', 'breaks'].includes(process.env.MOCK_EXPORT) ? process.env.MOCK_EXPORT : ''
const IMPORT_MS = Number(process.env.MOCK_IMPORT_MS) || 1500
const TRAFFIC_AVAILABLE = PROXY_ENABLED && process.env.MOCK_NO_TRAFFIC !== '1'
const ALERTS = ['none', 'warning', 'critical'].includes(process.env.MOCK_ALERTS) ? process.env.MOCK_ALERTS : 'warning'
const KEY_FROM_ENVIRONMENT = process.env.MOCK_KEY_ENV === '1'
const PASSPHRASE = process.env.MOCK_NO_PASSPHRASE !== '1'
const BUCKET = process.env.MOCK_NO_BUCKET !== '1'
const VERIFY_FAILS = process.env.MOCK_VERIFY_FAILS === '1'
const DNS_CHALLENGE = PROXY_ENABLED && process.env.MOCK_DNS_CHALLENGE === '1'
const IS_STANDBY = process.env.MOCK_STANDBY === '1'
// A server nobody has deployed to yet: no applications, no deployments, nothing to alert about.
const EMPTY = process.env.MOCK_EMPTY === '1'
const DASHBOARD_URL =`https://${OWN_HOSTNAMES[0]}`
const SERVER_HOSTNAME = process.env.MOCK_HOSTNAME || 'shipwick-fsn1-01'
const TOKEN_LIMIT = (process.env.MOCK_LIMIT ?? '').split(',').map(n => n.trim()).filter(Boolean).sort()
const NETWORK = process.env.MOCK_NETWORK === '1'
const ADOPT_FOREIGN = process.env.MOCK_ADOPT === 'foreign'
const PROMOTING = process.env.MOCK_PROMOTING === '1'
const SIGN_IN = process.env.MOCK_NO_SIGN_IN !== '1'
const SIGN_IN_REDIRECT = `${(process.env.MOCK_DASHBOARD_URL || 'http://localhost:3000').replace(/\/+$/, '')}/auth/callback`
const IDP_USER = process.env.MOCK_IDP_USER || ''
const SERVER_ADDRESS = '203.0.113.10'
const DATA_DIR = '/var/lib/shipwick'

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

const startedAt = Date.now()
const iso = (ms) => new Date(ms).toISOString()
const ago = (ms) => iso(startedAt - ms)

/** When MOCK_TOKEN expires: MOCK_EXPIRES as a span from the start, `expired` for an hour ago, nothing for never. */
function configuredExpiry() {
  const raw = process.env.MOCK_EXPIRES ?? ''
  if (raw === 'expired') return ago(60 * 60 * 1000)
  const m = /^(\d+)(s|m|h|d)$/.exec(raw)
  if (!m) return null
  return iso(startedAt + Number(m[1]) * { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 }[m[2]])
}
const TOKEN_EXPIRES_AT = configuredExpiry()

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

/** @type {Map<string, any>} */
const apps = new Map()
/** @type {Map<number, any>} */
const deployments = new Map()
/** name → events, newest last */
const appEvents = new Map()
/** name → log lines, oldest first */
const logBuffers = new Map()
/** name → Set of follower callbacks {onLine, onEnd} */
const followers = new Map()
/** container name → {cpu, mem} random-walk state */
const metricState = new Map()
/** name → stored token {id, name, role, created_at, last_used_at, value}; the value stays here only so it can sign in */
const tokens = new Map()
/** run id → run of a job, hook or one-off command, with its output */
const runs = new Map()
/** `${app}/${job}` → the minute the schedule was last looked at */
const scheduleSeen = new Map()
/** secret name → {created_at, updated_at}; the value is not kept, the API never returns it */
const secrets = new Map()
/** application name → the last static upload {digest, size_bytes, files}, one per application like the agent */
const uploads = new Map()
/** Volumes of deleted applications: kept on purpose, listed by GET /volumes with orphan: true */
const orphanVolumes = []
/** remote address → times of failed authentications within the last minute */
const authFailures = new Map()
/** registry → {username, created_at, updated_at}; the password is not kept, the API never returns it */
const registries = new Map()
/** hostname → what a supplied certificate says about itself; neither the PEM nor the key is kept */
const certificates = new Map()
/** backup id → run, of an application's volumes or (application STATE) of the agent's own state; one sequence for both, as in the agent's table */
const backups = new Map()
/** ids of containers a finished deployment replaced and that are still being given their grace period */
const draining = new Set()
/** The key was rotated while it is "in the environment": the next rotation is refused until a restart. */
let rotationPending = false
/** While in the future the mock drops every connection, as an agent that is restarting does. */
let awayUntil = 0

let nextDeploymentId = 1
let nextEventId = 1
let nextTokenId = 1
let nextRunId = 1
let nextBackupId = 1
let nextAuditId = 1
let nextRuleId = 1
let nextSessionId = 1
/** The audit trail, oldest first. */
const audit = []
/** Who may sign in: rules {id, kind, subject, role, applications, created_at, created_by}, oldest first. */
const accessRules = []
/** session value → a person's session; the value stays here only so it can be presented. */
const sessions = new Map()
/** code → what the stand-in provider remembers of an authorization request until the code is redeemed. */
const idpCodes = new Map()
/** Nonces of sign-ins that completed: each is accepted once. */
const usedNonces = new Set()
/** deployment id → the secret values its document wrote as references: {env: {NAME: text}, basic_auth: {index: text}} */
const references = new Map()
/** The log archive: the output of container runs that ended, oldest first, each with its lines. */
const logArchive = []
/** container name → the time of the last line an entry already holds: the next entry of that container starts after it. */
const archivedUntil = new Map()
let nextArchiveId = 1

function spec(name, image, extra = {}) {
  return {
    name,
    image,
    replicas: 1,
    resources: {},
    restart: { policy: 'always' },
    deploy: { strategy: 'rolling' },
    ...extra,
  }
}

function tagOf(image) {
  const at = image.indexOf('@')
  const ref = at === -1 ? image : image.slice(0, at)
  const colon = ref.lastIndexOf(':')
  return colon === -1 || colon < ref.lastIndexOf('/') ? 'latest' : ref.slice(colon + 1)
}

function addApp(name, createdAt) {
  const app = { name, desired_state: 'running', active_deployment_id: null, created_at: createdAt, updated_at: createdAt, containers: [] }
  apps.set(name, app)
  appEvents.set(name, [])
  logBuffers.set(name, [])
  return app
}

// `by` is the token that started the deployment; null leaves it out, as the
// agent does for deployments recorded before tokens had names. `files` is the
// upload a static deployment serves: its version is the digest's first twelve
// hex characters and it has no image.
function addDeployment(app, sp, { status, startedAtMs, durationMs, error = '', events = [], kind = 'deploy', sourceId = null, by = 'root', files = null }) {
  const id = nextDeploymentId++
  const sequence = [...deployments.values()].filter(d => d.application === app.name).length + 1
  const d = {
    id,
    application: app.name,
    sequence,
    version: files ? files.digest.slice('sha256:'.length, 'sha256:'.length + 12) : tagOf(sp.image),
    image: files ? '' : sp.image,
    status,
    error,
    started_at: iso(startedAtMs),
    completed_at: durationMs === null ? null : iso(startedAtMs + durationMs),
    kind,
    source_deployment_id: sourceId,
    ...(by ? { by } : {}),
    ...(files ? { static: { digest: files.digest, size_bytes: files.size_bytes, files: files.files } } : {}),
    spec: structuredClone(sp),
    events: [],
  }
  let t = startedAtMs
  for (const [type, message, level = 'info', dt = 600] of events) {
    t += dt
    d.events.push({ id: nextEventId++, deployment_id: id, level, type, message, created_at: iso(t) })
  }
  deployments.set(id, d)
  return d
}

// The agent's wording (agent/internal/deploy/rollout.go), so the UI is
// developed against the sentences it will really show.
const plural = (n, word) => `${n} ${n === 1 ? word : `${word}s`}`
const replicaList = list => (list.length === 1 ? `Replica ${list[0]}` : `Replicas ${list.slice(0, -1).join(', ')} and ${list[list.length - 1]}`)
const readyMessage = (sp, list) => (sp.health ? `${replicaList(list)} passed health checks` : `${replicaList(list)} running and stable`)
const servingMessage = (i, n, version, previousVersion) => `Replica ${i}/${n} is serving ${version}; its ${previousVersion} predecessor is retired`
const routedMessage = (sp, to = plural(sp.replicas, 'replica')) => (PROXY_ENABLED
  ? ['step', `Routed https://${sp.domain}${sp.path ?? ''} to ${to}`, 'info']
  : ['step', `No reverse proxy is configured, so ${sp.domain} is not being served. Set SHIPWICK_CADDY_ADMIN on the agent`, 'warn'])
// An image under shipwick.local/ was built by the CLI and sent here; the agent never pulls it.
const LOCAL_IMAGE_PREFIX = 'shipwick.local/'
const isLocalImage = image => image.startsWith(LOCAL_IMAGE_PREFIX)
const pulledMessage = sp => (isLocalImage(sp.image) ? `Using image ${sp.image}, sent from a developer's machine` : `Pulled image ${sp.image}`)

/** Events of a finished successful deployment, for fixtures. Rolling when there was a previous version. */
function successEvents(sp, previousVersion) {
  const n = sp.replicas
  const version = tagOf(sp.image)
  const events = [
    ['state', 'BUILDING', 'info', 50],
    ['step', pulledMessage(sp), 'info', 1900],
    // The hook runs after the pull and before any replica is touched.
    ...(sp.pre_deploy ? [['step', 'Running pre-deploy command', 'info', 200], ['step', 'Pre-deploy command finished (12s)', 'info', 11800]] : []),
    ['state', 'STARTING', 'info', 20],
  ]
  if (!previousVersion) {
    // Nothing to replace: all replicas start together as one batch.
    events.push(['step', `Started ${plural(n, 'container')}`, 'info', 700], ['state', 'HEALTH_CHECKING', 'info', 20])
    events.push(['step', readyMessage(sp, Array.from({ length: n }, (_, i) => i + 1)), 'info', 1800])
  }
  else {
    for (let i = 1; i <= n; i++) {
      events.push(['step', 'Started 1 container', 'info', 600])
      if (i === 1) events.push(['state', 'HEALTH_CHECKING', 'info', 20])
      events.push(['step', readyMessage(sp, [i]), 'info', 1100])
      events.push(['step', servingMessage(i, n, version, previousVersion), 'info', 400])
    }
  }
  events.push(['state', 'HEALTHY', 'info', 20])
  if (sp.domain) events.push([...routedMessage(sp), 120])
  events.push(['state', 'ACTIVE', 'info', 40], ['step', 'Deployment successful', 'info', 30])
  return events
}

/**
 * Events of a finished static deployment: the folder is copied into the proxy
 * (or found there already, on a redeploy or rollback), checked for index.html
 * and routed. The agent's wording (agent/internal/deploy/static.go).
 */
function staticSuccessEvents(sp, files, alreadyThere = false, olderFolders = 0) {
  const what = `${plural(files.files, 'file')} (${formatSize(files.size_bytes)})`
  return [
    ['state', 'BUILDING', 'info', 50],
    ['step', alreadyThere ? `The proxy already has ${what}` : `Received ${what}`, 'info', 300],
    ['state', 'STARTING', 'info', 20],
    ...(alreadyThere ? [] : [['step', `Copied ${plural(files.files, 'file')} into the proxy`, 'info', 900]]),
    ['state', 'HEALTH_CHECKING', 'info', 20],
    ['step', 'Found index.html', 'info', 80],
    ...(sp.static?.fallback && sp.static.fallback !== 'index.html' ? [['step', `Found ${sp.static.fallback}, the fallback page`, 'info', 40]] : []),
    ['state', 'HEALTHY', 'info', 20],
    [...routedMessage(sp, 'the uploaded files'), 400],
    ['state', 'ACTIVE', 'info', 40],
    ...(olderFolders > 0 ? [['step', `Removed ${plural(olderFolders, 'folder')} of older versions`, 'info', 60]] : []),
    ['step', 'Deployment successful', 'info', 30],
  ]
}

/** A deployment whose first replica never came up: FAILED, nothing of the old version was touched. */
function failureEvents(sp, reason, output) {
  return [
    ['state', 'BUILDING', 'info', 50],
    ['step', pulledMessage(sp), 'info', 1700],
    ['state', 'STARTING', 'info', 20],
    ['step', 'Started 1 container', 'info', 600],
    ['state', 'HEALTH_CHECKING', 'info', 20],
    ...(output ? [['log', output, 'error', 1400]] : []),
    ['state', `FAILED: ${reason}`, 'error', 30],
  ]
}

/** A rollout that failed at replica `failedAt` after earlier ones were replaced: ends ROLLED_BACK. */
function rolledBackEvents(sp, previousVersion, failedAt, reason, output) {
  const n = sp.replicas
  const version = tagOf(sp.image)
  const events = [
    ['state', 'BUILDING', 'info', 50],
    ['step', pulledMessage(sp), 'info', 1800],
    ['state', 'STARTING', 'info', 20],
  ]
  for (let i = 1; i < failedAt; i++) {
    events.push(['step', 'Started 1 container', 'info', 600])
    if (i === 1) events.push(['state', 'HEALTH_CHECKING', 'info', 20])
    events.push(['step', readyMessage(sp, [i]), 'info', 1100])
    events.push(['step', servingMessage(i, n, version, previousVersion), 'info', 400])
  }
  events.push(
    ['step', 'Started 1 container', 'info', 600],
    ...(output ? [['log', output, 'error', 1500]] : []),
    ['state', `FAILED: ${reason}`, 'error', 30],
    ['state', 'ROLLBACK', 'info', 20],
    ['step', `Rolling back: restoring ${plural(failedAt - 1, 'replica')} of ${previousVersion}`, 'info', 60],
    ['state', 'RESTORING', 'info', 20],
    ['step', readyMessage(sp, Array.from({ length: failedAt - 1 }, (_, i) => i + 1)), 'info', 1900],
    ['step', `Rolled back: ${sp.name} is running ${previousVersion} again`, 'info', 500],
    ['state', 'ROLLED_BACK', 'info', 20],
  )
  return events
}

function makeContainers(app, d, overrides = {}) {
  const list = []
  for (let r = 1; r <= d.spec.replicas; r++) {
    const name = `shipwick_${app.name}_${d.sequence}_${r}`
    list.push({
      id: randomHex(64),
      name,
      deployment_id: d.id,
      replica: r,
      image: d.image,
      state: 'running',
      exit_code: 0,
      oom_killed: false,
      ip: `172.18.0.${10 + Math.floor(Math.random() * 200)}`,
      started_at: d.completed_at ?? iso(Date.now()),
      health: d.spec.health ? 'healthy' : '',
      restarts: 0,
      crash_loop: false,
      ...(overrides[r] ?? {}),
    })
  }
  return list
}

function randomHex(length) {
  let out = ''
  while (out.length < length) out += Math.floor(Math.random() * 0xFFFFFFFF).toString(16).padStart(8, '0')
  return out.slice(0, length)
}

function addAppEvent(name, level, type, message, atMs = Date.now(), deploymentId = null) {
  const list = appEvents.get(name)
  if (!list) return
  list.push({ id: nextEventId++, deployment_id: deploymentId, level, type, message, created_at: iso(atMs) })
  if (list.length > 500) list.splice(0, list.length - 500)
}

// ---------------------------------------------------------------------------
// Fixtures: one application per status
// ---------------------------------------------------------------------------

function seed() {
  // HEALTHY: two replicas, domain, health check, limits, a failed attempt in its history.
  {
    const app = addApp('my-api', ago(41 * DAY))
    const base = (tag, extra = {}) => spec('my-api', `ghcr.io/acme/my-api:${tag}`, {
      port: 8080,
      domain: 'api.example.com',
      replicas: 2,
      env: { DATABASE_URL: MASK, REDIS_URL: MASK, SENTRY_DSN: MASK, LOG_LEVEL: MASK },
      health: { path: '/health', interval: '10s', timeout: '3s', retries: 3 },
      resources: { cpu: 1, memory_bytes: 1024 ** 3 },
      pre_deploy: { command: ['dotnet', 'Migrate.dll'], timeout: '10m0s' },
      // What the proxy does besides passing requests on; the account's password is a stored secret, masked like every value.
      proxy: {
        headers: { 'Strict-Transport-Security': 'max-age=31536000', 'X-Frame-Options': 'DENY' },
        basic_auth: [{ path: '/admin', username: 'ops', password: MASK }],
        redirects: [{ from: '/docs', to: 'https://example.com/docs/', status: 308 }],
      },
      // In-flight requests get half a minute to finish when a replica is replaced or stopped.
      deploy: { strategy: 'rolling', stop_timeout: '30s' },
      ...(BEFORE_06 ? {} : { init: true }),
      jobs: [
        { name: 'nightly-report', schedule: '0 3 * * *', command: ['node', 'report.js'], timeout: '1h0m0s' },
        { name: 'cleanup-sessions', schedule: '*/15 * * * *', command: ['node', 'cleanup.js', '--older-than', '30d'], timeout: '5m0s' },
      ],
      ...extra,
    })
    // tag, age, previous version, kind, index (in this list) of the deployment whose configuration was re-used, token
    // The three oldest predate named tokens and carry no `by`.
    const history = [
      ['1.3.8', 41 * DAY, null, 'deploy', null, null],
      ['1.3.9', 27 * DAY, '1.3.8', 'deploy', null, null],
      ['1.4.0', 12 * DAY, '1.3.9', 'deploy', null, null],
      ['1.4.1', 8 * DAY, '1.4.0', 'deploy', null, 'ci'],
      // 1.4.1 misbehaved: rolled back to #3, then redeployed once the image had been rebuilt under the same tag.
      ['1.4.0', 8 * DAY - 3 * HOUR, '1.4.1', 'rollback', 2, 'root'],
      ['1.4.1', 5 * DAY, '1.4.0', 'redeploy', 4, 'ci'],
    ]
    const made = []
    for (const [tag, age, prev, kind, sourceIndex, by] of history) {
      const sp = base(tag)
      made.push(addDeployment(app, sp, {
        status: 'SUPERSEDED',
        startedAtMs: startedAt - age,
        durationMs: prev ? 7300 : 4600,
        events: successEvents(sp, prev),
        kind,
        sourceId: sourceIndex === null ? null : made[sourceIndex].id,
        by,
      }))
    }
    // The release candidate's migration failed: the pre-deploy hook exited 1 and no replica was touched.
    const bad = base('1.4.2-rc1')
    const hookOutput = 'Applying migration 20260301_AddInvoiceIndex...\nNpgsql.PostgresException (0x80004005): 42P07: relation "ix_invoices_customer_id" already exists\n   at Npgsql.Internal.NpgsqlConnector.ReadMessageLong(...)\n   at Microsoft.EntityFrameworkCore.Migrations.Internal.Migrator.Migrate(String targetMigration)\nFailed to apply 1 of 1 migrations.'
    const badDeployment = addDeployment(app, bad, {
      status: 'FAILED',
      startedAtMs: startedAt - 26 * HOUR,
      durationMs: 5200,
      error: 'pre-deploy command exited 1',
      events: [
        ['state', 'BUILDING', 'info', 50],
        ['step', `Pulled image ${bad.image}`, 'info', 1700],
        ['step', 'Running pre-deploy command', 'info', 200],
        ['log', `Last output of the pre-deploy command:\n${hookOutput}`, 'error', 3100],
        ['state', 'FAILED: pre-deploy command exited 1', 'error', 30],
      ],
      by: 'ci',
    })
    const sp = base('1.4.2')
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 2 * HOUR, durationMs: 18100, events: successEvents(sp, '1.4.1'), by: 'ci' })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    app.containers = makeContainers(app, active)

    // Runs: the hook of every deployment, the schedule's own with every outcome, and a few by hand.
    const hook = (d, status, exitCode, output, durationMs = 11800) => addRun(app, {
      job: 'pre-deploy', kind: 'hook', command: ['dotnet', 'Migrate.dll'], deploymentId: d.id,
      startedAtMs: Date.parse(d.started_at) + 2000, durationMs, status, exitCode, output,
    })
    for (const d of made) hook(d, 'succeeded', 0, 'Applying migration...\nDone. 1 migration applied.')
    hook(badDeployment, 'failed', 1, hookOutput, 3100)
    hook(active, 'succeeded', 0, 'No pending migrations.', 2400)
    const reportOutput = n => `Collecting invoices for the last 24h...\n${n} invoices, ${Math.round(n * 0.37)} customers\nReport written to s3://acme-reports/my-api/${iso(startedAt).slice(0, 10)}.pdf`
    for (let daysAgo = 6; daysAgo >= 1; daysAgo--) {
      const at = new Date(startedAt - daysAgo * DAY)
      at.setUTCHours(3, 0, 0, 0)
      const started = at.getTime()
      if (daysAgo === 4) {
        addRun(app, { job: 'nightly-report', kind: 'scheduled', command: ['node', 'report.js'], deploymentId: made[4]?.id ?? null, startedAtMs: started, durationMs: HOUR, status: 'timed_out', output: 'Collecting invoices for the last 24h...\nWaiting for the warehouse connection (attempt 12)...' })
        addAppEvent('my-api', 'warn', 'job', 'Job nightly-report timed out after 1h', started + HOUR)
      }
      else if (daysAgo === 2) {
        addRun(app, { job: 'nightly-report', kind: 'scheduled', command: ['node', 'report.js'], deploymentId: made[5].id, startedAtMs: started, durationMs: 12000, status: 'failed', exitCode: 1, output: 'Collecting invoices for the last 24h...\nError: connect ECONNREFUSED 10.0.4.12:5432\n    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1555:16)\nnpm error code 1' })
        addAppEvent('my-api', 'warn', 'job', 'Job nightly-report failed (exit 1)', started + 12000)
      }
      else {
        addRun(app, { job: 'nightly-report', kind: 'scheduled', command: ['node', 'report.js'], deploymentId: made[Math.min(5, 6 - daysAgo)].id, startedAtMs: started, durationMs: 40000 + daysAgo * 2100, status: 'succeeded', exitCode: 0, output: reportOutput(800 + daysAgo * 37) })
      }
    }
    for (let i = 8; i >= 1; i--) {
      const started = Math.floor((startedAt - i * 15 * MINUTE) / (15 * MINUTE)) * 15 * MINUTE
      addRun(app, { job: 'cleanup-sessions', kind: 'scheduled', command: ['node', 'cleanup.js', '--older-than', '30d'], deploymentId: active.id, startedAtMs: started, durationMs: 1800 + i * 90, status: 'succeeded', exitCode: 0, output: `Deleted ${3 + i * 2} expired sessions.` })
    }
    // The agent was restarted while one ran, five days ago.
    addRun(app, { job: 'cleanup-sessions', kind: 'scheduled', command: ['node', 'cleanup.js', '--older-than', '30d'], deploymentId: made[5].id, startedAtMs: startedAt - 5 * DAY, durationMs: 4000, status: 'interrupted', output: '' })
    addRun(app, { job: 'run', kind: 'manual', command: ['node', 'scripts/reindex.js', '--all'], deploymentId: made[5].id, startedAtMs: startedAt - 27 * HOUR, durationMs: 83000, status: 'failed', exitCode: 3, output: 'Reindexing customers... done (12,408)\nReindexing invoices...\nError: index invoices_v2 is read-only\nexit status 3' })
    addAppEvent('my-api', 'warn', 'job', 'Command node failed (exit 3)', startedAt - 27 * HOUR + 83000)
    addRun(app, { job: 'run', kind: 'manual', command: ['node', '-e', 'console.log(process.version)'], deploymentId: active.id, startedAtMs: startedAt - 3 * HOUR, durationMs: 900, status: 'succeeded', exitCode: 0, output: 'v22.12.0' })
    // One still running when the dashboard opens; it finishes shortly after.
    const running = addRun(app, { job: 'cleanup-sessions', kind: 'scheduled', command: ['node', 'cleanup.js', '--older-than', '30d'], deploymentId: active.id, startedAtMs: startedAt - 20 * SECOND, status: 'running' })
    setTimeout(() => finishRun(app, running, 'succeeded', '5m0s'), 8 * SECOND).unref()
    // A database: one replica, a named volume, the recreate strategy, a TCP
    // health check, and its port published on one of the server's addresses.
    const db = addApp('postgres', ago(20 * DAY))
    const dbSpec = spec('postgres', 'postgres:17', {
      port: 5432,
      // The password is a secret kept on the server, filled in at deploy time; masked like every value here.
      env: { POSTGRES_USER: MASK, POSTGRES_PASSWORD: MASK, POSTGRES_DB: MASK },
      health: { tcp: 5432, interval: '10s', timeout: '3s', retries: 3 },
      resources: { memory_bytes: 2 * 1024 ** 3 },
      volumes: [{ name: 'data', path: '/var/lib/postgresql/data' }],
      publish: [{ port: 5432, host: 15432, address: '10.0.0.5', protocol: 'tcp' }],
      // Archived every night after a checkpoint; a week of them is kept.
      backups: { schedule: '0 3 * * *', keep: 7, before: ['psql', '-h', 'localhost', '-U', 'postgres', '-c', 'CHECKPOINT'], ...(BEFORE_06 ? {} : { before_timeout: '2h0m0s' }), ...(BEFORE_07 ? {} : { before_in: 'container' }) },
      deploy: { strategy: 'recreate' },
    })
    const dbActive = addDeployment(db, dbSpec, { status: 'ACTIVE', startedAtMs: startedAt - 9 * DAY, durationMs: 8300, events: successEvents(dbSpec, null) })
    db.active_deployment_id = dbActive.id
    db.updated_at = dbActive.completed_at
    db.containers = makeContainers(db, dbActive)

    // Its backups: one taken by hand right after the deployment, then one a night at 03:00 UTC; one of those failed, the newest are verified.
    addBackup('postgres', { trigger: 'manual', startedAtMs: startedAt - 9 * DAY + 10 * MINUTE, durationMs: 31 * SECOND, status: 'succeeded', volumes: [{ volume: 'data', size_bytes: 2101346304 }] })
    for (let daysAgo = 6; daysAgo >= 0; daysAgo--) {
      const at = new Date(startedAt - daysAgo * DAY)
      at.setUTCHours(3, 0, 0, 0)
      const started = at.getTime()
      if (started > startedAt) continue
      if (daysAgo === 3) {
        addBackup('postgres', { trigger: 'schedule', startedAtMs: started, durationMs: 2 * SECOND, status: 'failed', error: 'backups.before exited 2: psql: error: connection to server on socket "/var/run/postgresql/.s.PGSQL.5432" failed; nothing was archived' })
        addAppEvent('postgres', 'warn', 'backup', 'Backup #' + (nextBackupId - 1) + ' failed: backups.before exited 2: psql: error: connection to server on socket "/var/run/postgresql/.s.PGSQL.5432" failed; nothing was archived', started + 2 * SECOND)
        continue
      }
      const run = addBackup('postgres', { trigger: 'schedule', startedAtMs: started, durationMs: (38 + daysAgo) * SECOND, status: 'succeeded', volumes: [{ volume: 'data', size_bytes: 2254857830 - daysAgo * 18350080 }] })
      if (daysAgo === 5) {
        run.verify_error = 'the container did not become healthy on the restored data within 2m: TCP :5432: connection refused'
        run.verify_output = 'PostgreSQL Database directory appears to contain a database; Skipping initialization\n\nLOG:  starting PostgreSQL 17.2 on x86_64-pc-linux-musl\nLOG:  database system was interrupted; last known up at 2026-09-27 02:59:58 UTC\nLOG:  invalid checkpoint record\nPANIC:  could not locate a valid checkpoint record'
      }
      if (daysAgo === 0 || daysAgo === 1) {
        run.verified_at = iso(Math.min(startedAt - 20 * MINUTE, started + 6 * HOUR))
        run.verify_output = VERIFY_OUTPUT
      }
    }

    addAppEvent('my-api', 'warn', 'supervisor', 'Replica 2 exited with code 137 (out of memory); restarting in 1s', startedAt - 3 * DAY)
    addAppEvent('my-api', 'info', 'supervisor', 'Replica 2 restarted', startedAt - 3 * DAY + 1200)
    addAppEvent('my-api', 'info', 'supervisor', 'Replica 2 is healthy again', startedAt - 3 * DAY + 4100)
  }

  // DEGRADED: three replicas desired, one of them is down and being restarted.
  // Several hostnames: the domain, an alias served alike, two redirects to the domain.
  {
    const app = addApp('web', ago(30 * DAY))
    const base = tag => spec('web', `ghcr.io/acme/web:${tag}`, {
      port: 3000,
      domain: 'example.com',
      aliases: ['app.example.com'],
      redirects: ['www.example.com', 'example.net'],
      replicas: 3,
      env: { API_URL: MASK, SESSION_SECRET: MASK },
      health: { path: '/healthz', interval: '15s', timeout: '2s', retries: 3 },
      resources: { cpu: 0.5, memory_bytes: 512 * 1024 ** 2 },
    })
    const first = base('2024.11.3')
    addDeployment(app, first, { status: 'SUPERSEDED', startedAtMs: startedAt - 30 * DAY, durationMs: 7400, events: successEvents(first, null) })
    const sp = base('2024.12.0')
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 3 * DAY, durationMs: 7900, events: successEvents(sp, '2024.11.3') })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    app.containers = makeContainers(app, active, {
      3: { state: 'exited', exit_code: 137, oom_killed: true, health: 'unhealthy', restarts: 2, started_at: ago(4 * MINUTE) },
    })
    // A rollout that died at its second replica and was rolled back: 2024.12.0 stayed (became again) what runs.
    const bad = base('2024.12.1')
    const reason = 'replica 2 exited with code 1 shortly after start'
    addDeployment(app, bad, {
      status: 'ROLLED_BACK',
      startedAtMs: startedAt - 20 * HOUR,
      durationMs: 9200,
      error: reason,
      events: rolledBackEvents(bad, '2024.12.0', 2, reason,
        '> web@2024.12.1 start\n> node server.js\n\nError: listen EADDRINUSE: address already in use :::3000\n    at Server.setupListenHandle [as _listen2] (node:net:1908:16)\n    at listenInCluster (node:net:1965:12)\nnpm error code 1'),
    })
    addAppEvent('web', 'warn', 'supervisor', 'Replica 3 exited with code 137 (out of memory); restarting in 1s', startedAt - 9 * MINUTE)
    addAppEvent('web', 'info', 'supervisor', 'Replica 3 restarted', startedAt - 9 * MINUTE + 1100)
    addAppEvent('web', 'warn', 'supervisor', 'Replica 3 exited with code 137 (out of memory); restarting in 2s', startedAt - 4 * MINUTE)
    addAppEvent('web', 'info', 'supervisor', 'Replica 3 restarted', startedAt - 4 * MINUTE + 2100)
    addAppEvent('web', 'warn', 'supervisor', 'Replica 3 exited with code 137 (out of memory); restarting in 5s', startedAt - 20 * SECOND)
  }

  // CRASH_LOOP: replica 2 never turns healthy, restarts are rate-limited.
  // Its image's entrypoint, command and user are overridden, and its logs go to a GELF collector.
  {
    const app = addApp('worker', ago(19 * DAY))
    const base = tag => spec('worker', `registry.example.com:5000/acme/worker:${tag}`, {
      port: 9090,
      replicas: 2,
      env: { QUEUE_URL: MASK, DATABASE_URL: MASK, CONCURRENCY: MASK },
      health: { path: '/ready', interval: '10s', timeout: '3s', retries: 3 },
      resources: { cpu: 2 },
      entrypoint: ['/app/worker'],
      command: ['--queue', 'default', '--log-format', 'json lines'],
      user: '1000:1000',
      logging: { driver: 'gelf', options: { 'gelf-address': 'udp://logs.example.com:12201', 'tag': '{{.Name}}' } },
      restart: { policy: 'on-failure' },
    })
    const first = base('0.9.0')
    addDeployment(app, first, { status: 'SUPERSEDED', startedAtMs: startedAt - 19 * DAY, durationMs: 5600, events: successEvents(first, null) })
    const sp = base('0.9.1')
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 2 * DAY, durationMs: 5900, events: successEvents(sp, '0.9.0') })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    app.containers = makeContainers(app, active, {
      2: { health: 'unhealthy', restarts: 5, crash_loop: true, started_at: ago(34 * SECOND) },
    })
    const t = startedAt - 12 * MINUTE
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 failed 3 health checks in a row: GET /ready: HTTP 503; restarting in 1s', t)
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 did not become healthy within 30s of starting: HTTP 503; restarting in 2s', t + 40 * SECOND)
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 did not become healthy within 30s of starting: HTTP 503; restarting in 5s', t + 80 * SECOND)
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 did not become healthy within 30s of starting: HTTP 503; restarting in 10s', t + 125 * SECOND)
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 did not become healthy within 30s of starting: HTTP 503; restarting in 30s', t + 175 * SECOND)
    addAppEvent('worker', 'error', 'supervisor', 'Replica 2 is crash-looping: 5 restarts without staying up. Retrying every 5m', t + 245 * SECOND)
    addAppEvent('worker', 'warn', 'supervisor', 'Replica 2 did not become healthy within 30s of starting: HTTP 503', startedAt - 28 * SECOND)
  }

  // STOPPED on request. It serves one path of a domain that `web` serves the rest of, and sees it without the prefix.
  {
    const app = addApp('docs', ago(60 * DAY))
    const sp = spec('docs', 'nginx:1.27-alpine', { port: 80, domain: 'example.com', path: '/docs', proxy: { strip_prefix: true } })
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 60 * DAY, durationMs: 4300, events: successEvents(sp, null) })
    app.active_deployment_id = active.id
    app.desired_state = 'stopped'
    app.updated_at = ago(6 * DAY)
    app.containers = makeContainers(app, active, { 1: { state: 'exited', exit_code: 0, started_at: ago(60 * DAY) } })
    addAppEvent('docs', 'info', 'app', 'Application stopped by ci', startedAt - 6 * DAY)
  }

  // Tokens besides root: one for CI, one read-only, never used yet.
  addToken('ci', 'deploy', startedAt - 20 * DAY, startedAt - 2 * HOUR)
  addToken('viewer', 'read', startedAt - 3 * DAY, null)
  if (!BEFORE_06) {
    // A token for two applications that ends soon, and one that has ended: it stays listed until it is revoked.
    addToken('web-ci', 'deploy', startedAt - 81 * DAY, startedAt - 26 * HOUR, undefined, { applications: ['landing', 'web'], expiresAtMs: startedAt + 9 * DAY })
    addToken('contractor', 'read', startedAt - 45 * DAY, startedAt - 16 * DAY, undefined, { expiresAtMs: startedAt - 15 * DAY })
    seedAccess()
  }

  // Secrets: names and dates only. The database password was rotated once.
  secrets.set('POSTGRES_PASSWORD', { created_at: ago(20 * DAY), updated_at: ago(6 * DAY) })
  secrets.set('STRIPE_KEY', { created_at: ago(4 * HOUR), updated_at: ago(4 * HOUR) })

  // A volume left behind by a deleted application, kept on purpose.
  orphanVolumes.push({ name: 'shipwick_pgtest_data', application: 'pgtest', volume: 'data', size_bytes: 13631488 })

  // Registry credentials: where the private images above come from.
  registries.set('ghcr.io', { username: 'acme-deploy', created_at: ago(41 * DAY), updated_at: ago(12 * DAY) })
  registries.set('registry.example.com:5000', { username: 'shipwick', created_at: ago(19 * DAY), updated_at: ago(19 * DAY) })

  // Certificates the operator supplied: one in its last 30 days, one in order, one that expired and was never replaced.
  const supplied = (hostname, subjects, issuer, fromMs, untilMs, storedMs) => certificates.set(hostname, {
    subjects, issuer, not_before: iso(fromMs), not_after: iso(untilMs), created_at: iso(storedMs), updated_at: iso(storedMs),
  })
  supplied('billing.example.com', ['billing.example.com'], 'Acme Issuing CA', startedAt - 345 * DAY, startedAt + 20 * DAY, startedAt - 340 * DAY)
  supplied('*.internal.example.org', ['*.internal.example.org', 'internal.example.org'], 'Acme Issuing CA', startedAt - 30 * DAY, startedAt + 335 * DAY, startedAt - 30 * DAY)
  supplied('legacy.example.com', ['legacy.example.com'], 'Sectigo RSA Domain Validation Secure Server CA', startedAt - 400 * DAY, startedAt - 4 * DAY, startedAt - 399 * DAY)

  // The agent's own state: its database and key, backed up daily at a quarter past three, a week kept.
  if (PASSPHRASE) {
    for (let daysAgo = 6; daysAgo >= 0; daysAgo--) {
      const at = new Date(startedAt - daysAgo * DAY)
      at.setUTCHours(3, 17, 0, 0)
      if (at.getTime() > startedAt) continue
      addBackup(STATE, { trigger: 'schedule', startedAtMs: at.getTime(), durationMs: 4 * SECOND, status: 'succeeded', volumes: [{ volume: 'shipwick.db', size_bytes: 1437696 + (6 - daysAgo) * 20480 }, { volume: 'encryption.key', size_bytes: 1536 }] })
    }
  }

  // A static application: a built frontend the proxy serves itself. No container, no replicas, no image.
  {
    const app = addApp('landing', ago(15 * DAY))
    // A single-page application: paths that name no file get index.html, and the router in it takes over.
    const sp = spec('landing', '', { domain: 'acme.example.com', redirects: ['www.acme.example.com'], static: { dir: 'dist', fallback: 'index.html' } })
    const first = { digest: 'sha256:9c1d7e2a4b60f3d8a5e7c2b1904f6d3e8a7b5c4d2e1f0a9b8c7d6e5f4a3b2c1d', size_bytes: 2987654, files: 39 }
    addDeployment(app, sp, { status: 'SUPERSEDED', startedAtMs: startedAt - 15 * DAY, durationMs: 2600, events: staticSuccessEvents(sp, first), by: 'ci', files: first })
    const files = { digest: 'sha256:3f2a8b1c9d4e7f60a2b5c8d1e4f7a0b3c6d9e2f5a8b1c4d7e0f3a6b9c2d5e8f1', size_bytes: 3250000, files: 42 }
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 3 * HOUR, durationMs: 2100, events: staticSuccessEvents(sp, files), by: 'ci', files })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    uploads.set('landing', files)
  }

  // An application whose image is built by the CLI and sent here: no registry involved.
  {
    const app = addApp('shop', ago(9 * DAY))
    const base = stamp => spec('shop', `${LOCAL_IMAGE_PREFIX}shop:${stamp}`, {
      port: 3000,
      domain: 'shop.example.com',
      build: { context: '.', dockerfile: 'Dockerfile' },
      env: { DATABASE_URL: MASK, SESSION_SECRET: MASK },
      health: { path: '/healthz', interval: '10s', timeout: '3s', retries: 3 },
      resources: { memory_bytes: 512 * 1024 ** 2 },
    })
    const first = base('20260918-091204-4e1a')
    addDeployment(app, first, { status: 'SUPERSEDED', startedAtMs: startedAt - 9 * DAY, durationMs: 6100, events: successEvents(first, null), by: 'ci' })
    const sp = base('20260926-141230-7c1e')
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 26 * HOUR, durationMs: 6800, events: successEvents(sp, '20260918-091204-4e1a'), by: 'ci' })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    app.containers = makeContainers(app, active)
  }

  // DEPLOYING: first deployment in flight, waiting on a slow starter. It gives up after 15 minutes.
  {
    const app = addApp('billing', ago(90 * SECOND))
    const sp = spec('billing', 'ghcr.io/acme/billing:3.0.0', {
      port: 8080,
      domain: 'billing.example.com',
      replicas: 2,
      env: { STRIPE_KEY: MASK, DATABASE_URL: MASK },
      // A JVM that takes its time: failed checks only count after the start period.
      health: { path: '/actuator/health', interval: '30s', timeout: '5s', retries: 30, start_period: '2m0s' },
      resources: { cpu: 2, memory_bytes: 2 * 1024 ** 3 },
    })
    const d = addDeployment(app, sp, {
      status: 'HEALTH_CHECKING',
      startedAtMs: startedAt - 90 * SECOND,
      durationMs: null,
      events: [
        ['state', 'BUILDING', 'info', 50],
        ['step', `Pulled image ${sp.image}`, 'info', 14200],
        ['state', 'STARTING', 'info', 20],
        ['step', 'Started 2 containers', 'info', 1500],
        ['state', 'HEALTH_CHECKING', 'info', 20],
      ],
    })
    app.containers = makeContainers(app, d, {
      1: { health: 'starting', started_at: ago(74 * SECOND) },
      2: { health: 'starting', started_at: ago(74 * SECOND) },
    })
    setTimeout(() => {
      if (!apps.has('billing') || d.completed_at) return
      const reason = 'replica 1 did not become healthy within 900s: GET /actuator/health on port 8080: connection refused'
      pushDeploymentEvent(d, 'state', `FAILED: ${reason}`, 'error')
      d.status = 'FAILED'
      d.error = reason
      app.containers = []
      endFollowers('billing')
      d.completed_at = iso(Date.now())
      app.updated_at = d.completed_at
    }, 15 * MINUTE).unref()
  }

  // FAILED: no deployment has ever succeeded.
  {
    const app = addApp('legacy-cron', ago(2 * DAY))
    const first = spec('legacy-cron', 'registry.example.com:5000/acme/legacy-cron:7')
    addDeployment(app, first, {
      status: 'FAILED',
      startedAtMs: startedAt - 2 * DAY,
      durationMs: 2100,
      error: 'pull registry.example.com:5000/acme/legacy-cron:7: manifest unknown',
      events: [
        ['state', 'BUILDING', 'info', 50],
        ['state', 'FAILED: pull registry.example.com:5000/acme/legacy-cron:7: manifest unknown', 'error', 1900],
      ],
    })
    const second = spec('legacy-cron', 'registry.example.com:5000/acme/legacy-cron:8', { env: { TZ: MASK } })
    const d = addDeployment(app, second, {
      status: 'FAILED',
      startedAtMs: startedAt - 47 * HOUR,
      durationMs: 4800,
      error: 'replica 1 exited with code 127 shortly after start',
      events: failureEvents(second, 'replica 1 exited with code 127 shortly after start',
        '/entrypoint.sh: line 4: /usr/local/bin/cron-runner: not found'),
    })
    app.updated_at = d.completed_at
  }

  // Exports of the whole server in the backups: nightly, three kept.
  if (PASSPHRASE && !IS_STANDBY) {
    for (let daysAgo = 2; daysAgo >= 0; daysAgo--) {
      const at = new Date(startedAt - daysAgo * DAY)
      at.setUTCHours(4, 0, 0, 0)
      if (at.getTime() > startedAt) continue
      addBackup(EXPORTS, { trigger: 'schedule', startedAtMs: at.getTime(), durationMs: 96 * SECOND, status: 'succeeded', volumes: [{ volume: 'export.tar', size_bytes: 3141592653 + (2 - daysAgo) * 20971520 }] })
    }
  }

  // A standby holds what an export brought, deployed and stopped, until it is promoted.
  if (IS_STANDBY) {
    for (const [i, name] of STANDBY_APPLICATIONS.entries()) {
      const app = apps.get(name)
      const active = deployments.get(app.active_deployment_id)
      active.kind = 'standby'
      active.started_at = ago(47 * MINUTE - i * 9 * SECOND)
      active.completed_at = ago(47 * MINUTE - i * 9 * SECOND - 6 * SECOND)
      app.desired_state = 'stopped'
      app.updated_at = active.completed_at
      app.containers = makeContainers(app, active).map(c => ({ ...c, state: 'created', started_at: null, health: active.spec.health ? 'unknown' : '' }))
    }
    standbyPull.last_at = ago(47 * MINUTE)
    standbyPull.last_export = 42
  }

  if (!BEFORE_06) seedAudit()
  if (IS_STANDBY && PROMOTING && !BEFORE_06) startPromotion(20 * SECOND)

  // What the documents of these applications wrote as references to stored
  // secrets; every other value of theirs was a literal, and is masked for good.
  const refer = (name, env) => {
    for (const d of deployments.values()) if (d.application === name) references.set(d.id, { env, basic_auth: {} })
  }
  refer('my-api', { DATABASE_URL: 'postgres://api:${POSTGRES_PASSWORD}@postgres:5432/api' })
  refer('postgres', { POSTGRES_PASSWORD: '${POSTGRES_PASSWORD}' })
  refer('billing', { STRIPE_KEY: '${STRIPE_KEY}' })
  if (!BEFORE_07 && LOG_ARCHIVE) seedLogArchive()

  if (EMPTY) {
    apps.clear()
    deployments.clear()
    appEvents.clear()
    audit.length = 0
    logArchive.length = 0
    for (const run of [...backups.values()]) if (run.application === EXPORTS) backups.delete(run.id)
  }

  for (const app of apps.values()) {
    for (let i = 0; i < 120; i++) generateLogLine(app, startedAt - (120 - i) * 900)
  }
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

function inFlight(name) {
  for (const d of deployments.values()) {
    if (d.application === name && d.completed_at === null) return d
  }
  return null
}

function isHealthy(c) {
  return c.state === 'running' && c.health !== 'unhealthy'
}

const isStaticSpec = sp => Boolean(sp?.static)

function appSummary(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const flying = inFlight(app.name)
  // Mid-rollout, replica slots are filled by containers of two deployments: count slots, not containers.
  const own = active ? app.containers.filter(c => flying || c.deployment_id === active.id) : []
  // A static application has nothing to count: the proxy serves it, all three numbers are zero, and zero of zero is healthy.
  const desired = active && !isStaticSpec(active.spec) ? active.spec.replicas : 0
  const running = new Set(own.filter(c => c.state === 'running').map(c => c.replica)).size
  const healthy = new Set(own.filter(isHealthy).map(c => c.replica)).size

  let status
  if (!active) status = flying ? 'DEPLOYING' : 'FAILED'
  else if (app.desired_state === 'stopped') status = 'STOPPED'
  else if (own.some(c => c.crash_loop)) status = 'CRASH_LOOP'
  else if (healthy >= desired) status = 'HEALTHY'
  else if (healthy > 0) status = 'DEGRADED'
  else status = 'DOWN'

  return {
    name: app.name,
    status,
    desired_state: app.desired_state,
    image: active ? active.image : '',
    version: active ? active.version : '',
    domain: active ? active.spec.domain ?? '' : '',
    ...(active?.spec.aliases?.length ? { aliases: active.spec.aliases } : {}),
    ...(active?.spec.redirects?.length ? { redirects: active.spec.redirects } : {}),
    ...(active?.spec.path ? { path: active.spec.path } : {}),
    replicas: { desired, running, healthy },
    deploying: Boolean(flying),
    in_flight_deployment_id: flying ? flying.id : null,
    created_at: app.created_at,
    updated_at: app.updated_at,
    static: isStaticSpec(active?.spec ?? flying?.spec),
    ...(BEFORE_06 ? {} : { certificate_problem: certificateProblem(active), ...alertSummary(app.name) }),
  }
}

/** The hostname whose certificate is worst off: waiting for DNS before expiring before obtaining; null when all are in order. */
function certificateProblem(active) {
  const order = ['waiting_for_dns', 'expiring', 'obtaining']
  const worst = hostCertificates(active)
    .filter(c => order.includes(c.status))
    .sort((a, b) => order.indexOf(a.status) - order.indexOf(b.status))[0]
  return worst ? { hostname: worst.hostname, status: worst.status, message: worst.message } : null
}

function alertSummary(name) {
  const mine = activeAlerts().filter(a => a.application === name)
  return { alert_count: mine.length, alert_severity: mine.some(a => a.severity === 'critical') ? 'critical' : mine.length > 0 ? 'warning' : '' }
}

/** The application is a folder the proxy serves: logs, metrics, jobs and commands do not apply. */
function refuseStatic(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  if (isStaticSpec(active?.spec ?? inFlight(app.name)?.spec)) {
    throw new HttpError(409, 'STATIC_APPLICATION', 'this application is a folder served by the proxy; it has no containers')
  }
}

/** A deployment as it appears in lists: without its spec and events. */
function deploymentView(d) {
  const { spec: _spec, events: _events, ...rest } = d
  return rest
}

function appDetail(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  return {
    ...appSummary(app),
    spec: active ? active.spec : null,
    active_deployment: active ? deploymentView(active) : null,
    // A container the agent is retiring says so itself from 0.6 on.
    containers: BEFORE_06 ? app.containers : app.containers.map(c => ({ ...c, stopping: draining.has(c.id) })),
    ...(OLD_AGENT ? {} : { certificates: hostCertificates(active) }),
  }
}

// ---------------------------------------------------------------------------
// Certificate status: what the proxy presents for each hostname
// ---------------------------------------------------------------------------

const day = ms => iso(ms).slice(0, 10)

/** Hostnames that are not simply in order, so every status has one to be looked at. */
const CERTIFICATE_STATES = {
  'example.net': () => ({ status: 'waiting_for_dns', issuer: '', not_after: null, message: `does not resolve yet; add an A record: example.net → ${SERVER_ADDRESS}${DNS_CHALLENGE ? '' : ' (DNS only, not proxied)'}` }),
  'app.example.com': () => ({ status: 'obtaining', issuer: '', not_after: null, message: 'the proxy has no certificate for it yet; HTTPS connections to it fail until it does' }),
  'shop.example.com': () => ({ status: 'expiring', issuer: 'Let\'s Encrypt E7', not_after: iso(startedAt + 9 * DAY), message: `expires in 9 days, on ${day(startedAt + 9 * DAY)}` }),
}

/** A supplied certificate covers a hostname by its exact name, or by a wildcard one label up. */
function suppliedFor(hostname) {
  for (const [stored, c] of certificates) {
    if (c.subjects.some(s => s === hostname || (s.startsWith('*.') && !hostname.startsWith('*.') && hostname.slice(hostname.indexOf('.') + 1) === s.slice(2)))) return { hostname: stored, ...c }
  }
  return null
}

/** One entry per hostname in the order domain, aliases, redirects; [] without a domain or an active deployment. */
function hostCertificates(active) {
  if (!active?.spec.domain) return []
  return hostnamesOf(active.spec).map((hostname) => {
    if (!PROXY_ENABLED) return { hostname, status: 'unknown', issuer: '', not_after: null, message: 'this agent has no reverse proxy configured' }
    const supplied = suppliedFor(hostname)
    if (supplied) {
      const left = Math.floor((Date.parse(supplied.not_after) - Date.now()) / DAY)
      const soon = left < 14
      return { hostname, status: soon ? 'expiring' : 'ok', issuer: supplied.issuer, not_after: supplied.not_after, message: !soon ? '' : left < 0 ? `expired on ${supplied.not_after.slice(0, 10)}` : `expires in ${plural(left, 'day')}, on ${supplied.not_after.slice(0, 10)}` }
    }
    const special = CERTIFICATE_STATES[hostname]?.()
    return { hostname, ...(special ?? { status: 'ok', issuer: 'Let\'s Encrypt E7', not_after: iso(startedAt + 61 * DAY), message: '' }) }
  })
}

// ---------------------------------------------------------------------------
// Deployment engine (simulated)
// ---------------------------------------------------------------------------

function pushDeploymentEvent(d, type, message, level = 'info') {
  d.events.push({ id: nextEventId++, deployment_id: d.id, level, type, message, created_at: iso(Date.now()) })
}

/**
 * Simulates the agent's rolling deployment: pull, then for every replica
 * start the new one → wait until it is ready → it takes over → its predecessor
 * is retired; then route, commit, done. Roughly six seconds for two replicas.
 *
 * Tag `fail` dies at replica 1 (FAILED: nothing was touched). Tag `rollback`
 * dies at replica 2, after replica 1 was replaced, so the retired replica of
 * the previous version is restored: FAILED → ROLLBACK → RESTORING → ROLLED_BACK.
 */
function startDeployment(app, sp, { kind, sourceId, by, files = null, refs = null }) {
  if (isStaticSpec(sp)) return startStaticDeployment(app, sp, files, { kind, sourceId, by })
  const previous = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const d = addDeployment(app, sp, { status: 'PENDING', startedAtMs: Date.now(), durationMs: null, kind, sourceId, by })
  // A redeploy and a rollback start the values they find: the document they came from is still the one that wrote them.
  const kept = refs ?? references.get(sourceId)
  if (kept) references.set(d.id, kept)
  const tag = d.version
  const n = sp.replicas
  const failAt = tag === 'fail' ? 1 : tag === 'rollback' ? Math.min(2, n) : 0

  let clock = 0
  /** Schedules fn `delay` ms after the previously scheduled step. */
  const then = (delay, fn) => {
    clock += delay
    setTimeout(() => {
      // The application may have been deleted meanwhile.
      if (apps.get(app.name) !== app || d.completed_at) return
      fn()
    }, clock)
  }
  const state = (s) => {
    d.status = s
    pushDeploymentEvent(d, 'state', s)
  }
  const complete = () => {
    d.completed_at = iso(Date.now())
    app.updated_at = d.completed_at
  }
  const newContainer = (i, from) => makeContainers(app, { ...from, completed_at: iso(Date.now()) }).find(c => c.replica === i)
  const removeContainer = (deploymentId, i) => {
    app.containers = app.containers.filter(c => !(c.deployment_id === deploymentId && c.replica === i))
  }

  then(400, () => state('BUILDING'))
  then(1100, () => {
    if (tag === 'local') pushDeploymentEvent(d, 'step', `Could not pull ${sp.image}: registry unreachable. Using the local copy`, 'warn')
    else pushDeploymentEvent(d, 'step', `Pulled image ${sp.image}`)
    if (!sp.pre_deploy) state('STARTING')
  })

  // The hook runs after the pull, before any replica is touched; its failure is the deployment's.
  if (sp.pre_deploy) {
    let hookRun
    then(200, () => {
      pushDeploymentEvent(d, 'step', 'Running pre-deploy command')
      hookRun = addRun(app, { job: 'pre-deploy', kind: 'hook', command: sp.pre_deploy.command, deploymentId: d.id, startedAtMs: Date.now(), status: 'running' })
    })
    if (tag === 'hookfail') {
      then(1800, () => {
        finishRun(app, hookRun, 'failed', sp.pre_deploy.timeout, true)
        pushDeploymentEvent(d, 'log', `Last output of the pre-deploy command:\n${hookRun.output}`, 'error')
        d.status = 'FAILED'
        d.error = 'pre-deploy command exited 1'
        pushDeploymentEvent(d, 'state', `FAILED: ${d.error}`, 'error')
      })
      then(400, complete)
      return d
    }
    then(2200, () => {
      finishRun(app, hookRun, 'succeeded', sp.pre_deploy.timeout, true)
      pushDeploymentEvent(d, 'step', 'Pre-deploy command finished (2s)')
      state('STARTING')
    })
  }

  const crash = (i) => {
    const reason = `replica ${i} exited with code 1 shortly after start`
    pushDeploymentEvent(d, 'log', `level=info msg="starting ${app.name}" version=${tag}\nlevel=info msg="loading configuration"\nlevel=fatal msg="config: FEATURE_FLAGS_URL is required"\nexit status 1`, 'error')
    const died = app.containers.find(c => c.deployment_id === d.id && c.replica === i)
    if (died) {
      say(app, died, [['stdout', `level=info msg="starting ${app.name}" version=${tag}`], ['stdout', 'level=info msg="loading configuration"'], ['stderr', 'level=fatal msg="config: FEATURE_FLAGS_URL is required"']])
      archiveContainer(app, { ...died, state: 'exited' }, 'deployment_failed', { exitCode: 1 })
    }
    removeContainer(d.id, i)
    d.status = 'FAILED'
    d.error = reason
    pushDeploymentEvent(d, 'state', `FAILED: ${reason}`, 'error')
  }

  // Replicas that replace a predecessor go one at a time; those with nothing to
  // replace (scaling up) would start together, which redeploy/rollback of the
  // fixtures never needs beyond the simple case handled here.
  for (let i = 1; i <= n; i++) {
    const old = previous && i <= previous.spec.replicas ? previous : null
    then(i === 1 ? 300 : 250, () => {
      app.containers = [...app.containers, { ...newContainer(i, d), health: sp.health ? 'starting' : '' }]
      pushDeploymentEvent(d, 'step', 'Started 1 container')
      if (d.status === 'STARTING') state('HEALTH_CHECKING')
    })

    // The agent is restarted while the first replica is being checked. It leaves
    // the deployment as it is and picks it up again: nothing is started twice.
    if (tag === 'restart' && i === 1) {
      then(400, () => {
        awayUntil = Date.now() + AWAY_MS
      })
      then(AWAY_MS + 200, () => pushDeploymentEvent(d, 'step', 'Resumed after the agent restarted'))
    }

    if (i === failAt) {
      then(900, () => crash(i))
      if (i === 1) {
        // Nothing of the previous version was retired: FAILED is the end.
        then(500, complete)
        return d
      }
      // Replicas 1..i-1 of the previous version are gone: bring them back.
      then(300, () => {
        state('ROLLBACK')
        pushDeploymentEvent(d, 'step', `Rolling back: restoring ${plural(i - 1, 'replica')} of ${previous.version}`)
        state('RESTORING')
        for (let r = 1; r < i; r++) {
          app.containers = [...app.containers, { ...newContainer(r, previous), health: previous.spec.health ? 'starting' : '' }]
        }
      })
      then(1500, () => {
        for (const c of app.containers) if (c.deployment_id === previous.id && c.health === 'starting') c.health = 'healthy'
        pushDeploymentEvent(d, 'step', readyMessage(previous.spec, Array.from({ length: i - 1 }, (_, k) => k + 1)))
        // Traffic is back on the restored replicas: only now do the new ones go.
        for (const c of app.containers) if (c.deployment_id === d.id) archiveContainer(app, c, 'deployment_failed')
        app.containers = app.containers.filter(c => c.deployment_id !== d.id)
        pushDeploymentEvent(d, 'step', `Rolled back: ${app.name} is running ${previous.version} again`)
        state('ROLLED_BACK')
      })
      then(400, complete)
      return d
    }

    then(900, () => {
      const c = app.containers.find(x => x.deployment_id === d.id && x.replica === i)
      if (c && sp.health) c.health = 'healthy'
      pushDeploymentEvent(d, 'step', readyMessage(sp, [i]))
    })
    then(300, () => {
      if (old) {
        // A rollout waits for the replica it replaced before it starts the next
        // one; the last one is not waited for. It drains in the background, and
        // stays listed, with the previous deployment's id, until it has exited.
        const last = app.containers.find(c => c.deployment_id === old.id && c.replica === i)
        if (i === n && last) draining.add(last.id)
        else {
          const retired = app.containers.find(c => c.deployment_id === old.id && c.replica === i)
          if (retired) archiveContainer(app, retired, 'replaced', { exitCode: 0 })
          removeContainer(old.id, i)
        }
        pushDeploymentEvent(d, 'step', servingMessage(i, n, d.version, old.version))
      }
      else {
        pushDeploymentEvent(d, 'step', `Replica ${i}/${n} is serving ${d.version}`)
      }
      // The last predecessor is gone: streams that followed only old containers end here, as with the real agent.
      if (previous && !app.containers.some(x => x.deployment_id === previous.id)) endFollowers(app.name)
    })
  }

  then(200, () => {
    // Scaling down: surplus replicas of the previous version are retired last.
    if (previous) app.containers = app.containers.filter(c => c.deployment_id !== previous.id || draining.has(c.id))
    state('HEALTHY')
    if (sp.domain) {
      const [type, message, level] = routedMessage(sp)
      pushDeploymentEvent(d, type, message, level)
    }
  })
  then(200, () => {
    // Commit point: promote, supersede, repoint.
    state('ACTIVE')
    if (previous) previous.status = 'SUPERSEDED'
    app.active_deployment_id = d.id
    app.desired_state = 'running'
    app.updated_at = iso(Date.now())
    pushDeploymentEvent(d, 'step', 'Deployment successful')
  })
  then(700, () => {
    complete()
    // The deployment is over; the replica it replaced last is still on its way out.
    const grace = parseDuration(previous?.spec.deploy?.stop_timeout ?? '') ?? DEFAULT_STOP_TIMEOUT_MS
    const ignoresSigterm = tag === 'sigterm'
    setTimeout(() => {
      const gone = app.containers.filter(c => draining.has(c.id))
      for (const c of gone) draining.delete(c.id)
      if (apps.get(app.name) !== app || gone.length === 0) return
      for (const c of gone) archiveContainer(app, c, 'replaced', { exitCode: ignoresSigterm ? 137 : 0 })
      app.containers = app.containers.filter(c => !gone.includes(c))
      if (ignoresSigterm) {
        const within = formatGoDuration(grace).replace(/(?<=\d[hm])0[ms]/g, '')
        const advice = previous?.spec.init
          ? 'It runs under an init process, so the signal reached it: to give it longer, set deploy.stop_timeout'
          : 'If its process has no handler for SIGTERM, set init: true and the signal ends it at once; to let it finish its requests, handle SIGTERM in the application; to give it longer, set deploy.stop_timeout'
        for (const c of gone) addAppEvent(app.name, 'warn', 'app', `The replaced container ${c.name} did not exit within ${within} of SIGTERM and was killed. ${advice}`)
      }
      if (!app.containers.some(x => x.deployment_id === previous?.id)) endFollowers(app.name)
    }, ignoresSigterm ? grace : Math.min(grace, DRAIN_MS)).unref()
  })
  return d
}

/** How long a replaced replica that handles SIGTERM takes to exit here, and the grace period without deploy.stop_timeout. */
const DRAIN_MS = 6 * SECOND
const DEFAULT_STOP_TIMEOUT_MS = 10 * SECOND
/** How long the `restart` tag keeps the mock away. */
const AWAY_MS = 6 * SECOND

/**
 * A static deployment: no replicas, the folder is copied into the proxy (or is
 * there already, on a redeploy or rollback), checked for index.html and routed.
 * Containers of a previous container version are retired at the end.
 */
function startStaticDeployment(app, sp, files, { kind, sourceId, by }) {
  const previous = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const d = addDeployment(app, sp, { status: 'PENDING', startedAtMs: Date.now(), durationMs: null, kind, sourceId, by, files })
  // Folders are kept by digest: the serving version's and the one before it.
  const kept = new Set([previous?.static?.digest, previous && deployments.get(previous.source_deployment_id)?.static?.digest].filter(Boolean))
  const alreadyThere = kept.has(files.digest)
  const olderFolders = Math.max(0, kept.size + (alreadyThere ? 0 : 1) - 2)
  let clock = 0
  for (const [type, message, level, dt] of staticSuccessEvents(sp, files, alreadyThere, olderFolders)) {
    clock += dt
    setTimeout(() => {
      if (apps.get(app.name) !== app || d.completed_at) return
      pushDeploymentEvent(d, type, message, level)
      if (type !== 'state') return
      d.status = message
      if (message === 'ACTIVE') {
        if (previous) previous.status = 'SUPERSEDED'
        app.active_deployment_id = d.id
        app.desired_state = 'running'
        app.containers = []
        endFollowers(app.name)
        app.updated_at = iso(Date.now())
      }
    }, clock)
  }
  setTimeout(() => {
    if (apps.get(app.name) !== app || d.completed_at) return
    d.completed_at = iso(Date.now())
    app.updated_at = d.completed_at
  }, clock + 400)
  return d
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

const HTTP_PATHS = ['/v1/users', '/v1/users/42', '/v1/orders', '/v1/orders/9913/items', '/v1/session', '/health', '/v1/search?q=invoice', '/v1/webhooks/stripe']
const pick = list => list[Math.floor(Math.random() * list.length)]

function messageFor(app, container) {
  if (container.health === 'unhealthy' || container.crash_loop) {
    return pick([
      ['stderr', 'level=error msg="queue connection failed" err="dial tcp 10.0.4.12:5672: connect: connection refused"'],
      ['stderr', 'level=warn msg="readiness probe failing" reason="queue not connected"'],
      ['stdout', 'level=info msg="retrying queue connection" attempt=' + (1 + Math.floor(Math.random() * 9)) + ' backoff=2s'],
    ])
  }
  if (container.health === 'starting') {
    return pick([
      ['stdout', 'INFO  o.s.b.w.e.tomcat.TomcatWebServer - initializing'],
      ['stdout', 'INFO  o.f.c.i.database.DatabaseFactory - running migration V' + (40 + Math.floor(Math.random() * 30))],
      ['stdout', 'INFO  o.h.e.t.j.p.i.JtaPlatformInitiator - HHH000490: Using JtaPlatform implementation'],
    ])
  }
  const roll = Math.random()
  if (roll < 0.06) {
    return ['stderr', `level=error msg="upstream timeout" path=${pick(HTTP_PATHS)} upstream=payments elapsed=5.00s request_id=${randomHex(12)}`]
  }
  if (roll < 0.14) {
    return ['stderr', `level=warn msg="slow query" table=orders duration=${(300 + Math.random() * 900).toFixed(0)}ms`]
  }
  if (roll < 0.2) {
    return ['stdout', `level=info msg="cache refreshed" keys=${Math.floor(Math.random() * 4000)} note="ünïcödé → ✓"`]
  }
  const status = Math.random() < 0.93 ? 200 : pick([201, 204, 301, 400, 404, 500])
  return ['stdout', `level=info msg="request" method=${pick(['GET', 'GET', 'GET', 'POST', 'PUT'])} path=${pick(HTTP_PATHS)} status=${status} duration=${(1 + Math.random() * 180).toFixed(1)}ms request_id=${randomHex(12)}`]
}

function generateLogLine(app, atMs = Date.now()) {
  const running = app.containers.filter(c => c.state === 'running')
  if (running.length === 0) return
  const container = pick(running)
  const [stream, message] = messageFor(app, container)
  const line = { replica: container.replica, container: container.name, stream, time: iso(atMs), message }
  const buffer = logBuffers.get(app.name)
  buffer.push(line)
  if (buffer.length > LOG_BUFFER) buffer.splice(0, buffer.length - LOG_BUFFER)
  for (const f of followers.get(app.name) ?? []) f.onLine(line)
}

function endFollowers(name) {
  const set = followers.get(name)
  if (!set) return
  followers.delete(name)
  for (const f of set) f.onEnd()
}

function tailLines(app, tail, perReplica) {
  // Only containers that still exist can be read, as with Docker.
  const current = new Set(app.containers.map(c => c.name))
  const buffer = (logBuffers.get(app.name) ?? []).filter(line => current.has(line.container))
  if (tail === 0) return []
  if (!perReplica) return buffer.slice(-tail)
  // Following: each replica starts with its own last `tail` lines.
  const counts = new Map()
  const out = []
  for (let i = buffer.length - 1; i >= 0; i--) {
    const line = buffer[i]
    const seen = counts.get(line.container) ?? 0
    if (seen >= tail) continue
    counts.set(line.container, seen + 1)
    out.push(line)
  }
  return out.reverse()
}

// ---------------------------------------------------------------------------
// The log archive: the output of container runs that ended
// ---------------------------------------------------------------------------

/** What is kept of one run of a replica, and of a job. */
const REPLICA_LINES = 2000
const RUN_LINES = 10000

/** What a container says last when it dies of each cause. */
const LAST_WORDS = {
  oom_killed: [
    ['stdout', 'level=info msg="rendering report" rows=1840221'],
    ['stderr', '<--- Last few GCs --->'],
    ['stderr', '[1:0x7f2c4c000000]   412803 ms: Mark-Compact 498.2 (514.6) -> 497.9 (514.9) MB, 812.33 / 0.00 ms  (average mu = 0.112, current mu = 0.004) allocation failure; scavenge might not succeed'],
    ['stderr', 'FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory'],
  ],
  unhealthy: [
    ['stderr', 'level=error msg="queue connection failed" err="dial tcp 10.0.4.12:5672: connect: connection refused"'],
    ['stderr', 'level=warn msg="readiness probe failing" reason="queue not connected"'],
    ['stdout', 'level=info msg="received SIGTERM, shutting down"'],
  ],
  stopped: [
    ['stdout', 'level=info msg="received SIGTERM, shutting down"'],
    ['stdout', 'level=info msg="server closed" in_flight=0'],
  ],
  replaced: [
    ['stdout', 'level=info msg="received SIGTERM, shutting down"'],
    ['stdout', 'level=info msg="draining connections" in_flight=3'],
    ['stdout', 'level=info msg="server closed" in_flight=0'],
  ],
}

/** Writes lines as a container's output, now: into what is tailed and to whoever follows. */
function say(app, container, said) {
  const buffer = logBuffers.get(app.name)
  if (!buffer) return
  const now = Date.now()
  for (const [i, [stream, message]] of said.entries()) {
    const line = { replica: container.replica, container: container.name, stream, time: iso(now + i), message }
    buffer.push(line)
    for (const f of followers.get(app.name) ?? []) f.onLine(line)
  }
}

/** Keeps `lines` as the output of one container run that ended. Nothing is kept by an agent before 0.7, or with the archive off. */
function addArchive(app, { kind = 'replica', deployment = null, replica = 0, job = '', runId = null, container, reason, exitCode = null, oomKilled = false, endedAtMs = Date.now(), lines = [] }) {
  if (BEFORE_07 || !LOG_ARCHIVE) return null
  const limit = kind === 'run' ? RUN_LINES : REPLICA_LINES
  const kept = lines.slice(-limit)
  const bytes = kept.reduce((n, line) => n + Buffer.byteLength(line.message) + 1, 0)
  const entry = {
    id: nextArchiveId++,
    application: app.name,
    kind,
    deployment_id: deployment?.id ?? null,
    deployment: deployment?.sequence ?? 0,
    version: deployment?.version ?? '',
    replica,
    job,
    run_id: runId,
    container,
    reason,
    exit_code: exitCode,
    oom_killed: oomKilled,
    ended_at: iso(endedAtMs),
    first_line_at: kept[0]?.time ?? null,
    last_line_at: kept[kept.length - 1]?.time ?? null,
    lines: kept.length,
    bytes,
    stored_bytes: kept.length === 0 ? 0 : Math.round(bytes / 4.7) + 93,
    truncated: lines.length > limit,
    output: kept,
  }
  logArchive.push(entry)
  if (logArchive.length > 3000) logArchive.splice(0, logArchive.length - 3000)
  return entry
}

/** An entry as the list shows it: without its lines. */
function archiveView(entry) {
  const { output: _output, ...rest } = entry
  return rest
}

/**
 * Copies what a replica wrote since the last copy of the same container: a
 * crash loop leaves one entry per attempt and no line is kept twice.
 */
function archiveContainer(app, container, reason, { exitCode = null, oomKilled = false } = {}) {
  if (LAST_WORDS[reason] && container.state === 'running') say(app, container, LAST_WORDS[reason])
  const since = archivedUntil.get(container.name) ?? ''
  const lines = (logBuffers.get(app.name) ?? []).filter(line => line.container === container.name && line.time > since)
  if (lines.length > 0) archivedUntil.set(container.name, lines[lines.length - 1].time)
  return addArchive(app, { deployment: deployments.get(container.deployment_id) ?? null, replica: container.replica, container: container.name, reason, exitCode, oomKilled, lines: lines.map(line => ({ ...line })) })
}

/** A run's whole output, where its record keeps the last 200 lines. */
function archiveRun(app, run, lines) {
  const at = Date.parse(run.finished_at ?? run.started_at)
  const container = `shipwick_${app.name}_job_${run.job}_${run.id}`
  const output = lines ?? String(run.output ?? '').split('\n').filter(text => text !== '').map((message, i, all) => ({
    replica: 0,
    container,
    stream: /error|fatal|exception|^\s+at /i.test(message) ? 'stderr' : 'stdout',
    time: iso(at - (all.length - i) * 40),
    message,
  }))
  return addArchive(app, { kind: 'run', deployment: deployments.get(run.deployment_id) ?? null, job: run.job, runId: run.id, container, reason: run.status, exitCode: run.exit_code, endedAtMs: at, lines: output })
}

/** `count` plausible lines of a container that ran until `endMs`, the newest of them `said`. */
function pastLines(app, container, endMs, count, said = []) {
  const lines = []
  for (let i = count; i > 0; i--) {
    const [stream, message] = messageFor(app, { ...container, health: 'healthy', crash_loop: false })
    lines.push({ replica: container.replica, container: container.name, stream, time: iso(endMs - said.length * 3 - i * 850), message })
  }
  for (const [i, [stream, message]] of said.entries()) lines.push({ replica: container.replica, container: container.name, stream, time: iso(endMs - (said.length - i) * 3), message })
  return lines
}

/** What the archive holds when the mock starts: the crashes, the failed deployments, the replaced replicas and the runs of the fixtures. */
function seedLogArchive() {
  const past = (name, d, replica, endedAgoMs, reason, { count = 40, said = LAST_WORDS[reason] ?? [], exitCode = null, oomKilled = false } = {}) => {
    const app = apps.get(name)
    const container = { replica, name: `shipwick_${name}_${d.sequence}_${replica}` }
    const endMs = startedAt - endedAgoMs
    const lines = pastLines(app, container, endMs, count, said)
    archivedUntil.set(container.name, lines[lines.length - 1]?.time ?? iso(endMs))
    return addArchive(app, { deployment: d, replica, container: container.name, reason, exitCode, oomKilled, endedAtMs: endMs, lines })
  }
  const of = name => [...deployments.values()].filter(d => d.application === name).sort((a, b) => a.id - b.id)
  const lines = (container, replica, atMs, texts) => texts.split('\n').map((message, i, all) => ({ replica, container, stream: /error|fatal|not found|exited/i.test(message) ? 'stderr' : 'stdout', time: iso(atMs - (all.length - i) * 120), message }))

  // Every run with its output, oldest first, as they ended.
  const seededRuns = [...runs.values()].filter(r => r.finished_at).sort((a, b) => a.finished_at.localeCompare(b.finished_at))

  const events = []
  const myApi = of('my-api')
  // Each deployment that was replaced left the output of its two replicas.
  for (let i = 3; i < myApi.length - 1; i++) {
    const d = myApi[i]
    const next = myApi.slice(i + 1).find(n => n.status !== 'FAILED')
    if (d.status !== 'SUPERSEDED' || !next) continue
    for (const replica of [1, 2]) events.push([Date.parse(next.completed_at) + replica * 900, () => past('my-api', d, replica, startedAt - Date.parse(next.completed_at) - replica * 900, 'replaced', { count: 60, exitCode: 0 })])
  }
  // Three days ago replica 2 ran out of memory once.
  events.push([startedAt - 3 * DAY, () => past('my-api', myApi[myApi.length - 3], 2, 3 * DAY, 'oom_killed', { count: 80, exitCode: 137, oomKilled: true })])

  const web = of('web')
  const webActive = web.find(d => d.status === 'ACTIVE')
  const webBad = web.find(d => d.status === 'ROLLED_BACK')
  events.push([Date.parse(webActive.completed_at), () => {
    for (const replica of [1, 2, 3]) past('web', web[0], replica, startedAt - Date.parse(webActive.completed_at) - replica * 700, 'replaced', { count: 50, exitCode: 0 })
  }])
  // The rollout that died at its second replica: what that replica said, and the first one, which was running when it was removed.
  events.push([Date.parse(webBad.completed_at), () => {
    const at = Date.parse(webBad.completed_at)
    addArchive(apps.get('web'), {
      deployment: webBad, replica: 2, container: `shipwick_web_${webBad.sequence}_2`, reason: 'deployment_failed', exitCode: 1, endedAtMs: at - 2400,
      lines: lines(`shipwick_web_${webBad.sequence}_2`, 2, at - 2400, '> web@2024.12.1 start\n> node server.js\n\nError: listen EADDRINUSE: address already in use :::3000\n    at Server.setupListenHandle [as _listen2] (node:net:1908:16)\n    at listenInCluster (node:net:1965:12)\nnpm error code 1'.replace('\n\n', '\n')),
    })
    past('web', webBad, 1, startedAt - at + 300, 'deployment_failed', { count: 9, said: [['stdout', 'level=info msg="received SIGTERM, shutting down"']] })
  }])
  // Replica 3 keeps running out of memory. The first time it had been up for days: only its last lines were kept.
  events.push([startedAt - 9 * MINUTE, () => past('web', webActive, 3, 9 * MINUTE, 'oom_killed', { count: 2300, exitCode: 137, oomKilled: true })])
  events.push([startedAt - 4 * MINUTE, () => past('web', webActive, 3, 4 * MINUTE, 'oom_killed', { count: 180, exitCode: 137, oomKilled: true })])
  events.push([startedAt - 20 * SECOND, () => past('web', webActive, 3, 20 * SECOND, 'oom_killed', { count: 150, exitCode: 137, oomKilled: true })])

  // The worker's second replica never turns healthy and is restarted for it, over and over.
  const worker = of('worker').find(d => d.status === 'ACTIVE')
  for (const [i, ago] of [12 * MINUTE, 11 * MINUTE + 20 * SECOND, 10 * MINUTE + 40 * SECOND, 9 * MINUTE + 55 * SECOND, 9 * MINUTE + 5 * SECOND, 34 * SECOND].entries()) {
    events.push([startedAt - ago, () => past('worker', worker, 2, ago, 'unhealthy', { count: i === 0 ? 240 : 14 })])
  }

  // Stopped on request six days ago.
  const docs = of('docs')[0]
  events.push([startedAt - 6 * DAY, () => past('docs', docs, 1, 6 * DAY, 'stopped', { count: 30, exitCode: 0 })])

  // Never deployed successfully: the replica of the second attempt died of a missing file, and said so.
  const legacy = of('legacy-cron')[1]
  events.push([Date.parse(legacy.completed_at), () => {
    addArchive(apps.get('legacy-cron'), {
      deployment: legacy, replica: 1, container: `shipwick_legacy-cron_${legacy.sequence}_1`, reason: 'deployment_failed', exitCode: 127, endedAtMs: Date.parse(legacy.completed_at) - 900,
      lines: lines(`shipwick_legacy-cron_${legacy.sequence}_1`, 1, Date.parse(legacy.completed_at) - 900, 'Starting legacy-cron 8\n/entrypoint.sh: line 4: /usr/local/bin/cron-runner: not found'),
    })
  }])

  for (const run of seededRuns) {
    events.push([Date.parse(run.finished_at), () => {
      // The failed reindex wrote far more than a run's record keeps.
      if (run.job === 'run' && run.status === 'failed') {
        const container = `shipwick_my-api_job_run_${run.id}`
        const at = Date.parse(run.finished_at)
        const body = Array.from({ length: 640 }, (_, i) => ({ replica: 0, container, stream: 'stdout', time: iso(at - (644 - i) * 125), message: `Reindexing customers... batch ${i + 1}/640 (${(i + 1) * 20} of 12,800)` }))
        return archiveRun(apps.get(run.application), run, [...body, ...lines(container, 0, at, 'Reindexing customers... done (12,408)\nReindexing invoices...\nError: index invoices_v2 is read-only\nexit status 3')])
      }
      return archiveRun(apps.get(run.application), run)
    }])
  }

  // Entries are numbered in the order their containers ended.
  for (const [, add] of events.sort(([a], [b]) => a - b)) add()
}

/** What the archive holds and may hold: `log_archive` on GET /server. */
function logArchiveStatus() {
  if (!LOG_ARCHIVE) return { enabled: false, entries: 0, bytes: 0, max_bytes: 0, retention_days: 14 }
  // The fixtures stand for a server that has been running for a while: what they add is small next to it.
  return { enabled: true, entries: 248 + logArchive.length, bytes: 61 * 1024 ** 2 + logArchive.reduce((n, e) => n + e.stored_bytes, 0), max_bytes: 1024 ** 3, retention_days: 14 }
}

/** A positive number given as a query parameter; 0 when it is absent. */
function idParam(url, name) {
  const raw = url.searchParams.get(name)
  if (raw === null || raw === '') return 0
  if (!/^\d+$/.test(raw) || Number(raw) < 1) throw new HttpError(400, 'INVALID_REQUEST', `${name} must be a positive number`)
  return Number(raw)
}

/** A time given as a query parameter, in milliseconds; null when it is absent. */
function timeParam(url, name) {
  const raw = url.searchParams.get(name)
  if (raw === null || raw === '') return null
  const at = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(raw) ? Date.parse(raw) : Number.NaN
  if (Number.isNaN(at)) throw new HttpError(400, 'INVALID_REQUEST', `${name} must be a time in RFC 3339, e.g. ${iso(Date.now()).replace(/\.\d+Z$/, 'Z')}`)
  return at
}

/** `deployment`, `replica` and `run` of the two endpoints that narrow by where output came from. */
function logSource(url, app) {
  const deployment = idParam(url, 'deployment')
  const replica = intParam(url, 'replica', 0, 1, 1000)
  const run = idParam(url, 'run')
  if (deployment !== 0 && deployments.get(deployment)?.application !== app.name) throw new HttpError(404, 'NOT_FOUND', 'not found')
  return { deployment, replica, run }
}

/** How many containers' output one search request reads: what stands for the agent's bound of bytes. */
const SEARCH_SOURCES = 6

/**
 * One page of a search: the replicas that exist, from the highest index down,
 * then the archive from its newest entry; within each, the newest line first.
 * A request that has read its share of sources stops and says where to go on,
 * with whatever it found by then — nothing at all, sometimes.
 */
function searchLogs(app, { text, since, until, deployment, replica, run, limit, cursor }) {
  const needle = text.toLowerCase()
  const active = app.active_deployment_id
  const sources = []
  // A run, or a deployment that is not the active one, is in the archive only.
  if (run === 0 && (deployment === 0 || deployment === active)) {
    for (const c of [...app.containers].sort((a, b) => b.replica - a.replica || b.deployment_id - a.deployment_id)) {
      if (replica !== 0 && c.replica !== replica) continue
      if (deployment !== 0 && c.deployment_id !== deployment) continue
      const d = deployments.get(c.deployment_id)
      const from = archivedUntil.get(c.name) ?? ''
      sources.push({ key: `r${c.deployment_id}-${c.replica}`, meta: { archive_id: null, deployment_id: d?.id ?? null, deployment: d?.sequence ?? 0, job: '', run_id: null }, lines: (logBuffers.get(app.name) ?? []).filter(line => line.container === c.name && line.time > from) })
    }
  }
  for (const entry of logArchive.filter(e => e.application === app.name).sort((a, b) => b.id - a.id)) {
    if (replica !== 0 && entry.replica !== replica) continue
    if (deployment !== 0 && entry.deployment_id !== deployment) continue
    if (run !== 0 && entry.run_id !== run) continue
    sources.push({ key: `a${entry.id}`, meta: { archive_id: entry.id, deployment_id: entry.deployment_id, deployment: entry.deployment, job: entry.job, run_id: entry.run_id }, lines: entry.output })
  }

  let at = 0
  let offset = 0
  if (cursor !== '') {
    const m = /^([ar][\d-]+)\.(\d+)$/.exec(cursor)
    at = m ? sources.findIndex(s => s.key === m[1]) : -1
    // The source a cursor names may have aged out since: the search goes on with what follows it.
    if (m && at === -1 && m[1].startsWith('a')) at = sources.findIndex(s => s.key.startsWith('a') && Number(s.key.slice(1)) < Number(m[1].slice(1)))
    if (!m) throw new HttpError(400, 'INVALID_REQUEST', 'cursor must be the next of an earlier answer')
    if (at === -1) at = sources.length
    else if (sources[at].key === m[1]) offset = Number(m[2])
  }

  const found = []
  let read = 0
  let bytes = 0
  let next = ''
  for (; at < sources.length; at++, offset = 0) {
    if (read >= SEARCH_SOURCES) {
      next = `${sources[at].key}.0`
      break
    }
    const source = sources[at]
    read++
    bytes += source.lines.reduce((n, line) => n + Buffer.byteLength(line.message) + 1, 0)
    const matches = source.lines
      .filter(line => (needle === '' || line.message.toLowerCase().includes(needle)) && (since === null || Date.parse(line.time) >= since) && (until === null || Date.parse(line.time) <= until))
      .reverse()
    const room = limit - found.length
    for (const line of matches.slice(offset, offset + room)) found.push({ ...line, ...source.meta })
    if (matches.length - offset > room) {
      next = `${source.key}.${offset + room}`
      break
    }
    if (found.length >= limit && at + 1 < sources.length) {
      next = `${sources[at + 1].key}.0`
      break
    }
  }
  return { lines: found, next, sources: read, bytes }
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

function sampleContainer(app, container, limits) {
  let s = metricState.get(container.name)
  if (!s) {
    const memCeiling = limits.memory_bytes || 768 * 1024 ** 2
    s = { cpu: 5 + Math.random() * 30, mem: memCeiling * (0.25 + Math.random() * 0.3), phase: Math.random() * Math.PI * 2 }
    metricState.set(container.name, s)
  }
  const cpuCeiling = (limits.cpu || 2) * 100
  s.phase += 0.12
  // A slow wave plus noise, with the occasional burst.
  const burst = Math.random() < 0.05 ? cpuCeiling * 0.35 : 0
  s.cpu = clamp(s.cpu + (Math.random() - 0.5) * 9 + Math.sin(s.phase) * 2.5 + burst, 0.3, cpuCeiling)
  if (burst === 0 && s.cpu > cpuCeiling * 0.6) s.cpu *= 0.8
  const memCeiling = limits.memory_bytes || 1024 ** 3
  s.mem = clamp(s.mem + (Math.random() - 0.47) * memCeiling * 0.012, memCeiling * 0.08, memCeiling * 0.97)
  return replicaSample(container, limits, round1(s.cpu), Math.round(s.mem))
}

function replicaSample(container, limits, cpu, mem) {
  return {
    replica: container.replica,
    container: container.name,
    cpu_percent: cpu,
    // Percent of one core, like cpu_percent: a limit of 0.5 cores is 50. 0 = unlimited.
    cpu_limit_percent: Math.round((limits.cpu || 0) * 100),
    memory_bytes: mem,
    memory_limit_bytes: limits.memory_bytes || 0,
  }
}

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v))
const round1 = v => Math.round(v * 10) / 10

function metrics(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const limits = active ? active.spec.resources : {}
  // A replica that is not running reports zeros; it is never an error.
  const replicas = app.containers
    .map(c => (c.state === 'running' ? sampleContainer(app, c, limits) : replicaSample(c, limits, 0, 0)))
  return {
    application: app.name,
    collected_at: iso(Date.now()),
    cpu_percent: round1(replicas.reduce((sum, r) => sum + r.cpu_percent, 0)),
    cpu_limit_percent: replicas.reduce((sum, r) => sum + r.cpu_limit_percent, 0),
    memory_bytes: replicas.reduce((sum, r) => sum + r.memory_bytes, 0),
    memory_limit_bytes: replicas.reduce((sum, r) => sum + r.memory_limit_bytes, 0),
    replicas,
  }
}

// ---------------------------------------------------------------------------
// Metrics history: what the sampler would have written down, generated on the
// fly and deterministic per bucket, so a refresh redraws the same past.
// ---------------------------------------------------------------------------

const HISTORY_WINDOWS = { '1h': [HOUR, 30 * SECOND, '30s'], '24h': [DAY, 5 * MINUTE, '5m'], '7d': [7 * DAY, HOUR, '1h'] }

/** A stable pseudo-random number in [0, 1) for a (string, number) pair. */
function noise(key, n) {
  let h = 2166136261
  for (const ch of `${key}:${n}`) h = Math.imul(h ^ ch.charCodeAt(0), 16777619)
  return ((h >>> 0) % 10007) / 10007
}

function metricsHistory(app, since) {
  const active = requireActive(app)
  const [windowMs, stepMs, step] = HISTORY_WINDOWS[since]
  const now = Date.now()
  const start = now - windowMs
  const limits = active.spec.resources
  const cpuCeiling = (limits.cpu || 2) * 100
  const memCeiling = limits.memory_bytes || 768 * 1024 ** 2
  // Samples belong to the application, across its deployments: they start with its first one.
  const activeSince = Date.parse(app.created_at)
  // The application was stopped at some point: nothing was sampled after that.
  const stoppedAt = app.desired_state === 'stopped' ? Date.parse(app.updated_at) : Infinity
  // One outage of the agent itself, a while ago: every series has the same hole.
  const outageStart = now - windowMs * 0.35
  const outageEnd = outageStart + Math.max(2 * stepMs, windowMs * 0.03)

  const series = []
  for (let r = 1; r <= active.spec.replicas; r++) {
    const container = app.containers.find(c => c.replica === r)
    const key = `${app.name}/${r}`
    const points = []
    let cpu = 8 + noise(key, -1) * 25
    let mem = memCeiling * (0.3 + noise(key, -2) * 0.25)
    for (let t = Math.ceil(start / stepMs) * stepMs; t < now; t += stepMs) {
      const bucket = t / stepMs
      cpu = clamp(cpu + (noise(key, bucket) - 0.5) * 8 + Math.sin(bucket / 9) * 2, 0.5, cpuCeiling * 0.92)
      mem = clamp(mem + (noise(key, bucket + 0.5) - 0.48) * memCeiling * 0.01, memCeiling * 0.1, memCeiling * 0.95)
      if (t + stepMs <= activeSince || t >= stoppedAt) continue
      if (t >= outageStart && t < outageEnd) continue
      // A flapping replica (web's third) is down for a stretch every so often; a crash-looping one barely runs.
      if (container?.oom_killed && Math.floor(bucket / 6) % 3 === 2) continue
      if (container?.crash_loop && noise(key, bucket + 0.25) < 0.7) continue
      points.push({ at: iso(t), cpu_percent: round1(container?.crash_loop ? cpu * 0.2 : cpu), memory_bytes: Math.round(mem) })
    }
    if (points.length > 0) series.push({ replica: r, points })
  }
  return {
    application: app.name,
    since: iso(start),
    step,
    series,
    limits: { cpu: limits.cpu || 0, memory_bytes: limits.memory_bytes || 0 },
  }
}

// ---------------------------------------------------------------------------
// Jobs: the hook, the schedule and one-off commands, all as runs
// ---------------------------------------------------------------------------

const JOB_NAME = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

function addRun(app, { job, kind, command, deploymentId = null, startedAtMs, durationMs = null, status, exitCode = null, output = '' }) {
  const run = {
    id: nextRunId++,
    application: app.name,
    job,
    kind,
    command,
    status,
    exit_code: exitCode,
    deployment_id: deploymentId,
    started_at: iso(startedAtMs),
    finished_at: durationMs === null ? null : iso(startedAtMs + durationMs),
    output,
  }
  runs.set(run.id, run)
  return run
}

/** Runs of one application, newest first; `job` narrows them (a name, "pre-deploy" or "run"). The last 50 per job are kept. */
function runsOf(app, job = '') {
  const all = [...runs.values()].filter(r => r.application === app.name).sort((a, b) => b.started_at.localeCompare(a.started_at) || b.id - a.id)
  const seen = new Map()
  const kept = all.filter((r) => {
    const n = (seen.get(r.job) ?? 0) + 1
    seen.set(r.job, n)
    if (n > 50) runs.delete(r.id)
    return n <= 50
  })
  return job === '' ? kept : kept.filter(r => r.job === job)
}

/** A run as it appears in lists: without its output. */
function runView(run) {
  if (!run) return null
  const { output: _output, ...rest } = run
  return rest
}

/**
 * Starts a run that finishes by itself after a few seconds. The outcome comes
 * from the command, so it can be provoked: "fail" fails, "timeout" times out.
 */
function startRun(app, active, { job, kind, command, timeout }) {
  const run = addRun(app, { job, kind, command, deploymentId: active.id, startedAtMs: Date.now(), status: 'running' })
  const text = command.join(' ')
  const outcome = /fail|false/.test(text) ? 'failed' : /timeout|sleep/.test(text) ? 'timed_out' : 'succeeded'
  const wait = outcome === 'timed_out' ? 6 * SECOND : 2500 + (run.id % 3) * 800
  setTimeout(() => finishRun(app, run, outcome, timeout), wait).unref()
  return run
}

/** Settles a run: exit code, output, and the `job` event a failure leaves behind (a hook's failure is the deployment's, not an event). */
function finishRun(app, run, outcome, timeout = '10m0s', hook = false) {
  if (!runs.has(run.id) || run.finished_at) return
  run.finished_at = iso(Date.now())
  run.status = outcome
  const argv = run.command.join(' ')
  const subject = run.job === 'run' ? `Command ${run.command[0] ?? ''}` : `Job ${run.job}`
  if (outcome === 'succeeded') {
    run.exit_code = 0
    run.output = run.job === 'cleanup-sessions' ? `Deleted ${1 + (run.id % 7)} expired sessions.` : run.job === 'nightly-report' ? 'Collecting invoices for the last 24h...\n912 invoices, 337 customers\nReport written.' : run.job === 'pre-deploy' ? 'No pending migrations.' : `$ ${argv}\nok`
  }
  else if (outcome === 'failed') {
    run.exit_code = 1
    run.output = `$ ${argv}\nError: the command failed\nexit status 1`
    if (!hook) addAppEvent(app.name, 'warn', 'job', `${subject} failed (exit 1)`)
  }
  else {
    run.exit_code = null
    run.output = `$ ${argv}\nstill working...`
    if (!hook) addAppEvent(app.name, 'warn', 'job', `${subject} timed out after ${timeout.replace(/(?<!\d)0[ms]/g, '') || timeout}`)
  }
  archiveRun(app, run)
}

// --- cron, five fields, UTC ---------------------------------------------------

function cronField(field, min, max) {
  const set = new Set()
  for (const part of field.split(',')) {
    const [range, stepText] = part.split('/')
    const step = stepText ? Number(stepText) : 1
    let lo
    let hi
    if (range === '*') [lo, hi] = [min, max]
    else if (range.includes('-')) [lo, hi] = range.split('-').map(Number)
    else [lo, hi] = [Number(range), stepText ? max : Number(range)]
    if (![lo, hi, step].every(Number.isInteger) || step < 1) return null
    for (let v = lo; v <= hi; v += step) set.add(max === 7 && v === 7 ? 0 : v)
  }
  return set
}

/** The next firing after `fromMs`, in epoch milliseconds; null when the expression never fires within a year. */
function cronNext(expr, fromMs) {
  const fields = expr.trim().split(/\s+/)
  if (fields.length !== 5) return null
  const [minute, hour, dom, month, dow] = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]].map(([lo, hi], i) => cronField(fields[i], lo, hi))
  if (!minute || !hour || !dom || !month || !dow) return null
  const anyDom = fields[2] === '*'
  const anyDow = fields[4] === '*'
  let t = Math.floor(fromMs / MINUTE) * MINUTE + MINUTE
  const end = t + 366 * DAY
  while (t < end) {
    const d = new Date(t)
    if (!month.has(d.getUTCMonth() + 1)) {
      t = Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1)
      continue
    }
    // Vixie cron: when both day fields are restricted, either one matching is enough.
    const dayOk = anyDom && anyDow ? true : anyDom ? dow.has(d.getUTCDay()) : anyDow ? dom.has(d.getUTCDate()) : dom.has(d.getUTCDate()) || dow.has(d.getUTCDay())
    if (!dayOk) {
      t = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() + 1)
      continue
    }
    if (!hour.has(d.getUTCHours())) {
      t = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), d.getUTCHours() + 1)
      continue
    }
    if (minute.has(d.getUTCMinutes())) return t
    t += MINUTE
  }
  return null
}

function jobsView(app) {
  const active = requireActive(app)
  return (active.spec.jobs ?? []).map((j) => {
    const next = app.desired_state === 'stopped' ? null : cronNext(j.schedule, Date.now())
    return { name: j.name, schedule: j.schedule, command: j.command, timeout: j.timeout, last_run: runView(runsOf(app, j.name)[0]), next_run_at: next === null ? null : iso(next) }
  })
}

/** The scheduler's tick: a job fires once for a minute its schedule names, one run at a time, never while a deployment holds the application. */
function runSchedules() {
  const minute = Math.floor(Date.now() / MINUTE) * MINUTE
  for (const app of apps.values()) {
    const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
    for (const j of active?.spec.jobs ?? []) {
      const key = `${app.name}/${j.name}`
      const seen = scheduleSeen.get(key) ?? minute
      scheduleSeen.set(key, minute)
      if (app.desired_state !== 'running' || inFlight(app.name)) continue
      const due = cronNext(j.schedule, seen)
      if (due === null || due > Date.now()) continue
      if (!runsOf(app, j.name).some(r => r.status === 'running')) startRun(app, active, { job: j.name, kind: 'scheduled', command: j.command, timeout: j.timeout })
    }
  }
}

function validateCommand(command) {
  if (command === undefined) throw new HttpError(400, 'INVALID_REQUEST', 'command is required, e.g. {"command": ["rails", "db:migrate"]}')
  if (!Array.isArray(command)) throw new HttpError(400, 'INVALID_REQUEST', 'command must be a list of arguments, not a shell string')
  if (command.length === 0) throw new HttpError(400, 'INVALID_REQUEST', 'command must not be empty')
  if (command.length > 256) throw new HttpError(400, 'INVALID_REQUEST', `command has too many arguments (${command.length}); at most 256`)
  for (const [i, arg] of command.entries()) {
    if (typeof arg !== 'string') throw new HttpError(400, 'INVALID_REQUEST', `command[${i}] must be a string`)
    if (Buffer.byteLength(arg) > 4096) throw new HttpError(400, 'INVALID_REQUEST', `command[${i}] is longer than 4 KB`)
    if (/[\n\0]/.test(arg)) throw new HttpError(400, 'INVALID_REQUEST', `command[${i}] must not contain a newline or NUL`)
  }
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

const TOKEN_NAME = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

function addToken(name, role, createdAtMs, lastUsedAtMs, value = newTokenValue(), { applications = [], expiresAtMs = null } = {}) {
  const t = {
    id: nextTokenId++,
    name,
    role,
    created_at: iso(createdAtMs),
    last_used_at: lastUsedAtMs === null ? null : iso(lastUsedAtMs),
    ...(BEFORE_06 ? {} : { applications: [...applications].sort(), expires_at: expiresAtMs === null ? null : iso(expiresAtMs) }),
    value,
  }
  tokens.set(name, t)
  return t
}

/** `swk_` + 32 random bytes as unpadded base64url, the agent's format; the prefix is not part of the secret. */
function newTokenValue() {
  return `swk_${randomBytes(32).toString('base64url')}`
}

function tokenView(t) {
  const { value: _value, ...rest } = t
  return rest
}

/** "2026-10-02 at 12:00 UTC", as the agent writes a moment into a sentence. */
const moment = at => `${at.slice(0, 10)} at ${at.slice(11, 16)} UTC`

/** An identity as the agent hands it out: an agent before 0.6 knows a name and a role. */
const identityOf = (name, role, kind, applications, expiresAt) => (BEFORE_06 ? { name, role } : { name, role, kind, applications, expires_at: expiresAt })

/** A credential the agent knows and no longer serves: told to its holder only, and never counted as a failed authentication. */
class Ended extends Error {
  constructor(code, message, details) {
    super(message)
    this.code = code
    this.details = details
  }
}

function refuseExpiredToken(name, expiresAt) {
  if (expiresAt && Date.parse(expiresAt) <= Date.now()) {
    throw new Ended('TOKEN_EXPIRED', `token ${name} expired on ${moment(expiresAt)}; an admin creates a new one with: shipwick token create`, { name, expired_at: expiresAt })
  }
}

/**
 * Who a bearer value is: the configured token, one created here and not
 * revoked since, or the session of a person who signed in. Null for a value
 * nobody issued; throws Ended for one that was valid and is over.
 */
function identify(authorization) {
  const value = typeof authorization === 'string' && authorization.startsWith('Bearer ') ? authorization.slice(7) : ''
  if (value === '') return null
  if (value === TOKEN) {
    // Root never expires and is never limited; a lesser configured token may be both.
    if (ROLE === 'admin') return identityOf('root', 'admin', 'token', [], null)
    if (!BEFORE_06) refuseExpiredToken(TOKEN_NAME_OF_ROLE, TOKEN_EXPIRES_AT)
    return identityOf(TOKEN_NAME_OF_ROLE, ROLE, 'token', ROLE === 'deploy' ? TOKEN_LIMIT : [], TOKEN_EXPIRES_AT)
  }
  for (const t of tokens.values()) {
    if (t.value === value) {
      // A refused use of an expired token does not move last_used_at.
      if (!BEFORE_06) refuseExpiredToken(t.name, t.expires_at)
      // Kept to the minute, as the agent does: it says whether the token is still in use.
      t.last_used_at = iso(Math.floor(Date.now() / MINUTE) * MINUTE)
      return identityOf(t.name, t.role, 'token', t.applications ?? [], t.expires_at ?? null)
    }
  }
  return BEFORE_06 ? null : identifySession(value)
}

const roleCovers = (have, required) => ROLES.indexOf(have) >= ROLES.indexOf(required)

/** "token" or "session": what the caller presented, for the sentences that refuse it. */
const credential = who => (who.kind === 'user' ? 'session' : 'token')

function forbiddenMessage(who, required) {
  const need = required === 'deploy' ? 'deploying needs deploy or admin' : `this needs ${required}`
  return `this ${credential(who)} has the ${who.role} role; ${need}`
}

/** "my-api", "my-api and web", "a, b and c". */
const nameList = names => (names.length <= 1 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`)

/** A deploy token limited to some applications changes those and reads the rest. */
function refuseLimited(who, required, segments) {
  const limits = who.applications ?? []
  if (who.role !== 'deploy' || limits.length === 0 || required !== 'deploy') return
  const application = segments[0] === 'applications' ? segments[1] : undefined
  if (application === undefined) {
    throw new HttpError(403, 'TOKEN_LIMITED', `this ${credential(who)} is limited to ${nameList(limits)}, and this is not about one application`, { applications: limits })
  }
  if (!limits.includes(application)) {
    throw new HttpError(403, 'TOKEN_LIMITED', `this ${credential(who)} is limited to ${nameList(limits)}; it can read ${application} and not change it`, { applications: limits, application })
  }
}

const APPLICATION_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** The applications a token or a rule is limited to, as the agent stores them: sorted, each once. */
function validateLimits(role, applications) {
  if (applications === undefined || applications === null) return []
  if (!Array.isArray(applications)) throw new HttpError(400, 'INVALID_REQUEST', 'applications: must be a list of application names')
  if (applications.length === 0) return []
  if (role !== 'deploy') return null
  for (const name of applications) {
    if (typeof name !== 'string' || !APPLICATION_NAME.test(name)) throw new HttpError(400, 'INVALID_REQUEST', `applications: invalid name ${JSON.stringify(name)}: lowercase letters, digits and dashes only`)
  }
  const list = [...new Set(applications)].sort()
  if (list.length > 50) throw new HttpError(400, 'INVALID_REQUEST', 'a token can be limited to at most 50 applications')
  return list
}

// ---------------------------------------------------------------------------
// The audit trail: who did what, from where, and how it was answered
// ---------------------------------------------------------------------------

// Every route that changes something, with its action and what it names as its target.
const AUDITED = {
  'POST /applications/:p/deploy': ['deploy'],
  'POST /applications/:p/redeploy': ['redeploy'],
  'POST /applications/:p/rollback': ['rollback'],
  'POST /applications/:p/stop': ['stop'],
  'POST /applications/:p/start': ['start'],
  'DELETE /applications/:p': ['application.delete'],
  'POST /applications/:p/run': ['run'],
  'POST /applications/:p/jobs/:x/run': ['job.run', 3],
  'POST /applications/:p/images': ['image.upload'],
  'PUT /applications/:p/static': ['static.upload'],
  'POST /applications/:p/backups': ['backup.create'],
  'POST /applications/:p/backups/:x/verify': ['backup.verify', 3],
  'POST /applications/:p/backups/:x/restore': ['backup.restore', 3],
  'GET /applications/:p/backups/:x/volumes/:y/archive': ['backup.download', 3],
  'DELETE /applications/:p/backups/:x': ['backup.delete', 3],
  'GET /applications/:p/volumes/:x/archive': ['volume.download', 3],
  'PUT /applications/:p/volumes/:x/archive': ['volume.restore', 3],
  'DELETE /volumes/:p': ['volume.delete', 1],
  'POST /server/backups': ['server.backup'],
  'POST /server/backups/adopt': ['backup.adopt'],
  'PUT /secrets/:p': ['secret.set', 1],
  'DELETE /secrets/:p': ['secret.delete', 1],
  'PUT /registries/:p': ['registry.login', 1],
  'DELETE /registries/:p': ['registry.logout', 1],
  'PUT /certificates/:p': ['certificate.set', 1],
  'DELETE /certificates/:p': ['certificate.delete', 1],
  'POST /server/rotate-key': ['key.rotate'],
  'POST /tokens': ['token.create'],
  'DELETE /tokens/:p': ['token.revoke', 1],
  'POST /export': ['export.download'],
  'POST /exports': ['export.create'],
  'POST /import': ['import'],
  'POST /standby/pull': ['standby.pull'],
  'POST /standby/promote': ['standby.promote'],
  'DELETE /auth/session': ['signout'],
  'POST /access/rules': ['access.grant'],
  'DELETE /access/rules/:p': ['access.revoke'],
  'DELETE /access/sessions/:p': ['access.signout', 1],
  'POST /applications': ['deploy'],
  'PUT /tokens/:p': ['token.update', 1],
  'GET /audit/export': ['audit.export'],
}

function addAudit(entry) {
  audit.push({
    id: nextAuditId++,
    at: iso(Date.now()),
    actor: { kind: 'token', name: 'root' },
    address: '172.18.0.3',
    forwarded_for: '',
    action: '',
    application: '',
    target: '',
    outcome: 'ok',
    status: 200,
    code: '',
    detail: '',
    ...entry,
  })
  if (audit.length > 5000) audit.splice(0, audit.length - 5000)
}

/** The address a request came from, and what the proxy in front said it came from: the last entry, if it is an address. */
function addressesOf(req) {
  const address = String(req.socket.remoteAddress ?? '').replace(/^::ffff:/, '')
  const last = String(req.headers['x-forwarded-for'] ?? '').split(',').pop().trim()
  return { address, forwarded_for: /^[0-9a-fA-F:.]{3,45}$/.test(last) ? last : '' }
}

/** Writes the entry for an audited request once it has been answered: what a refusal or a failure was answered with, too. */
function auditWhenAnswered(req, res, route, segments, who) {
  const [action, targetAt] = AUDITED[route]
  res.once('finish', () => {
    const status = res.statusCode
    const location = String(res.getHeader('location') ?? '')
    const made = /\/(deployments|runs|backups|exports)\/(\d+)$/.exec(location)
    addAudit({
      actor: { kind: who.kind ?? 'token', name: who.name },
      ...addressesOf(req),
      action,
      // A document sent without a name in the address is recorded under the name it carries.
      application: res.auditApplication ?? (segments[0] === 'applications' ? segments[1] ?? '' : ''),
      target: res.auditTarget ?? (targetAt !== undefined ? segments[targetAt] ?? '' : ''),
      outcome: status >= 200 && status < 300 ? 'ok' : status === 403 ? 'refused' : 'failed',
      status,
      code: res.auditCode ?? '',
      detail: res.auditDetail ?? (made ? `${made[1].slice(0, -1)} ${made[2]}` : ''),
    })
  })
}

/** `since` of GET /audit: 7d, 24h, a date (the start of that day, UTC) or RFC 3339. */
function parseSince(raw) {
  const span = /^(\d+)(h|d)$/.exec(raw)
  if (span) return Date.now() - Number(span[1]) * (span[2] === 'h' ? HOUR : DAY)
  const at = Date.parse(/^\d{4}-\d{2}-\d{2}$/.test(raw) ? `${raw}T00:00:00Z` : raw)
  if (Number.isNaN(at)) throw new HttpError(400, 'INVALID_REQUEST', `since: invalid value ${JSON.stringify(raw)}: use 7d, 24h, a date such as 2026-09-01 or an RFC 3339 time`)
  return at
}

/** A trail of two weeks, so that there is more than one page of it. */
function seedAudit() {
  const at = minutesAgo => iso(startedAt - minutesAgo * MINUTE)
  const ci = { kind: 'token', name: 'ci' }
  const ada = { kind: 'user', name: BEFORE_07 ? 'ada@example.com' : personName('ada@example.com') }
  const office = '203.0.113.40'
  let deployment = 3
  // Older first: the nightly routine of a CI token, which fills the first pages.
  for (let day = 13; day >= 2; day--) {
    for (const [name, minute] of [['my-api', 610], ['web', 640], ['shop', 700], ['landing', 730], ['worker', 760]]) {
      addAudit({ at: at(day * 1440 - minute), actor: ci, address: '172.18.0.1', forwarded_for: '198.51.100.24', action: name === 'landing' ? 'static.upload' : 'redeploy', application: name, status: name === 'landing' ? 200 : 202, detail: name === 'landing' ? '' : `deployment ${deployment++}` })
    }
  }
  const recent = [
    [1400, { action: 'token.create', target: 'web-ci', status: 201, forwarded_for: office, detail: `role deploy, limited to landing web, expires ${iso(startedAt + 9 * DAY).replace(/\.\d+Z$/, 'Z')}` }],
    [1310, { actor: { kind: 'token', name: 'web-ci' }, action: 'redeploy', application: 'worker', outcome: 'refused', status: 403, code: 'TOKEN_LIMITED', forwarded_for: '198.51.100.24' }],
    [1180, { action: 'secret.set', target: 'STRIPE_KEY', status: 204, forwarded_for: office }],
    [960, { action: 'access.grant', target: 'group:developers', status: 201, forwarded_for: office, detail: 'role deploy, limited to my-api web' }],
    [955, { action: 'access.grant', target: 'ada@example.com', status: 201, forwarded_for: office, detail: 'role admin' }],
    [610, { actor: ada, action: 'signin', status: 200, forwarded_for: office, detail: `role admin, expires ${iso(startedAt).replace(/\.\d+Z$/, 'Z')}` }],
    [596, { actor: { kind: 'user', name: 'mallory@elsewhere.org' }, action: 'signin', outcome: 'refused', status: 403, code: 'ACCESS_NOT_GRANTED', forwarded_for: '192.0.2.144' }],
    [480, { actor: ada, action: 'backup.create', application: 'postgres', status: 202, forwarded_for: office, detail: 'backup 12' }],
    [420, { actor: ada, action: 'stop', application: 'docs', status: 200, forwarded_for: office }],
    [300, { actor: { kind: 'token', name: 'viewer' }, action: 'stop', application: 'web', outcome: 'refused', status: 403, code: 'FORBIDDEN', forwarded_for: '203.0.113.201' }],
    [190, { actor: ci, action: 'redeploy', application: 'web', status: 202, forwarded_for: '198.51.100.24', detail: `deployment ${deployment++}` }],
    [120, { actor: ci, action: 'job.run', application: 'my-api', target: 'cleanup-sessions', outcome: 'failed', status: 409, code: 'JOB_ALREADY_RUNNING', forwarded_for: '198.51.100.24' }],
    [62, { actor: ci, action: 'run', application: 'my-api', status: 202, forwarded_for: '198.51.100.24', detail: 'run 14' }],
    [14, { action: 'certificate.set', target: '*.example.com', status: 200, forwarded_for: office }],
    // What 0.7 records on top: a token that was changed, and an export of this trail.
    ...(BEFORE_07
      ? []
      : [
          [840, { action: 'token.update', target: 'web-ci', status: 200, forwarded_for: office, detail: 'applications web -> landing web' }],
          [44, { actor: ada, action: 'audit.export', status: 200, forwarded_for: office, detail: 'csv, outcome refused failed, since ' + iso(startedAt - 7 * DAY).replace(/\.\d+Z$/, 'Z') + ', 4 entries' }],
        ]),
  ]
  for (const [minutes, entry] of recent.sort(([a], [b]) => b - a)) addAudit({ at: at(minutes), ...entry })
}

// ---------------------------------------------------------------------------
// Signing in through a provider: rules, sessions, and a stand-in for the provider
// ---------------------------------------------------------------------------

const SESSION_HOURS = 10
/** The accounts the stand-in provider knows, with the groups it reports for them. */
const IDP_ACCOUNTS = {
  'ada@example.com': ['developers', 'admins'],
  'grace@example.com': ['developers'],
  'sam@example.com': [],
  'mallory@elsewhere.org': [],
}

/** Accounts the stand-in provider knows and the agent does not take, with why: what a sign-in as one of them is answered. */
const IDP_REFUSED = {
  'intern@contoso.example': 'tenant_not_allowed',
  ...(NAME_CLAIM === 'email' ? {} : { 'scanner@example.com': 'name_missing' }),
}

/** The name the agent knows a person by: the address, or with another claim what that claim holds — here what stands before the @. */
const personName = email => (NAME_CLAIM === 'email' ? email : email.slice(0, email.indexOf('@')))
const isAddress = name => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(name)

function seedAccess() {
  const add = (kind, subject, role, applications, daysAgo, by) => accessRules.push({ id: nextRuleId++, kind, subject, role, applications, created_at: ago(daysAgo * DAY), created_by: by })
  if (NAME_CLAIM === 'email' || BEFORE_07) add('email', 'ada@example.com', 'admin', [], 16, 'root')
  else add('name', 'ada', 'admin', [], 16, 'root')
  add('group', 'developers', 'deploy', ['my-api', 'web'], 16, 'root')
  add('domain', 'example.com', 'read', [], 9, personName('ada@example.com'))
  // Somebody is signed in already, so that the list of sessions has a row.
  const grace = resolveAccess(personName('grace@example.com'), IDP_ACCOUNTS['grace@example.com'])
  sessions.set(newSessionValue(), { id: nextSessionId++, email: personName('grace@example.com'), groups: IDP_ACCOUNTS['grace@example.com'], role: grace.role, applications: grace.applications, created_at: ago(3 * HOUR), expires_at: iso(startedAt + 7 * HOUR), last_used_at: ago(12 * MINUTE), ended: null })
}

const newSessionValue = () => `sws_${randomBytes(32).toString('base64url')}`

/**
 * What the rules give a person: the most specific kind decides. An address,
 * then the groups (the highest role; limits add up, and a group without a
 * limit lifts it), then the domain. Null when no rule covers them.
 */
function resolveAccess(name, groups) {
  // A rule for the name itself comes first; rules by address and by domain apply to a name that reads as an address, in lowercase.
  const named = accessRules.find(r => r.kind === 'name' && r.subject === name)
  if (named) return { role: named.role, applications: named.applications }
  const email = isAddress(name) ? name.toLowerCase() : ''
  const own = email !== '' && accessRules.find(r => r.kind === 'email' && r.subject === email)
  if (own) return { role: own.role, applications: own.applications }
  const byGroup = accessRules.filter(r => r.kind === 'group' && groups.includes(r.subject))
  if (byGroup.length > 0) {
    const role = ROLES[Math.max(...byGroup.map(r => ROLES.indexOf(r.role)))]
    const same = byGroup.filter(r => r.role === role)
    const unlimited = role !== 'deploy' || same.some(r => r.applications.length === 0)
    return { role, applications: unlimited ? [] : [...new Set(same.flatMap(r => r.applications))].sort() }
  }
  const domain = email !== '' && accessRules.find(r => r.kind === 'domain' && email.endsWith(`@${r.subject}`))
  return domain ? { role: domain.role, applications: domain.applications } : null
}

/** How a rule for exactly this person is written for the CLI: the address itself, or `name:` and the name where it is not one. */
const whoOf = name => (isAddress(name) ? name : `name:${name}`)

const sameAccess = (a, b) => a.role === b.role && a.applications.join(' ') === b.applications.join(' ')

/** A session's identity; the rules are asked again on every request, and a session they no longer give the same ends with it. */
function identifySession(value) {
  const s = sessions.get(value)
  if (!s) return null
  if (!s.ended) {
    const now = resolveAccess(s.email, s.groups)
    if (!now) s.ended = { reason: 'rule_removed', message: `no rule on this server gives ${s.email} a role any more. An admin grants one with: shipwick access grant ${whoOf(s.email)} --role read` }
    else if (!sameAccess(now, s)) s.ended = { reason: 'access_changed', message: `what ${s.email} may do on this server was changed. Sign in again` }
  }
  if (s.ended) throw new Ended('SESSION_ENDED', s.ended.message, { name: s.email, reason: s.ended.reason })
  if (Date.parse(s.expires_at) <= Date.now()) {
    throw new Ended('SESSION_EXPIRED', `the session of ${s.email} expired on ${moment(s.expires_at)}. Sign in again`, { name: s.email, expired_at: s.expires_at })
  }
  s.last_used_at = iso(Math.floor(Date.now() / MINUTE) * MINUTE)
  return { name: s.email, role: s.role, kind: 'user', applications: s.applications, expires_at: s.expires_at }
}

/** Sessions that would be served right now: not expired, not ended, and still what the rules give. */
function liveSessions() {
  return [...sessions.values()].filter((s) => {
    if (s.ended || Date.parse(s.expires_at) <= Date.now()) return false
    const now = resolveAccess(s.email, s.groups)
    return now !== null && sameAccess(now, s)
  })
}

const subjectOf = rule => (rule.kind === 'group' ? `group:${rule.subject}` : rule.kind === 'domain' ? `*@${rule.subject}` : rule.kind === 'name' ? `name:${rule.subject}` : rule.subject)
const grantDetail = rule => `role ${rule.role}${rule.applications.length ? `, limited to ${rule.applications.join(' ')}` : ''}`

function signInConfig() {
  const base = `http://${HOST}:${PORT}`
  if (!SIGN_IN) return { configured: false, issuer: '', authorization_endpoint: '', client_id: '', scopes: [], redirect_uri: '' }
  return { configured: true, issuer: `${base}/mock-idp`, authorization_endpoint: `${base}/mock-idp/authorize`, client_id: 'shipwick', scopes: ['openid', 'email', 'profile'], redirect_uri: SIGN_IN_REDIRECT }
}

/** The stand-in provider: a page of accounts to sign in as, then the redirect back with a code. */
function mockProvider(url, res) {
  const q = url.searchParams
  const redirect = q.get('redirect_uri') ?? ''
  if (q.get('client_id') !== 'shipwick' || redirect !== SIGN_IN_REDIRECT || q.get('response_type') !== 'code' || !q.get('code_challenge') || !q.get('state')) {
    res.writeHead(400, { 'content-type': 'text/plain; charset=utf-8' })
    return res.end('mock provider: this authorization request is not one the dashboard would send')
  }
  const account = q.get('login_hint') || IDP_USER
  if (!account) {
    const notes = { tenant_not_allowed: 'an account of another tenant', name_missing: `an account without a ${NAME_CLAIM} claim` }
    const rows = [...Object.entries(IDP_ACCOUNTS), ...(BEFORE_07 ? [] : Object.keys(IDP_REFUSED).map(email => [email, []]))].map(([email, groups]) => {
      const next = new URL(url)
      next.searchParams.set('login_hint', email)
      return `<li><a href="${next.pathname}${next.search.replace(/&/g, '&amp;')}">${email}</a> <small>${notes[IDP_REFUSED[email]] ?? (groups.length ? `groups: ${groups.join(', ')}` : 'no groups')}</small></li>`
    }).join('')
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
    return res.end(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Mock sign-in provider</title><body style="font: 15px/1.5 system-ui; max-width: 30rem; margin: 12vh auto; padding: 0 1rem"><h1 style="font-size: 1.1rem">Mock sign-in provider</h1><p>This page stands in for the company's sign-in. Choose who signs in:</p><ul style="padding-left: 1.2rem">${rows}</ul><p><a href="${redirect}?error=access_denied&amp;state=${encodeURIComponent(q.get('state'))}">Cancel</a></p>`)
  }
  const code = randomBytes(24).toString('base64url')
  idpCodes.set(code, { challenge: q.get('code_challenge'), nonce: q.get('nonce') ?? '', email: account, groups: IDP_ACCOUNTS[account] ?? [] })
  const back = new URL(redirect)
  back.searchParams.set('code', code)
  back.searchParams.set('state', q.get('state'))
  res.writeHead(302, { location: back.toString() })
  return res.end()
}

/** POST /auth/exchange: redeems the provider's code and, if a rule covers who signed in, begins a session. */
function exchange(req, res, body) {
  const required = { code: /^[\x21-\x7E]{1,4096}$/, code_verifier: /^[A-Za-z0-9._~-]{43,128}$/, nonce: /^[A-Za-z0-9_-]{22,256}$/ }
  for (const [field, pattern] of Object.entries(required)) {
    if (typeof body[field] !== 'string' || !pattern.test(body[field])) throw new HttpError(400, 'INVALID_REQUEST', `${field} is required: what the sign-in began with, as the dashboard kept it`)
  }
  if (!SIGN_IN) throw new HttpError(409, 'SIGN_IN_NOT_CONFIGURED', 'signing in is not configured on this agent: it accepts API tokens only. An operator turns it on by setting SHIPWICK_OIDC_ISSUER')
  if (body.redirect_uri !== SIGN_IN_REDIRECT) throw new HttpError(400, 'INVALID_REQUEST', `redirect_uri must be ${SIGN_IN_REDIRECT}: the dashboard this agent is configured with`, { redirect_uri: SIGN_IN_REDIRECT })
  const failed = (reason, message) => new HttpError(401, 'SIGN_IN_FAILED', message, { reason })
  const known = idpCodes.get(body.code)
  idpCodes.delete(body.code)
  if (!known || createHash('sha256').update(body.code_verifier).digest('base64url') !== known.challenge) {
    throw failed('code_rejected', 'the sign-in provider refused the code: it was used before, has expired, or belongs to another sign-in. Sign in again')
  }
  if (known.nonce !== body.nonce) throw failed('nonce_mismatch', 'the sign-in provider\'s ID token answers another sign-in than this one. Sign in again')
  if (usedNonces.has(body.nonce)) throw failed('nonce_reused', 'this sign-in was completed before. Sign in again')
  usedNonces.add(body.nonce)

  // What the provider's answer itself rules out, before any rule is looked at.
  const refused = BEFORE_07 ? undefined : IDP_REFUSED[known.email]
  if (refused === 'tenant_not_allowed') {
    throw failed(refused, 'this account belongs to a Microsoft Entra tenant that may not sign in here. An operator adds the tenant\'s id to SHIPWICK_OIDC_TENANTS on the agent')
  }
  if (refused === 'name_missing') {
    throw new HttpError(401, 'SIGN_IN_FAILED', `the provider's ID token has no ${NAME_CLAIM} claim Shipwick can name this account by: it is missing, or holds something other than letters, digits and punctuation without spaces. SHIPWICK_OIDC_NAME_CLAIM on the agent says which claim that is, and SHIPWICK_OIDC_SCOPES has to ask for it`, { reason: refused, claim: NAME_CLAIM })
  }
  const name = BEFORE_07 ? known.email : personName(known.email)
  const access = resolveAccess(name, known.groups)
  const actor = { kind: 'user', name }
  if (!access) {
    addAudit({ actor, ...addressesOf(req), action: 'signin', outcome: 'refused', status: 403, code: 'ACCESS_NOT_GRANTED' })
    if (isAddress(name)) throw new HttpError(403, 'ACCESS_NOT_GRANTED', `${name} signed in, and no rule on this server gives that address a role. An admin grants one with: shipwick access grant ${name} --role read`, BEFORE_07 ? { email: name } : { email: name, name })
    throw new HttpError(403, 'ACCESS_NOT_GRANTED', `${name} signed in, and no rule on this server gives that name a role. An admin grants one with: shipwick access grant name:${name} --role read`, { email: name, name })
  }
  const value = newSessionValue()
  const expiresAt = iso(Date.now() + SESSION_HOURS * HOUR)
  sessions.set(value, { id: nextSessionId++, email: name, groups: known.groups, role: access.role, applications: access.applications, created_at: iso(Date.now()), expires_at: expiresAt, last_used_at: null, ended: null })
  addAudit({ actor, ...addressesOf(req), action: 'signin', status: 200, detail: `role ${access.role}${access.applications.length ? `, limited to ${access.applications.join(' ')}` : ''}, expires ${expiresAt.replace(/\.\d+Z$/, 'Z')}` })
  return sendJSON(res, 200, { session: value, identity: { name, role: access.role, kind: 'user', applications: access.applications, expires_at: expiresAt } })
}

// ---------------------------------------------------------------------------
// Volumes: the archive is a real (small) tar, built here byte by byte.
// ---------------------------------------------------------------------------

/** Contents of the postgres volume, relative to its mount point. */
const VOLUME_FILES = [
  ['PG_VERSION', '17\n'],
  ['postgresql.conf', '# mock data directory\nlisten_addresses = \'*\'\nmax_connections = 100\nshared_buffers = 128MB\n'],
  ['base/', null],
  ['base/1/', null],
  ['base/1/pg_filenode.map', 'mock relation map\n'],
  ['global/', null],
  ['global/pg_control', 'mock control file\n'],
]

function tarHeader(name, size, type) {
  const header = Buffer.alloc(512)
  header.write(name, 0, 100, 'utf8')
  header.write(type === '5' ? '0000755\0' : '0000644\0', 100, 8, 'ascii')
  header.write('0000000\0', 108, 8, 'ascii')
  header.write('0000000\0', 116, 8, 'ascii')
  header.write(`${size.toString(8).padStart(11, '0')}\0`, 124, 12, 'ascii')
  header.write(`${Math.floor(Date.now() / 1000).toString(8).padStart(11, '0')}\0`, 136, 12, 'ascii')
  header.write('        ', 148, 8, 'ascii')
  header.write(type, 156, 1, 'ascii')
  header.write('ustar\0', 257, 6, 'ascii')
  header.write('00', 263, 2, 'ascii')
  let sum = 0
  for (const byte of header) sum += byte
  header.write(`${sum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'ascii')
  return header
}

function tarArchive(files) {
  const blocks = []
  for (const [name, content] of files) {
    if (content === null) {
      blocks.push(tarHeader(name, 0, '5'))
      continue
    }
    const data = Buffer.from(content, 'utf8')
    blocks.push(tarHeader(name, data.length, '0'), data, Buffer.alloc((512 - (data.length % 512)) % 512))
  }
  blocks.push(Buffer.alloc(1024))
  return Buffer.concat(blocks)
}

function requireVolume(app, name) {
  if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name ?? '')) {
    throw new HttpError(400, 'INVALID_REQUEST', 'volume: lowercase letters, digits and dashes only')
  }
  const active = requireActive(app)
  const volume = (active.spec.volumes ?? []).find(v => v.name === name)
  if (!volume) throw new HttpError(404, 'NOT_FOUND', 'the application has no volume by that name')
  return volume
}

function formatSize(bytes) {
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(1).replace(/\.0$/, '')} ${units[unit]}`
}

// ---------------------------------------------------------------------------
// Validation of a submitted configuration, and the conflicts with what runs
// ---------------------------------------------------------------------------

const HOSTNAME = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/

/** Errors in the shape of pkg/spec's, all of them at once. */
const configError = fields => new HttpError(400, 'INVALID_CONFIG', 'invalid deploy.yaml', { fields })

/**
 * The document a deploy or a validation carries as its body: YAML, or JSON,
 * which YAML subsumes. A document that cannot be read is INVALID_CONFIG with
 * the parser's sentence and no fields, as the agent answers it.
 */
async function readConfig(req) {
  const chunks = []
  let size = 0
  for await (const chunk of req) {
    size += chunk.length
    if (size > 64 * 1024) throw new HttpError(413, 'INVALID_REQUEST', 'request body exceeds 64 KB')
    chunks.push(chunk)
  }
  const text = Buffer.concat(chunks).toString('utf8')
  let value
  try {
    value = text.trimStart().startsWith('{') ? JSON.parse(text) : parseYaml(text)
  }
  catch (error) {
    throw new HttpError(400, 'INVALID_CONFIG', String(error.message).startsWith('yaml:') ? error.message : `yaml: ${error.message}`)
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new HttpError(400, 'INVALID_CONFIG', 'yaml: the document must be a mapping of keys, starting with name')
  return value
}

const SPEC_KEYS = ['name', 'image', 'build', 'static', 'port', 'domain', 'aliases', 'redirects', 'replicas', 'env', 'health', 'resources', 'volumes', 'publish', 'entrypoint', 'command', 'user', 'pre_deploy', 'jobs', 'logging', 'path', 'proxy', 'backups', 'restart', 'deploy', 'init']

/** "512mb", "1gb" as bytes; null for anything else. */
function parseSize(text) {
  const m = /^(\d+(?:\.\d+)?)\s*(b|kb|mb|gb)$/i.exec(String(text).trim())
  return m ? Math.round(Number(m[1]) * { b: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3 }[m[2].toLowerCase()]) : null
}

/**
 * Turns a deploy.yaml into a stored spec: defaults filled in,
 * hostnames lowercased, the health block checked for exactly one kind. Far
 * from the agent's whole validation, but the same fields and wording where it
 * checks at all.
 */
function parseSpec(name, body, { validating = false } = {}) {
  const fields = []
  const problem = (field, message, expected) => fields.push({ field, message, ...(expected ? { expected } : {}) })
  for (const key of Object.keys(body)) if (!SPEC_KEYS.includes(key) && !(BEFORE_06 && key === 'init')) problem(key, `unknown field ${JSON.stringify(key)}`)
  if (BEFORE_06 && body.init !== undefined) problem('init', 'unknown field "init"')
  if (body.name === undefined || body.name === null || body.name === '') problem('name', 'is required', 'my-api')
  else if (typeof body.name !== 'string' || !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(body.name)) problem('name', `invalid value ${JSON.stringify(String(body.name))}`, 'lowercase letters, digits and dashes, e.g. my-api')
  else if (body.name !== name) {
    throw new HttpError(400, 'INVALID_REQUEST', `the config describes application ${JSON.stringify(body.name)} but the URL names ${JSON.stringify(name)}`)
  }
  const STATIC_EXCLUSIVE = 'does not apply to a static application: the proxy serves the files, there is no container'

  // A folder served by the proxy: a cleaned relative path, a domain, and nothing
  // that describes a container. Written as the folder alone, or as {dir, fallback}.
  let isStatic = false
  let fallback
  if (body.static !== undefined && body.static !== null && body.static !== '') {
    isStatic = true
    const form = typeof body.static === 'object' ? body.static : { dir: body.static }
    const dir = String(form.dir ?? '').trim().replace(/\\/g, '/').replace(/^\.\//, '').replace(/\/+$/, '')
    if (dir === '' && typeof body.static === 'object') problem('static.dir', 'is required: the folder the fallback page is in', 'dist')
    else if (dir === '' || dir === '.') problem('static', 'must name a folder below the project', 'dist')
    else if (dir.startsWith('/') || dir.split('/').includes('..')) problem('static', 'must be a relative path inside the project', 'dist')
    else body.static = dir
    if (form.fallback !== undefined && form.fallback !== null && form.fallback !== '') {
      fallback = String(form.fallback)
      if (fallback.startsWith('/') || fallback.split('/').includes('..')) problem('static.fallback', `invalid value ${JSON.stringify(fallback)}: must be relative to the folder`, 'index.html, 200.html')
      else if (!/^[\w.~-]+(\/[\w.~-]+)*$/.test(fallback)) problem('static.fallback', `invalid value ${JSON.stringify(fallback)}: name a file inside the folder, with letters, digits, dots, dashes, underscores and tildes between the slashes`, 'index.html, 200.html')
    }
    if (!body.domain) problem('domain', 'is required for a static application: the proxy serves the files at it', 'example.com')
    for (const key of ['image', 'build', 'port', 'env', 'health', 'resources', 'volumes', 'publish', 'entrypoint', 'command', 'user', 'logging', 'pre_deploy', 'jobs', 'backups']) {
      if (body[key] !== undefined && body[key] !== null) problem(key, STATIC_EXCLUSIVE)
    }
    if (body.replicas !== undefined && body.replicas !== 1) problem('replicas', STATIC_EXCLUSIVE)
    if (body.init !== undefined && body.init !== null && !BEFORE_06) problem('init', STATIC_EXCLUSIVE)
  }
  else if (body.init !== undefined && body.init !== null && typeof body.init !== 'boolean' && !BEFORE_06) problem('init', `invalid value ${JSON.stringify(body.init)}`, 'true or false')

  // Limits are written with a unit in deploy.yaml and stored in bytes.
  let resources = {}
  if (body.resources !== undefined && body.resources !== null) {
    const r = body.resources
    if (r.cpu !== undefined && (typeof r.cpu !== 'number' || r.cpu <= 0)) problem('resources.cpu', `invalid value ${JSON.stringify(r.cpu)}`, '0.5, 1, 2, ...')
    const bytes = r.memory_bytes ?? (r.memory === undefined ? undefined : parseSize(r.memory))
    if (r.memory !== undefined && bytes === null) problem('resources.memory', `invalid value ${JSON.stringify(r.memory)}`, '128mb, 512mb, 1gb, ...')
    resources = { ...(r.cpu ? { cpu: r.cpu } : {}), ...(bytes ? { memory_bytes: bytes } : {}) }
  }

  // Built where shipwick deploy runs: the document arrives with the shipwick.local/ image the agent was sent.
  let build
  if (!isStatic && body.build !== undefined && body.build !== null && body.build !== '') {
    const raw = typeof body.build === 'string' ? { context: body.build } : body.build
    build = { context: String(raw.context ?? '').trim() || undefined, dockerfile: String(raw.dockerfile ?? '').trim() || 'Dockerfile' }
    if (!build.context) problem('build.context', 'is required', '.')
    // Asked before the build: there is no image yet, and that is no fault of the document.
    if ((body.image === undefined || body.image === '') && !validating) {
      throw new HttpError(400, 'INVALID_REQUEST', 'image: this application is built where shipwick deploy runs and sent to the server first; the agent never builds. Run shipwick deploy from the project')
    }
    if (typeof body.image === 'string' && body.image !== '' && !isLocalImage(body.image)) problem('image', `must be tagged ${LOCAL_IMAGE_PREFIX}${name}:<tag> when build is set; shipwick deploy does that`, `${LOCAL_IMAGE_PREFIX}${name}:20260927-153000-a1b2`)
  }
  const unbuilt = validating && build && (body.image === undefined || body.image === '')
  if (!isStatic && !unbuilt && (typeof body.image !== 'string' || !IMAGE_PATTERN.test(body.image))) problem('image', 'is required', 'ghcr.io/org/app:1.0.0')

  // `${NAME}` in an env value or a basic-auth password is filled in from the secrets kept here; a name that is not stored refuses the whole document.
  const missingSecrets = value => [...new Set([...String(value ?? '').matchAll(/(?<!\$)\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g)].map(m => m[1]))].filter(n => !secrets.has(n)).sort()
  for (const [variable, value] of Object.entries(body.env ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    for (const secret of missingSecrets(value)) {
      problem(`env.${variable}`, `refers to \${${secret}}, which is not set where shipwick runs and not stored on the server`, `shipwick secret set ${secret}`)
    }
  }

  // `domain` and aliases may be a wildcard, one leading `*.`; a redirect is a single name.
  const validHost = (host, wildcards) => HOSTNAME.test(wildcards && host.startsWith('*.') ? host.slice(2) : host)
  const hosts = (field, list) => {
    if (list === undefined) return undefined
    if (!Array.isArray(list)) problem(field, 'must be a list of hostnames')
    else if (list.length > 20) problem(field, `too many (${list.length})`, 'at most 20')
    else if (!body.domain) problem(field, `requires domain: ${field} are served next to it`)
    return Array.isArray(list) ? list.map(h => String(h ?? '').trim().toLowerCase()) : []
  }
  const domain = body.domain ? String(body.domain).trim().toLowerCase() : undefined
  if (domain !== undefined && !validHost(domain, true)) problem('domain', `invalid value "${domain}": not a valid hostname`, 'api.example.com, *.example.com')
  const aliases = hosts('aliases', body.aliases)
  const redirects = hosts('redirects', body.redirects)
  if (domain?.startsWith('*.') && redirects?.length) problem('redirects', 'cannot be sent to a wildcard domain', 'domain: example.com, with the wildcard under aliases')
  const seen = new Map(domain ? [[domain, 'domain']] : [])
  for (const [field, list] of [['aliases', aliases], ['redirects', redirects]]) {
    for (const [i, host] of (list ?? []).entries()) {
      if (host === '') problem(`${field}[${i}]`, 'is empty')
      else if (!validHost(host, field === 'aliases')) problem(`${field}[${i}]`, `invalid value "${host}": not a valid hostname`)
      else if (seen.has(host)) problem(`${field}[${i}]`, `"${host}" is already listed under ${seen.get(host)}`)
      else seen.set(host, `${field}[${i}]`)
    }
  }

  // The part of the domain the application serves: /api and everything below it. `/` is the whole of it, which is no path.
  let path
  if (body.path !== undefined && body.path !== null && body.path !== '' && body.path !== '/') {
    path = String(body.path)
    if (!body.domain) problem('path', 'requires domain: a path is a part of it', 'domain: example.com')
    else if (!path.startsWith('/')) problem('path', `invalid value ${JSON.stringify(path)}: must start with /`, '/api, /docs/v2, ...')
    else if (path.endsWith('/')) problem('path', `invalid value ${JSON.stringify(path)}: must not end with /`, '/api, /docs/v2, ...')
    else if (!/^(\/[\w.~-]+)+$/.test(path)) problem('path', `invalid value ${JSON.stringify(path)}: use letters, digits, dots, dashes, underscores and tildes between the slashes`, '/api, /docs/v2, ...')
    else if (path.split('/').some(s => s === '.' || s === '..')) problem('path', `invalid value ${JSON.stringify(path)}: must not contain . or .. segments`, '/api, /docs/v2, ...')
  }

  // What the proxy does with the requests besides passing them on. Passwords are secrets: stored sealed, answered masked.
  let proxy
  if (body.proxy !== undefined && body.proxy !== null) {
    const p = body.proxy
    const within = p => path === undefined || p === path || String(p).startsWith(`${path}/`)
    if (!body.domain) problem('proxy', 'requires domain: it says what the proxy does with the requests for it', 'domain: example.com')
    for (const key of Object.keys(p)) if (!['strip_prefix', 'headers', 'basic_auth', 'redirects'].includes(key)) problem('proxy', `unknown field ${JSON.stringify(key)}`, 'strip_prefix, headers, basic_auth, redirects')
    if (p.strip_prefix && path === undefined) problem('proxy.strip_prefix', 'requires path: it is the prefix that is removed', 'path: /api')
    for (const [header, value] of Object.entries(p.headers ?? {})) {
      if (!/^[A-Za-z0-9-]+$/.test(header)) problem('proxy.headers', `invalid header name ${JSON.stringify(header)}`, 'letters, digits and dashes, e.g. X-Frame-Options')
      else if (typeof value !== 'string' || value === '') problem(`proxy.headers.${header}`, 'value must not be empty')
    }
    const accounts = (p.basic_auth ?? []).map((a, i) => {
      const field = `proxy.basic_auth[${i}]`
      if (!a?.username) problem(`${field}.username`, 'is required', 'admin')
      if (a?.path !== undefined && !within(a.path)) problem(`${field}.path`, `${JSON.stringify(a.path)} is outside path ${path}, which is all this application serves`, `${path}/admin`)
      const password = String(a?.password ?? '')
      const missing = missingSecrets(password)
      for (const secret of missing) problem(`${field}.password`, `refers to \${${secret}}, which is not set where shipwick runs and not stored on the server`, `shipwick secret set ${secret}`)
      const example = '${ADMIN_PASSWORD}, with the value in the environment, in --env-file or stored with shipwick secret set'
      if (password === '') problem(`${field}.password`, 'is required', example)
      else if (missing.length === 0 && !password.includes('${') && password.length < 8) problem(`${field}.password`, 'is too short: at least 8 characters', example)
      return { ...(a?.path ? { path: a.path } : {}), username: a?.username, password: MASK }
    })
    const pathRedirects = (p.redirects ?? []).map((r, i) => {
      const field = `proxy.redirects[${i}]`
      const status = r?.status ?? 308
      if (!r?.from) problem(`${field}.from`, 'is required', `${path ?? ''}/old`)
      else if (r.from === '/') problem(`${field}.from`, 'must be a path below /: redirecting everything would leave nothing to serve', `${path ?? ''}/old`)
      else if (!within(r.from)) problem(`${field}.from`, `${JSON.stringify(r.from)} is outside path ${path}, which is all this application serves`, `${path}/old`)
      if (!r?.to) problem(`${field}.to`, 'is required', '/new, https://example.org/new')
      else if (!/^(\/(?!\/)|https:\/\/)/.test(r.to)) problem(`${field}.to`, `invalid value ${JSON.stringify(r.to)}: must be a path starting with / or an https:// URL`, '/new, https://example.org/new')
      if (![301, 302, 307, 308].includes(status)) problem(`${field}.status`, `invalid value ${status}`, '301, 302, 307, 308')
      return { from: r?.from, to: r?.to, status }
    })
    proxy = {
      ...(p.strip_prefix ? { strip_prefix: true } : {}),
      ...(Object.keys(p.headers ?? {}).length ? { headers: p.headers } : {}),
      ...(accounts.length ? { basic_auth: accounts } : {}),
      ...(pathRedirects.length ? { redirects: pathRedirects } : {}),
    }
    if (Object.keys(proxy).length === 0) proxy = undefined
  }

  // How long a replica gets after SIGTERM, wherever one is stopped.
  let stopTimeout
  if (body.deploy?.stop_timeout !== undefined && body.deploy.stop_timeout !== null && body.deploy.stop_timeout !== '') {
    const grace = typeof body.deploy.stop_timeout === 'string' ? parseDuration(body.deploy.stop_timeout) : null
    if (isStatic) problem('deploy.stop_timeout', STATIC_EXCLUSIVE)
    else if (grace === null || grace < SECOND || grace > 10 * MINUTE) problem('deploy.stop_timeout', `invalid value ${JSON.stringify(body.deploy.stop_timeout)}`, '10s, 30s, 5m, ... (1s to 10m)')
    else stopTimeout = formatGoDuration(grace)
  }

  // Backups the agent takes by itself; a backup is an archive of volumes, so it needs some.
  let backupPlan
  if (!isStatic && body.backups !== undefined && body.backups !== null) {
    const b = body.backups
    const example = '"0 3 * * *" (minute hour day-of-month month day-of-week, in UTC)'
    if (typeof b !== 'object' || Array.isArray(b)) problem('backups', 'must be a block with a schedule', `schedule: ${example}`)
    else {
      if (!b.schedule) problem('backups.schedule', 'is required', example)
      else if (cronNext(String(b.schedule), Date.now()) === null) problem('backups.schedule', `invalid value ${JSON.stringify(b.schedule)}: not a five-field cron expression that ever fires`, example)
      if (b.keep !== undefined && (!Number.isInteger(b.keep) || b.keep < 1 || b.keep > 365)) problem('backups.keep', `invalid value ${b.keep}`, 'a number between 1 and 365')
      if (!body.volumes?.length) problem('backups', 'needs volumes: a backup is an archive of the application\'s volumes', 'volumes:\n    - name: data\n      path: /var/lib/postgresql/data')
      // How long the command before the archive gets; an hour when the block does not say.
      let beforeTimeout
      const timeoutExample = '30m, 2h, ... (1s to 24h)'
      if (b.before_timeout !== undefined && b.before_timeout !== null && b.before_timeout !== '' && !BEFORE_06) {
        const limit = typeof b.before_timeout === 'string' ? parseDuration(b.before_timeout) : null
        if (!b.before) problem('backups.before_timeout', 'needs backups.before: it is that command\'s time limit', timeoutExample)
        else if (limit === null || limit < SECOND || limit > 24 * HOUR) problem('backups.before_timeout', `invalid value ${JSON.stringify(b.before_timeout)}`, timeoutExample)
        else beforeTimeout = formatGoDuration(limit)
      }
      else if (b.before && !BEFORE_06) beforeTimeout = '1h0m0s'
      // Where the command runs: inside the replica unless the block says a container of its own.
      let beforeIn
      if (b.before_in !== undefined && b.before_in !== null && b.before_in !== '') {
        const whereExample = 'replica (inside the running replica), container (in a container of its own, which can be ended)'
        if (BEFORE_07) problem('backups', 'unknown field "before_in"', 'schedule, keep, before, before_timeout, stop')
        else if (!b.before) problem('backups.before_in', 'needs backups.before: it says where that command runs', whereExample)
        else if (b.before_in === 'container') beforeIn = 'container'
        else if (b.before_in !== 'replica') problem('backups.before_in', `invalid value ${JSON.stringify(String(b.before_in))}`, whereExample)
      }
      backupPlan = { schedule: String(b.schedule ?? ''), keep: b.keep ?? 7, ...(b.before ? { before: [].concat(b.before) } : {}), ...(beforeTimeout ? { before_timeout: beforeTimeout } : {}), ...(beforeIn ? { before_in: beforeIn } : {}), ...(b.stop ? { stop: true } : {}) }
    }
  }

  let health
  if (body.health !== undefined && body.health !== null) {
    const h = body.health
    const kinds = ['path', 'tcp', 'command'].filter(k => h[k] !== undefined && h[k] !== null && h[k] !== '')
    if (kinds.length === 0) problem('health', 'one of path, tcp or command is required', 'path: /health')
    else if (kinds.length > 1) problem('health', `${kinds.join(' and ')} are set; a health check is one of path, tcp or command`, 'path: /health for an HTTP application, tcp: 5432 for a database')
    if (h.tcp !== undefined && (!Number.isInteger(h.tcp) || h.tcp < 1 || h.tcp > 65535)) problem('health.tcp', `invalid value ${JSON.stringify(h.tcp)}`, 'the port your application listens on, e.g. 5432')
    if (h.command !== undefined) {
      const command = Array.isArray(h.command) ? h.command : [h.command]
      if (command.length === 0) problem('health.command', 'must not be empty', '["pg_isready", "-U", "postgres"]')
      else if (command.length > 64) problem('health.command', `too many arguments (${command.length})`, 'at most 64')
      for (const [i, arg] of command.entries()) if (arg === '') problem(`health.command[${i}]`, 'must not be empty', '["pg_isready", "-U", "postgres"]')
      h.command = command
    }
    if (h.start_period !== undefined && h.start_period !== '' && h.start_period !== 0) {
      const period = typeof h.start_period === 'string' ? parseDuration(h.start_period) : null
      if (period === null || period > 30 * MINUTE) problem('health.start_period', `invalid value ${JSON.stringify(h.start_period)}`, '30s, 1m, 5m, ... (up to 30m)')
      else h.start_period = formatGoDuration(period)
    }
    else delete h.start_period
    health = { ...(h.path ? { path: h.path } : {}), ...(h.tcp ? { tcp: h.tcp } : {}), ...(h.command ? { command: h.command } : {}), interval: h.interval ?? '10s', timeout: h.timeout ?? '3s', retries: h.retries ?? 3, ...(h.start_period ? { start_period: h.start_period } : {}) }
  }

  let publish
  if (body.publish !== undefined) {
    if (!Array.isArray(body.publish)) problem('publish', 'must be a list')
    else {
      publish = body.publish.map((p, i) => {
        const entry = { port: p?.port, host: p?.host ?? p?.port, ...(p?.address ? { address: String(p.address) } : {}), protocol: p?.protocol ?? 'tcp' }
        if (!Number.isInteger(entry.port) || entry.port < 1 || entry.port > 65535) problem(`publish[${i}].port`, 'is required', '1-65535')
        if (!Number.isInteger(entry.host) || entry.host < 1 || entry.host > 65535) problem(`publish[${i}].host`, 'must be a port', '1-65535')
        else if (entry.host === 80 || entry.host === 443) problem(`publish[${i}].host`, `port ${entry.host} belongs to the proxy`, 'another port, or a domain')
        if (entry.protocol !== 'tcp' && entry.protocol !== 'udp') problem(`publish[${i}].protocol`, `invalid value "${entry.protocol}"`, 'tcp or udp')
        return entry
      })
      if (publish.length > 0 && (body.deploy?.strategy ?? 'rolling') !== 'recreate') problem('deploy.strategy', 'must be recreate when ports are published', 'recreate')
      if (publish.length > 0 && (body.replicas ?? 1) !== 1) problem('replicas', 'must be 1 when ports are published', '1')
    }
  }

  // What validation finds comes first, as in the agent, where the document is
  // validated before its secrets are looked at; then the mask, which is what
  // the server shows in the place of a value and never a value; then the
  // secrets a reference names and the server does not hold.
  if (fields.some(f => !f.expected?.startsWith('shipwick secret set '))) throw configError(fields)
  const masks = BEFORE_07 ? [] : maskedFields(body)
  if (masks.length > 0) {
    throw configError(masks.map(field => ({ field, message: `${MASK} is what the server shows in the place of this value, not the value`, expected: 'the value itself, or ${NAME} with the value stored by shipwick secret set NAME' })))
  }
  if (fields.length > 0) throw configError(fields)
  const routing = { ...(path ? { path } : {}), ...(proxy ? { proxy } : {}) }
  const deploy = { strategy: body.deploy?.strategy ?? 'rolling', ...(stopTimeout ? { stop_timeout: stopTimeout } : {}) }
  if (isStatic) {
    return spec(name, '', {
      domain,
      ...(aliases?.length ? { aliases } : {}),
      ...(redirects?.length ? { redirects } : {}),
      static: { dir: body.static, ...(fallback ? { fallback } : {}) },
      ...routing,
      restart: body.restart ?? { policy: 'always' },
      deploy,
    })
  }
  return spec(name, unbuilt ? '' : body.image, {
    ...(build ? { build } : {}),
    ...(body.port ? { port: body.port } : {}),
    ...(domain ? { domain } : {}),
    ...(aliases?.length ? { aliases } : {}),
    ...(redirects?.length ? { redirects } : {}),
    replicas: body.replicas ?? 1,
    ...(body.env ? { env: Object.fromEntries(Object.keys(body.env).map(k => [k, MASK])) } : {}),
    ...(health ? { health } : {}),
    resources,
    ...(body.volumes?.length ? { volumes: body.volumes } : {}),
    ...(publish?.length ? { publish } : {}),
    ...(body.entrypoint ? { entrypoint: [].concat(body.entrypoint) } : {}),
    ...(body.command ? { command: [].concat(body.command) } : {}),
    ...(body.user ? { user: body.user } : {}),
    ...(body.logging ? { logging: body.logging } : {}),
    ...(body.init === true && !BEFORE_06 ? { init: true } : {}),
    ...routing,
    ...(backupPlan ? { backups: backupPlan } : {}),
    restart: body.restart ?? { policy: 'always' },
    deploy,
  })
}

/** A Go duration ("1m30s", "500ms", "2h") in milliseconds; null when it is not one. */
function parseDuration(text) {
  if (!/^(\d+(\.\d+)?(ms|s|m|h))+$/.test(text)) return null
  const units = { ms: 1, s: SECOND, m: MINUTE, h: HOUR }
  let total = 0
  for (const [, n, , unit] of text.matchAll(/(\d+(\.\d+)?)(ms|s|m|h)/g)) total += Number(n) * units[unit]
  return total
}

/** Milliseconds as Go prints a duration: 90s → "1m30s", 2m → "2m0s", 500ms → "500ms". */
function formatGoDuration(ms) {
  if (ms < SECOND) return `${ms}ms`
  const h = Math.floor(ms / HOUR)
  const m = Math.floor((ms % HOUR) / MINUTE)
  const s = (ms % MINUTE) / SECOND
  return `${h ? `${h}h` : ''}${h || m ? `${m}m` : ''}${s}s`
}

const hostnamesOf = sp => [...(sp.domain ? [sp.domain] : []), ...(sp.aliases ?? []), ...(sp.redirects ?? [])]

/**
 * What the agent checks before it records anything, for deploy, redeploy and
 * rollback alike: a hostname or a server port another application's active
 * configuration holds, or one Shipwick itself uses. One entry per offending line.
 */
function checkConflicts(app, sp) {
  const fields = []
  const others = [...apps.values()].filter(a => a !== app && a.active_deployment_id).map(a => [a.name, deployments.get(a.active_deployment_id).spec])
  const hostLines = [
    ...(sp.domain ? [['domain', sp.domain]] : []),
    ...(sp.aliases ?? []).map((h, i) => [`aliases[${i}]`, h]),
    ...(sp.redirects ?? []).map((h, i) => [`redirects[${i}]`, h]),
  ]
  // Applications share a hostname when their paths differ (compared without
  // regard to case, as the proxy matches). A redirect hostname, and Shipwick's
  // own, are taken whole.
  const pathKey = other => (other.path ?? '').toLowerCase()
  const mine = pathKey(sp)
  const serves = other => [...(other.domain ? [other.domain] : []), ...(other.aliases ?? [])]
  for (const [field, host] of hostLines) {
    if (OWN_HOSTNAMES.includes(host)) {
      fields.push({ field, message: 'already served by Shipwick itself (the agent or the dashboard)' })
      continue
    }
    const whole = field.startsWith('redirects')
    const owner = others.find(([, other]) => (other.redirects ?? []).includes(host) || (serves(other).includes(host) && (whole || pathKey(other) === mine)))
    if (!owner) continue
    if (whole || mine === '') fields.push({ field, message: `already served by application "${owner[0]}"` })
    else if (field === 'domain') fields.push({ field: 'path', message: `${host}${sp.path} is already served by application "${owner[0]}"; applications share a domain under different paths` })
    else fields.push({ field, message: `${host}${sp.path} is already served by application "${owner[0]}"` })
  }
  // A certificate for a wildcard comes through a DNS record or from the operator; without either there is none to serve it with.
  for (const [field, host] of hostLines) {
    if (!host.startsWith('*.') || DNS_CHALLENGE || [...certificates.values()].some(c => c.subjects.includes(host))) continue
    fields.push({
      field,
      message: 'a certificate for a wildcard is issued only through a DNS record, and the agent is not set up for that',
      expected: `SHIPWICK_CLOUDFLARE_API_TOKEN on the agent, or a certificate of your own: shipwick cert set '${host}' --cert fullchain.pem --key privkey.pem`,
    })
  }
  for (const [i, p] of (sp.publish ?? []).entries()) {
    if (RESERVED_PORTS.includes(p.host)) {
      fields.push({ field: `publish[${i}].host`, message: 'already published by Shipwick itself (the agent or the proxy)' })
      continue
    }
    // "Every address" collides with any specific address, and the other way round.
    const owner = others.find(([, other]) => (other.publish ?? []).some(q => q.host === p.host && q.protocol === p.protocol && (!q.address || !p.address || q.address === p.address)))
    if (owner) fields.push({ field: `publish[${i}].host`, message: `already published by application "${owner[0]}"` })
  }
  if (fields.length > 0) throw configError(fields)
}

// ---------------------------------------------------------------------------
// Traffic: what the proxy's access log would say, generated on the fly and
// deterministic per step, so a refresh redraws the same past.
// ---------------------------------------------------------------------------

const TRAFFIC_WINDOWS = { '1h': [HOUR, 60], '24h': [DAY, 300], '7d': [7 * DAY, 3600] }
/** Requests a minute at the busiest time of day, by application; one that is not listed gets a trickle. */
const TRAFFIC_RATE = { 'my-api': 210, 'web': 90, 'landing': 14, 'shop': 0.6, 'docs': 3 }

function requireTraffic() {
  if (!TRAFFIC_AVAILABLE) {
    throw new HttpError(409, 'TRAFFIC_UNAVAILABLE', 'the proxy\'s access log cannot be read: traffic is recorded when the proxy runs as the caddy service of the agent\'s compose project')
  }
}

/** The requests of one minute: counts by status class, bytes and percentiles. Null for a minute without a request. */
function trafficMinute(app, sp, t) {
  const minute = t / MINUTE
  const rate = TRAFFIC_RATE[app.name] ?? 0.2
  // Busiest in the afternoon (UTC), a third of that at night.
  const hour = (t % DAY) / HOUR
  const daily = 0.65 + 0.35 * Math.sin(((hour - 9) / 24) * 2 * Math.PI)
  const requests = Math.floor(rate * daily * (0.7 + noise(app.name, minute) * 0.6) + noise(app.name, minute + 0.3))
  if (requests <= 0) return null
  // A stopped application is answered by the proxy itself: 503, quickly.
  const stopped = app.desired_state === 'stopped' && t >= Date.parse(app.updated_at)
  // Every so often a few minutes in which a share of the answers are errors.
  const troubled = Math.floor(minute / 7) % 23 === 5
  const status5xx = stopped ? requests : Math.round(requests * (troubled ? 0.04 + noise(app.name, minute + 0.6) * 0.1 : noise(app.name, minute + 0.6) < 0.03 ? 0.01 : 0))
  const status4xx = stopped ? 0 : Math.round((requests - status5xx) * (0.01 + noise(app.name, minute + 0.7) * 0.03))
  const status3xx = stopped ? 0 : Math.round((requests - status5xx - status4xx) * (sp.redirects?.length ? 0.04 : 0.005))
  const p50 = stopped ? 0.4 : isStaticSpec(sp) ? 0.6 + noise(app.name, minute + 0.8) : 9 + noise(app.name, minute + 0.8) * 8 + (troubled ? 30 : 0)
  return {
    requests,
    status_2xx: requests - status5xx - status4xx - status3xx,
    status_3xx: status3xx,
    status_4xx: status4xx,
    status_5xx: status5xx,
    bytes: Math.round(requests * (stopped ? 180 : 2400 + noise(app.name, minute + 0.9) * 9000)),
    p50_ms: round1(p50),
    p95_ms: round1(p50 * (3 + noise(app.name, minute + 0.1) * 2)),
    p99_ms: round1(p50 * (9 + noise(app.name, minute + 0.2) * 14)),
  }
}

/** Adds minutes up into one step. The percentiles of a sum are not the sum of percentiles; a mean weighted by requests is close enough here. */
function addTraffic(into, m) {
  const before = into.requests
  for (const key of ['requests', 'status_2xx', 'status_3xx', 'status_4xx', 'status_5xx', 'bytes']) into[key] += m[key]
  for (const key of ['p50_ms', 'p95_ms', 'p99_ms']) into[key] = round1((into[key] * before + m[key] * m.requests) / into.requests)
}

const noTraffic = () => ({ requests: 0, status_2xx: 0, status_3xx: 0, status_4xx: 0, status_5xx: 0, bytes: 0, p50_ms: 0, p95_ms: 0, p99_ms: 0 })

function traffic(app, since) {
  const [windowMs, stepSeconds] = TRAFFIC_WINDOWS[since]
  const stepMs = stepSeconds * SECOND
  const now = Date.now()
  // The window starts on a step boundary, so it is up to one step longer than asked.
  const start = Math.floor((now - windowMs) / stepMs) * stepMs
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const totals = noTraffic()
  const points = []
  // Without a domain nothing reaches the application through the proxy: zeros, not an error.
  if (active?.spec.domain) {
    const from = Math.max(start, Math.floor(Date.parse(app.created_at) / stepMs) * stepMs)
    for (let t = from; t <= now; t += stepMs) {
      const step = noTraffic()
      // A week of hours is sampled, four minutes to the hour, rather than added up minute by minute.
      const stride = stepMs > 5 * MINUTE ? 15 : 1
      for (let m = t; m < t + stepMs && m <= now; m += stride * MINUTE) {
        const minute = trafficMinute(app, active.spec, m)
        if (!minute) continue
        if (stride > 1) for (const key of ['requests', 'status_2xx', 'status_3xx', 'status_4xx', 'status_5xx', 'bytes']) minute[key] *= stride
        addTraffic(step, minute)
      }
      if (step.requests === 0) continue
      points.push({ t: iso(t), ...step })
      addTraffic(totals, step)
    }
  }
  return { application: app.name, since: iso(start), step_seconds: stepSeconds, totals, points }
}

const REQUEST_SLOT_MS = 1500
const REQUEST_PATHS = {
  'my-api': ['/v1/users', '/v1/users/42', '/v1/orders', '/v1/orders/9913/items', '/v1/session', '/health', '/v1/search', '/admin/reports'],
  'landing': ['/', '/pricing', '/assets/index-4f2a.js', '/assets/index-91bc.css', '/favicon.svg', '/about'],
  'docs': ['/docs/', '/docs/install', '/docs/cli'],
}
const REQUEST_CLIENTS = ['203.0.113.7', '198.51.100.24', '192.0.2.144', '2001:db8::9f', '203.0.113.201', '198.51.100.77']

/**
 * The most recent requests, oldest first: the agent keeps the last 200 per
 * application in memory, so the list holds nothing older than its start. One
 * time slot yields at most one request, the same one whenever it is asked for.
 */
function recentRequests(app, tail) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  if (!active?.spec.domain) return []
  const rate = Math.min(0.9, (TRAFFIC_RATE[app.name] ?? 0.2) / 40)
  const stopped = app.desired_state === 'stopped'
  const paths = REQUEST_PATHS[app.name] ?? ['/', '/login', '/api/items', '/api/items/7', '/static/app.js']
  const first = Math.ceil((startedAt - 10 * MINUTE) / REQUEST_SLOT_MS)
  const out = []
  for (let slot = Math.floor(Date.now() / REQUEST_SLOT_MS); slot >= first && out.length < tail; slot--) {
    if (noise(app.name, slot) >= rate) continue
    const pickFrom = (list, salt) => list[Math.floor(noise(app.name, slot + salt) * list.length)]
    const roll = noise(app.name, slot + 0.5)
    const status = stopped ? 503 : roll < 0.9 ? 200 : roll < 0.93 ? 304 : roll < 0.96 ? 404 : roll < 0.98 ? 401 : roll < 0.99 ? 201 : 502
    const method = status === 201 ? 'POST' : roll > 0.8 && roll < 0.86 ? 'POST' : 'GET'
    out.push({
      time: new Date(slot * REQUEST_SLOT_MS + Math.floor(noise(app.name, slot + 0.2) * REQUEST_SLOT_MS)).toISOString().replace('Z', `${String(Math.floor(noise(app.name, slot + 0.4) * 1000)).padStart(3, '0')}Z`),
      method,
      path: pickFrom(paths, 0.1),
      status,
      duration_ms: Math.round((stopped ? 0.3 : status === 502 ? 3000 : 2) * (1 + noise(app.name, slot + 0.3) * 40) * 1000) / 1000,
      bytes: status === 304 ? 0 : Math.round(120 + noise(app.name, slot + 0.6) * 18000),
      client: pickFrom(REQUEST_CLIENTS, 0.7),
    })
  }
  return out.reverse()
}

// ---------------------------------------------------------------------------
// Alerts and disk
// ---------------------------------------------------------------------------

const DISK_TOTAL = 40 * 1024 ** 3

function diskUsage() {
  // 62% full when all is well; with the critical alerts, past the 95% they are raised at.
  return { total_bytes: DISK_TOTAL, used_bytes: Math.round(DISK_TOTAL * (ALERTS === 'critical' ? 0.97 : 0.62)) }
}

/** The conditions that hold right now, oldest first. They follow the fixtures: an alert about an application that was deleted or stopped is gone. */
function activeAlerts() {
  if (ALERTS === 'none') return []
  const list = []
  const running = name => apps.get(name)?.desired_state === 'running' && apps.get(name)?.active_deployment_id
  if (ALERTS === 'critical') {
    const disk = diskUsage()
    list.push({ kind: 'disk', severity: 'critical', application: '', replica: 0, message: `The server's disk is ${Math.round((disk.used_bytes / disk.total_bytes) * 100)}% full (${formatSize(disk.total_bytes - disk.used_bytes)} of ${formatSize(disk.total_bytes)} free). See what takes the space with: docker system df`, since: ago(3 * HOUR) })
    if (running('worker')) list.push({ kind: 'unhealthy', severity: 'critical', application: 'worker', replica: 0, message: 'worker has had 1 of 2 replicas healthy for 1h. See why with: shipwick status worker', since: ago(72 * MINUTE) })
  }
  if (running('worker')) list.push({ kind: 'restarts', severity: 'warning', application: 'worker', replica: 2, message: 'worker replica 2 was restarted 3 times in 10 minutes. Its last output says why: shipwick logs worker', since: ago(11 * MINUTE) })
  if (running('web')) list.push({ kind: 'memory', severity: 'warning', application: 'web', replica: 3, message: 'web replica 3 is at 93% of its memory limit (476 MB of 512 MB). At the limit it is killed and restarted; raise resources.memory in deploy.yaml, or watch it with: shipwick status web', since: ago(6 * MINUTE) })
  return list.sort((a, b) => a.since.localeCompare(b.since))
}

// ---------------------------------------------------------------------------
// Registries, supplied certificates, key rotation
// ---------------------------------------------------------------------------

const REGISTRY_HOST = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/

/** The name a credential is stored under: a hostname with an optional port, lower-cased; Docker Hub's three names are one. */
function normalizeRegistry(raw) {
  const name = String(raw ?? '').toLowerCase()
  const invalid = new HttpError(400, 'INVALID_REQUEST', `invalid registry ${JSON.stringify(raw)}: use its hostname, with a port if it has one, e.g. ghcr.io or registry.example.com:5000`)
  const [host, port, ...rest] = name.split(':')
  if (name.length > 255 || rest.length > 0 || !REGISTRY_HOST.test(host)) throw invalid
  if (port !== undefined && (!/^[1-9]\d{0,4}$/.test(port) || Number(port) > 65535)) throw invalid
  return name === 'index.docker.io' || name === 'registry-1.docker.io' ? 'docker.io' : name
}

function validateRegistryCredential(username, password) {
  const refuse = message => new HttpError(400, 'INVALID_REQUEST', message)
  if (typeof username !== 'string' || username === '') throw refuse('the username is empty')
  if (username.length > 255) throw refuse('the username is too long (max 255 characters)')
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1F\x7F:]/.test(username)) throw refuse('the username must not contain a colon or control characters')
  if (typeof password !== 'string' || password === '') throw refuse('the password is empty')
  if (Buffer.byteLength(password) > 16 * 1024) throw refuse('the password is too large (max 16 KB)')
  if (password.includes('\0')) throw refuse('the password must not contain NUL bytes')
}

/** The registry's verdict on a credential, before anything is stored. Two names make it say no, for demonstration. */
function registryLogin(registry) {
  const host = registry.split(':')[0]
  if (host.startsWith('refused.')) {
    throw new HttpError(400, 'REGISTRY_LOGIN_FAILED', `${registry} refused the login: unauthorized: incorrect username or password`, { registry, refused: true })
  }
  if (host.endsWith('.invalid')) {
    throw new HttpError(400, 'REGISTRY_LOGIN_FAILED', `could not log in to ${registry}: dial tcp: lookup ${host}: no such host`, { registry, refused: false })
  }
}

const invalidCertificate = message => new HttpError(400, 'INVALID_CERTIFICATE', message)

/** A hostname as in deploy.yaml, lower-cased; one leading `*.` makes it a wildcard. */
function certificateHostname(raw) {
  const hostname = String(raw ?? '').toLowerCase()
  const name = hostname.startsWith('*.') ? hostname.slice(2) : hostname
  if (!HOSTNAME.test(name)) throw new HttpError(400, 'INVALID_REQUEST', `hostname: invalid value ${JSON.stringify(hostname)}: not a valid hostname`)
  return hostname
}

/**
 * Looks at the PEM the way the agent's first checks do. X.509 is not parsed
 * here: what the certificate says about itself is made up from the hostname.
 */
function checkCertificate(hostname, body) {
  for (const field of ['certificate', 'key']) {
    if (typeof body[field] !== 'string' || body[field].trim() === '') throw new HttpError(400, 'INVALID_REQUEST', `${field} is required: PEM text`)
    if (Buffer.byteLength(body[field]) > 64 * 1024) throw new HttpError(400, 'INVALID_REQUEST', `${field} is larger than 64 KB`)
  }
  if (/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(body.certificate)) throw invalidCertificate('the certificate file contains a private key: give the key separately, and only certificates here')
  if (!body.certificate.includes('-----BEGIN CERTIFICATE-----')) throw invalidCertificate('the certificate is not PEM: expected one or more -----BEGIN CERTIFICATE----- blocks, the server\'s own first')
  if (body.key.includes('-----BEGIN ENCRYPTED PRIVATE KEY-----') || body.key.includes('Proc-Type: 4,ENCRYPTED')) throw invalidCertificate('the key is protected by a passphrase, which the proxy cannot enter: remove it with openssl pkey -in <key> -out privkey.pem')
  if (!/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(body.key)) throw invalidCertificate('the key is not PEM: expected a -----BEGIN PRIVATE KEY----- block')
  const now = Date.now()
  return { subjects: [hostname], issuer: 'Mock Issuing CA', not_before: iso(now - DAY), not_after: iso(now + 365 * DAY) }
}

const certificateView = ([hostname, c]) => ({ hostname, ...c })

// ---------------------------------------------------------------------------
// Backups: of an application's volumes, and of the agent's own state
// ---------------------------------------------------------------------------

/** What the agent's own state is recorded under; no application can be named so. */
const STATE = '_agent'
const STATE_KEEP = 7
const VERIFY_OUTPUT = 'PostgreSQL Database directory appears to contain a database; Skipping initialization\n\nLOG:  starting PostgreSQL 17.2 on x86_64-pc-linux-musl\nLOG:  listening on IPv4 address "0.0.0.0", port 5432\nLOG:  database system was shut down at 2026-10-03 03:00:39 UTC\nLOG:  database system is ready to accept connections'
const STATE_NOT_ENCRYPTED = 'the agent\'s state is not backed up: SHIPWICK_BACKUP_PASSPHRASE is not set, and the encryption key is never written anywhere unencrypted'

const backupDestinations = () => (BUCKET ? ['local', 's3'] : ['local'])

function addBackup(application, { trigger, startedAtMs, durationMs = null, status, volumes = [], error = '' }) {
  const kept = status === 'succeeded'
  const run = {
    id: nextBackupId++,
    application,
    trigger,
    status,
    started_at: iso(startedAtMs),
    completed_at: durationMs === null ? null : iso(startedAtMs + durationMs),
    volumes: kept ? volumes : [],
    destinations: kept ? backupDestinations() : [],
    encrypted: PASSPHRASE,
    error,
    activity: '',
    verified_at: null,
    verify_error: '',
    restored_at: null,
    restore_error: '',
    verify_output: '',
  }
  backups.set(run.id, run)
  return run
}

/** A backup as lists and 202 answers carry it: without its application and the verification's output. */
function backupView(run) {
  const { application: _application, verify_output: _output, ...rest } = run
  return rest
}

function backupDetail(run) {
  const { application: _application, ...rest } = run
  return rest
}

const backupsOf = application => [...backups.values()].filter(r => r.application === application).sort((a, b) => b.id - a.id)

function backupId(raw) {
  const id = Number(raw)
  if (!/^\d+$/.test(raw ?? '') || id < 1) throw new HttpError(400, 'INVALID_REQUEST', 'backup id must be a positive number')
  return id
}

/** The backup `raw` names for one application; `latest` is the newest that succeeded, where that is allowed. */
function requireBackup(application, raw, allowLatest = false) {
  if (allowLatest && raw === 'latest') {
    const newest = backupsOf(application).find(r => r.status === 'succeeded')
    if (!newest) throw new HttpError(404, 'NOT_FOUND', 'there is no successful backup yet; take one with: shipwick backups run')
    return newest
  }
  const run = backups.get(backupId(raw))
  if (!run || run.application !== application) throw new HttpError(404, 'NOT_FOUND', 'not found')
  return run
}

const backupBusy = run => run.status === 'running' || run.activity !== ''

function requireUsable(run) {
  if (backupBusy(run)) throw new HttpError(409, 'BACKUP_BUSY', 'the backup is in use: it is still being taken, verified or restored')
  if (run.status !== 'succeeded') throw new HttpError(409, 'BACKUP_NOT_USABLE', 'that backup did not succeed; nothing was kept of it')
}

/** Keeps the newest `keep` successes; failures never count and are never what is kept. */
function pruneBackups(application, keep) {
  const successes = backupsOf(application).filter(r => r.status === 'succeeded' && !backupBusy(r))
  for (const old of successes.slice(keep)) backups.delete(old.id)
}

/** Starts a backup that finishes by itself after a few seconds: `running` on the first poll, done on a later one. */
function startBackup(application, volumes, keep, wait) {
  const run = addBackup(application, { trigger: 'manual', startedAtMs: Date.now(), status: 'running' })
  setTimeout(() => {
    if (!backups.has(run.id)) return
    Object.assign(run, { status: 'succeeded', completed_at: iso(Date.now()), volumes, destinations: backupDestinations() })
    if (keep) pruneBackups(application, keep)
  }, wait).unref()
  return run
}

/** The `backups` object of GET /server: where backups go, and how the agent's own state is doing. */
function backupStatus() {
  const last = backupsOf(STATE).find(r => r.status === 'succeeded')
  const latest = backupsOf(STATE).find(r => r.status !== 'running')
  return {
    destination: BUCKET ? 's3' : 'local',
    encrypted: PASSPHRASE,
    state_last_at: last ? last.completed_at : null,
    state_error: !PASSPHRASE ? STATE_NOT_ENCRYPTED : latest?.status === 'failed' ? latest.error : '',
  }
}

// ---------------------------------------------------------------------------
// Export, import and the standby server
// ---------------------------------------------------------------------------

/** What scheduled and requested exports are recorded under, next to the agent's own state. */
const EXPORTS = '_export'
const EXPORT_KEEP = 3
const STANDBY_APPLICATIONS = ['postgres', 'shop', 'docs']
/** How the scheduled fetch from the bucket is doing; only a standby has one. */
const standbyPull = { schedule: '15 * * * *', last_at: null, last_export: 0, last_error: '' }
/** The import that is running or ran last; kept in memory, so null until one has run. */
let lastImport = null

/** Deployed stopped by an import and not started since: what a promotion starts. */
function standbyApplications() {
  return [...apps.values()]
    .map(app => [app, app.active_deployment_id ? deployments.get(app.active_deployment_id) : null])
    .filter(([app, active]) => active?.kind === 'standby' && app.desired_state === 'stopped')
    .sort(([, a], [, b]) => a.started_at.localeCompare(b.started_at))
}

/** The records that send an application's hostnames to this server. */
const recordsFor = list => list.flatMap(([, active]) => hostnamesOf(active.spec).filter(h => !h.startsWith('*.')).map(hostname => ({ hostname, type: 'A', value: SERVER_ADDRESS })))

function standbyView() {
  const waiting = standbyApplications()
  return {
    applications: waiting.map(([app, active]) => ({ name: app.name, version: active.version, hostnames: hostnamesOf(active.spec), imported_at: active.completed_at ?? active.started_at })),
    records: recordsFor(waiting),
    pull: IS_STANDBY ? { ...standbyPull } : null,
    ...(BEFORE_06 ? {} : { promotion }),
  }
}

function requireNoImport() {
  if (lastImport?.status === 'running') throw new HttpError(409, 'IMPORT_IN_PROGRESS', 'an import is running on this server; follow it with: shipwick import --status')
}

/** The promotion that runs or ran last; null on a server that was never promoted. */
let promotion = null
let promotionCount = 0

function requireNoPromotion() {
  if (promotion && promotion.completed_at === null) throw new HttpError(409, 'PROMOTION_IN_PROGRESS', 'a promotion is running on this server; follow it with: shipwick standby promote')
}

/**
 * Starts what waits stopped, one application after the other, `pace` each:
 * pending → starting → running. The record is what GET /standby/promotion
 * answers while it runs and afterwards. An application named `shop` is
 * started and not ready within its budget, so that outcome can be seen too.
 */
function startPromotion(pace = 3 * SECOND, byWho = '') {
  const waiting = standbyApplications()
  const record = {
    applications: waiting.map(([app]) => ({ name: app.name, status: 'pending', message: '' })),
    records: recordsFor(waiting),
    id: ++promotionCount,
    status: 'running',
    started_at: iso(Date.now()),
    completed_at: null,
  }
  promotion = record
  waiting.forEach(([app, active], i) => {
    setTimeout(() => {
      record.applications[i].status = 'starting'
    }, 400 + i * pace).unref()
    setTimeout(() => {
      const entry = record.applications[i]
      if (apps.get(app.name) !== app) return Object.assign(entry, { status: 'failed', message: 'the application was deleted while the promotion ran' })
      app.desired_state = 'running'
      app.containers = makeContainers(app, { ...active, completed_at: iso(Date.now()) })
      app.updated_at = iso(Date.now())
      addAppEvent(app.name, 'info', 'app', `Application started${byWho}`)
      if (app.name === 'shop') Object.assign(entry, { status: 'started', message: 'its replica did not pass the health check within 2m; it keeps being checked: shipwick status shop' })
      else entry.status = 'running'
    }, 400 + (i + 1) * pace).unref()
  })
  setTimeout(() => {
    Object.assign(record, { status: record.applications.some(a => a.status === 'failed') ? 'failed' : 'succeeded', completed_at: iso(Date.now()) })
  }, 600 + waiting.length * pace).unref()
  return record
}

/** What a promotion with nothing to start is answered: complete, and not recorded. */
const emptyPromotion = () => ({ applications: [], records: [], id: 0, status: 'succeeded', started_at: iso(Date.now()), completed_at: iso(Date.now()) })

// What the backup destination "holds" beyond the agent's lists, until it has been adopted.
let adoptable = true

/**
 * An import that works through the applications one by one, a second and a
 * half each. Stopped, it replaces what waits stopped and leaves what runs;
 * otherwise everything that exists is left as it is, which is all the mock
 * can know of an export it does not read.
 */
function startImport({ source, stopped, overwrite }) {
  const names = [...apps.values()].filter(a => a.active_deployment_id).sort((a, b) => a.created_at.localeCompare(b.created_at)).map(a => a.name)
  const run = {
    status: 'running',
    source,
    stopped,
    overwrite,
    started_at: iso(Date.now()),
    completed_at: null,
    exported_at: null,
    secrets: 0,
    registries: 0,
    certificates: 0,
    applications: [],
    warnings: [],
    error: '',
  }
  lastImport = run
  // An import that has ended, because its upload broke off, changes nothing more.
  const step = (delay, fn) => setTimeout(() => {
    if (lastImport === run && run.status === 'running') fn()
  }, delay).unref()
  step(1200, () => {
    run.exported_at = iso(Date.now() - 12 * MINUTE)
    run.applications = names.map(name => ({ name, status: 'pending', version: deployments.get(apps.get(name).active_deployment_id).version, deployment_id: null, volumes: [], message: '' }))
    if (overwrite) Object.assign(run, { secrets: secrets.size, registries: registries.size, certificates: certificates.size })
    else run.warnings = [...secrets.keys()].sort().map(name => `${name}: a secret by that name exists on this server and was kept; --overwrite replaces it`)
  })
  names.forEach((name, i) => {
    step(1200 + i * IMPORT_MS + 300, () => {
      run.applications[i].status = 'importing'
    })
    step(1200 + (i + 1) * IMPORT_MS, () => {
      const app = apps.get(name)
      const entry = run.applications[i]
      const active = app && deployments.get(app.active_deployment_id)
      if (stopped && active?.kind === 'standby' && app.desired_state === 'stopped') {
        active.completed_at = iso(Date.now())
        Object.assign(entry, { status: 'imported', deployment_id: active.id, volumes: (active.spec.volumes ?? []).map(v => v.name) })
      }
      else if (stopped) Object.assign(entry, { status: 'skipped', message: 'it runs on this server, which was promoted or deployed to since; a stopped import replaces only what is stopped' })
      // Replacing what exists: here the application is the one the export was written from, so it stays as it is and is reported as taken over.
      else if (overwrite && active && !inFlight(name)) Object.assign(entry, { status: 'imported', deployment_id: active.id, volumes: (active.spec.volumes ?? []).map(v => v.name) })
      else if (overwrite) Object.assign(entry, { status: 'failed', message: `another operation is in progress for ${name}` })
      else Object.assign(entry, { status: 'skipped', message: 'it exists on this server and was left as it is; import with --overwrite to replace it and its volumes' })
    })
  })
  step(1200 + names.length * IMPORT_MS + 400, () => {
    Object.assign(run, { status: run.applications.some(a => a.status === 'failed') ? 'failed' : 'succeeded', completed_at: iso(Date.now()) })
  })
  return run
}

// ---------------------------------------------------------------------------
// Background activity
// ---------------------------------------------------------------------------

function startBackground() {
  // A log line per running application roughly every 700ms.
  setInterval(() => {
    for (const app of apps.values()) generateLogLine(app)
  }, 700).unref()

  // The scheduler looks at every job's schedule a few times a minute.
  runSchedules()
  setInterval(runSchedules, 20 * SECOND).unref()

  // The crash-looping replica gets its slow retry; here every 45s instead of every 5m.
  setInterval(() => {
    const app = apps.get('worker')
    const c = app?.containers.find(x => x.crash_loop)
    if (!app || !c || app.desired_state !== 'running') return
    archiveContainer(app, c, 'unhealthy')
    c.restarts++
    c.started_at = iso(Date.now())
    addAppEvent('worker', 'warn', 'supervisor', `Replica ${c.replica} did not become healthy within 30s of starting: HTTP 503`)
  }, 45 * SECOND).unref()

  // The degraded app's third replica flaps: up for a while, then out of memory again.
  setInterval(() => {
    const app = apps.get('web')
    const c = app?.containers.find(x => x.replica === 3)
    if (!app || !c || app.desired_state !== 'running') return
    if (c.state === 'running') {
      archiveContainer(app, c, 'oom_killed', { exitCode: 137, oomKilled: true })
      Object.assign(c, { state: 'exited', exit_code: 137, oom_killed: true, health: 'unhealthy' })
      addAppEvent('web', 'warn', 'supervisor', 'Replica 3 exited with code 137 (out of memory); restarting in 5s')
    }
    else {
      Object.assign(c, { state: 'running', exit_code: 0, oom_killed: false, health: 'starting', restarts: c.restarts + 1, started_at: iso(Date.now()) })
      addAppEvent('web', 'info', 'supervisor', 'Replica 3 restarted')
    }
  }, 40 * SECOND).unref()
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

function sendJSON(res, status, data, headers = {}) {
  const body = JSON.stringify({ data })
  // Set, not only written: the audit trail reads Location back to name what a request made.
  for (const [name, value] of Object.entries(headers)) res.setHeader(name, value)
  res.writeHead(status, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body) })
  res.end(body)
}

function sendError(res, status, code, message, details = {}) {
  // The audit trail's entry for the request names the code it was refused or failed with.
  res.auditCode = code
  const body = JSON.stringify({ error: { code, message, details } })
  res.writeHead(status, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body) })
  res.end(body)
}

class HttpError extends Error {
  constructor(status, code, message, details = {}) {
    super(message)
    this.status = status
    this.code = code
    this.details = details
  }
}

function intParam(url, name, fallback, min, max) {
  const raw = url.searchParams.get(name)
  if (raw === null || raw === '') return fallback
  const n = Number(raw)
  if (!Number.isInteger(n) || n < min || n > max) {
    throw new HttpError(400, 'INVALID_REQUEST', `${name} must be a number between ${min} and ${max}`)
  }
  return n
}

function boolParam(url, name) {
  const raw = url.searchParams.get(name)
  if (raw === null || raw === '') return false
  if (['1', 't', 'true', 'TRUE', 'True'].includes(raw)) return true
  if (['0', 'f', 'false', 'FALSE', 'False'].includes(raw)) return false
  throw new HttpError(400, 'INVALID_REQUEST', `${name} must be true or false`)
}

async function readJSON(req, allowed, limit = 64 * 1024, expected = '') {
  const chunks = []
  let size = 0
  for await (const chunk of req) {
    size += chunk.length
    if (size > limit) throw new HttpError(413, 'INVALID_REQUEST', `request body larger than ${Math.round(limit / 1024)} KB`)
    chunks.push(chunk)
  }
  const text = Buffer.concat(chunks).toString('utf8').trim()
  if (text === '') return {}
  let value
  try {
    value = JSON.parse(text)
    if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new Error('not an object')
  }
  catch {
    throw new HttpError(400, 'INVALID_REQUEST', expected ? `invalid JSON body: expected ${expected}` : allowed ? 'request body must be a JSON object' : 'the mock agent reads deploy.yaml as JSON only; send the document as a JSON object')
  }
  // Like the agent: a typo such as "imgae" must not quietly redeploy the old image.
  for (const key of Object.keys(value)) {
    if (allowed && !allowed.includes(key)) throw new HttpError(400, 'INVALID_REQUEST', expected ? `invalid JSON body: expected ${expected}` : `json: unknown field ${JSON.stringify(key)}`)
  }
  return value
}

/**
 * Reads an archive upload: the content type must say tar, the first block
 * must be a tar header, and the rest is counted, not kept. Answers the size.
 * `what` names the thing for the size limit's message; the digest and the
 * file count are what a static upload is answered with.
 */
async function readArchive(req, { limit = 10 * 1024 ** 3, what = 'the archive', wrongType = 'the body must be a tar archive sent as Content-Type: application/x-tar' } = {}) {
  const [type] = String(req.headers['content-type'] ?? '').split(';')
  if (type.trim() !== 'application/x-tar') throw new HttpError(400, 'INVALID_REQUEST', wrongType)
  const tooLarge = () => new HttpError(413, 'INVALID_REQUEST', `${what} exceeds ${formatSize(limit).replace('.0', '')}`)
  if (Number(req.headers['content-length'] ?? 0) > limit) throw tooLarge()
  const hash = createHash('sha256')
  let size = 0
  let files = 0
  let fileBytes = 0
  // Walk the headers: a file entry's data follows it, padded to 512 bytes.
  let offset = 0
  let skip = 0
  let pending = Buffer.alloc(0)
  for await (const chunk of req) {
    size += chunk.length
    if (size > limit) throw tooLarge()
    hash.update(chunk)
    pending = Buffer.concat([pending, chunk])
    while (pending.length >= 512) {
      if (skip > 0) {
        const take = Math.min(skip, pending.length - (pending.length % 512))
        if (take === 0) break
        pending = pending.subarray(take)
        skip -= take
        offset += take
        continue
      }
      const header = pending.subarray(0, 512)
      pending = pending.subarray(512)
      offset += 512
      if (header.every(b => b === 0)) continue
      if (offset === 512 && header.toString('ascii', 257, 262) !== 'ustar') throw new HttpError(400, 'INVALID_REQUEST', `${what} is not a tar file`)
      const entrySize = parseInt(header.toString('ascii', 124, 136).replace(/\0.*$/, '').trim() || '0', 8)
      const kind = header.toString('ascii', 156, 157)
      if (kind === '0' || kind === '\0' || kind === '') {
        files++
        fileBytes += entrySize
      }
      skip = Math.ceil(entrySize / 512) * 512
    }
  }
  if (offset < 512) throw new HttpError(400, 'INVALID_REQUEST', `${what} is not a tar file`)
  return { size, files, fileBytes, digest: `sha256:${hash.digest('hex')}` }
}

/** A volume's Docker name, shipwick_<application>_<volume>, taken apart; null when it is not one. */
function parseVolumeName(name) {
  const m = /^shipwick_([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)_([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)$/.exec(name ?? '')
  return m ? { application: m[1], volume: m[3] } : null
}

/** Every volume on the server: those of existing applications' active deployments, and the kept ones of deleted applications. */
function managedVolumes() {
  const list = [...orphanVolumes.map(v => ({ ...v, orphan: true }))]
  for (const app of apps.values()) {
    const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
    for (const v of active?.spec.volumes ?? []) {
      // The database's volume holds data; anything else has been created but not written to, which the daemon reports as no size.
      const size = app.name === 'postgres' ? 2684354560 : -1
      list.push({ name: `shipwick_${app.name}_${v.name}`, application: app.name, volume: v.name, size_bytes: size, orphan: false })
    }
  }
  return list.sort((a, b) => a.name.localeCompare(b.name))
}

const SECRET_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/

function requireSecretName(name) {
  if (!SECRET_NAME.test(name ?? '')) {
    throw new HttpError(400, 'INVALID_REQUEST', `invalid secret name ${JSON.stringify(name)}: letters, digits and underscores, not starting with a digit, at most 64 characters`)
  }
}

/** Failed authentications per address: the 21st within a minute is refused for a minute without looking at the token. */
const AUTH_FAILURE_LIMIT = 20

function rateLimited(address) {
  const now = Date.now()
  const recent = (authFailures.get(address) ?? []).filter(t => now - t < MINUTE)
  authFailures.set(address, recent)
  if (recent.length < AUTH_FAILURE_LIMIT) return 0
  return Math.max(1, Math.ceil((recent[recent.length - 1] + MINUTE - now) / SECOND))
}

function recordAuthFailure(address) {
  const list = authFailures.get(address) ?? []
  list.push(Date.now())
  authFailures.set(address, list)
}

function requireApp(name) {
  const app = apps.get(name)
  if (!app) throw new HttpError(404, 'NOT_FOUND', 'not found')
  return app
}

function requireIdle(app) {
  // A backup being taken, or restored, holds the application like a deployment does; a verification runs beside it.
  if (inFlight(app.name) || backupsOf(app.name).some(r => r.status === 'running' || r.activity === 'restore')) {
    throw new HttpError(409, 'DEPLOYMENT_IN_PROGRESS', `another operation is in progress for ${app.name}`)
  }
}

function requireActive(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  if (!active) throw new HttpError(409, 'NOT_DEPLOYED', `${app.name} has no active deployment`)
  return active
}

const IMAGE_PATTERN = /^[a-z0-9]+([._\-/:][a-z0-9]+)*(:[\w][\w.-]{0,127})?(@sha256:[a-f0-9]{64})?$/i

/** `update` on GET /server: what the agent heard from GitHub when it last asked, once a day. */
function updateStatus() {
  const checkedAt = iso(startedAt - 5 * HOUR).replace(/\.\d+Z$/, 'Z')
  if (UPDATE === 'off') return { enabled: false, latest_version: '', checked_at: null, available: false }
  if (UPDATE === 'unknown') return { enabled: true, latest_version: '', checked_at: null, available: false }
  if (UPDATE === 'current') return { enabled: true, latest_version: VERSION, checked_at: checkedAt, available: false }
  return { enabled: true, latest_version: 'v0.7.1', checked_at: checkedAt, available: true }
}

/** `?static=` of a deployment: the digest PUT …/static answered, or null. */
function staticDigest(url) {
  const digest = url.searchParams.get('static')
  if (digest !== null && !/^sha256:[a-f0-9]{64}$/.test(digest)) throw new HttpError(400, 'INVALID_REQUEST', 'static: must be the digest PUT …/static answered, sha256:<64 hex characters>')
  return digest
}

/**
 * The application a document sent without a name in the address is about: its
 * own `name`. A limited token is held to it, and to a document that names
 * none; for everybody else a missing name is what validation reports.
 */
function documentName(who, body) {
  const name = typeof body.name === 'string' && APPLICATION_NAME.test(body.name) ? body.name : ''
  const limits = who.role === 'deploy' ? who.applications ?? [] : []
  if (limits.length > 0 && name === '') {
    throw new HttpError(403, 'TOKEN_LIMITED', `this ${credential(who)} is limited to ${nameList(limits)}, and the document does not name an application`, { applications: limits })
  }
  if (limits.length > 0 && !limits.includes(name)) {
    throw new HttpError(403, 'TOKEN_LIMITED', `this ${credential(who)} is limited to ${nameList(limits)}; it can read ${name} and not change it`, { applications: limits, application: name })
  }
  return name
}

/** Deploys a document under `name`: what POST /applications/:name/deploy and POST /applications both do. */
function deployDocument(name, body, digest, who) {
  const sp = parseSpec(name, body)
  let files = null
  if (isStaticSpec(sp)) {
    if (digest === null) throw new HttpError(400, 'INVALID_REQUEST', `static: the folder ${sp.static.dir}/ has not been uploaded for this deployment; upload it with PUT /applications/${name}/static first and pass its digest as ?static=`)
    files = uploads.get(name)?.digest === digest ? uploads.get(name) : null
    if (!files) throw new HttpError(404, 'NOT_FOUND', 'no files were uploaded for this deployment: run shipwick deploy from the project')
  }
  else if (digest !== null) {
    throw new HttpError(400, 'INVALID_REQUEST', 'static: this deploy.yaml describes a container; ?static= is for a static application')
  }
  const app = apps.get(name) ?? addApp(name, iso(Date.now()))
  requireIdle(app)
  checkConflicts(app, sp)
  return startDeployment(app, sp, { kind: 'deploy', sourceId: null, by: who.name, files, refs: referencesOf(body) })
}

/** The filters GET /audit and its export share, with the agent's refusals; `described` is how an export's entry in the trail names them. */
function auditFilter(url) {
  const q = url.searchParams
  const several = name => [...new Set(q.getAll(name).flatMap(v => v.split(',')).map(v => v.trim()).filter(v => v !== ''))]
  const application = q.get('application') ?? ''
  if (application !== '' && !APPLICATION_NAME.test(application)) throw new HttpError(400, 'INVALID_REQUEST', 'application: lowercase letters, digits and dashes only')
  const actor = q.get('actor') ?? ''
  if (actor.length > 320) throw new HttpError(400, 'INVALID_REQUEST', 'actor is the name of a token or the address of a person, e.g. actor=ci')
  // An agent before 0.7 does not know the next three and ignores them.
  const actorKind = BEFORE_07 ? '' : q.get('actor_kind') ?? ''
  if (actorKind !== '' && actorKind !== 'token' && actorKind !== 'user') throw new HttpError(400, 'INVALID_REQUEST', `actor_kind: invalid kind of actor ${JSON.stringify(actorKind)}: use token or user`)
  const actions = BEFORE_07 ? [] : several('action')
  if (actions.length > 20) throw new HttpError(400, 'INVALID_REQUEST', 'action: at most 20 at once; the start of a family covers all of it, e.g. action=backup.')
  for (const action of actions) {
    if (!/^[a-z]{1,20}(\.[a-z]{1,20})?$|^[a-z]{1,20}\.$/.test(action)) throw new HttpError(400, 'INVALID_REQUEST', `invalid action ${JSON.stringify(action)}: use one as the trail shows it, or the start of a family with its dot, e.g. deploy, token.create or backup.`)
  }
  const outcomes = BEFORE_07 ? [] : several('outcome')
  for (const outcome of outcomes) {
    if (!['ok', 'refused', 'failed'].includes(outcome)) throw new HttpError(400, 'INVALID_REQUEST', `invalid outcome ${JSON.stringify(outcome)}: use ok, refused or failed`)
  }
  const since = q.get('since') ? parseSince(q.get('since')) : null
  const before = q.get('before') ?? ''
  if (before !== '' && (!/^\d+$/.test(before) || Number(before) < 1)) throw new HttpError(400, 'INVALID_REQUEST', 'before must be the id of an audit entry')
  const described = [
    application && `application ${application}`,
    actor && `actor ${actor}`,
    actorKind && `actor_kind ${actorKind}`,
    actions.length > 0 && `action ${actions.join(' ')}`,
    outcomes.length > 0 && `outcome ${outcomes.join(' ')}`,
    since !== null && `since ${iso(since).replace(/\.\d+Z$/, 'Z')}`,
  ].filter(Boolean).join(', ')
  const matches = e => (application === '' || e.application === application)
    && (actor === '' || e.actor.name === actor)
    && (actorKind === '' || e.actor.kind === actorKind)
    && (actions.length === 0 || actions.some(a => (a.endsWith('.') ? e.action.startsWith(a) : e.action === a)))
    && (outcomes.length === 0 || outcomes.includes(e.outcome))
    && (since === null || Date.parse(e.at) >= since)
    && (before === '' || e.id < Number(before))
  return { matches, described }
}

const COLLECTIONS = ['applications', 'deployments', 'tokens', 'secrets', 'volumes', 'registries', 'certificates', 'exports']

// What 0.7 added.
const ROUTES_07 = {
  'GET /applications/:p/logs/archive': 'read',
  'GET /applications/:p/logs/archive/:x': 'read',
  'GET /applications/:p/logs/search': 'read',
  'GET /applications/:p/config': 'deploy',
  'POST /applications': 'deploy',
  'POST /validate': 'deploy',
  'PUT /tokens/:p': 'admin',
  'GET /audit/export': 'admin',
}

// What 0.6 added. `none`: answered without a token, to whoever asks.
const ROUTES_06 = {
  'GET /audit': 'admin',
  'POST /server/backups/adopt': 'admin',
  'GET /standby/promotion': 'read',
  'GET /auth': 'none',
  'POST /auth/exchange': 'none',
  'DELETE /auth/session': 'read',
  'GET /access/rules': 'admin',
  'POST /access/rules': 'admin',
  'DELETE /access/rules/:p': 'admin',
  'GET /access/sessions': 'admin',
  'DELETE /access/sessions/:p': 'admin',
}

// What 0.5 added. An agent before it answers every one of these with ENDPOINT_NOT_FOUND.
const ROUTES_05 = {
  'POST /applications/:p/validate': 'deploy',
  'POST /applications/:p/images/missing': 'deploy',
  'GET /applications/:p/traffic': 'read',
  'GET /applications/:p/requests': 'read',
  'GET /applications/:p/backups': 'read',
  'GET /applications/:p/backups/:x': 'read',
  'POST /applications/:p/backups': 'deploy',
  'POST /applications/:p/backups/:x/verify': 'deploy',
  'POST /applications/:p/backups/:x/restore': 'admin',
  'GET /applications/:p/backups/:x/volumes/:y/archive': 'admin',
  'DELETE /applications/:p/backups/:x': 'admin',
  'GET /server/backups': 'admin',
  'GET /server/backups/:x': 'admin',
  'POST /server/backups': 'admin',
  'POST /server/rotate-key': 'admin',
  'GET /registries': 'read',
  'PUT /registries/:p': 'admin',
  'DELETE /registries/:p': 'admin',
  'GET /certificates': 'read',
  'PUT /certificates/:p': 'admin',
  'DELETE /certificates/:p': 'admin',
  'POST /export': 'admin',
  'GET /exports': 'admin',
  'GET /exports/:p': 'admin',
  'POST /exports': 'admin',
  'POST /import': 'admin',
  'GET /import': 'admin',
  'GET /standby': 'read',
  'POST /standby/pull': 'admin',
  'POST /standby/promote': 'admin',
}

// Every endpoint and the role it needs, the agent's own table (docs/api.md).
// A route that is not here is ENDPOINT_NOT_FOUND, answered before the token is looked at.
const ROUTES = {
  'GET /server': 'read',
  'GET /applications': 'read',
  'GET /applications/:p': 'read',
  'DELETE /applications/:p': 'admin',
  'POST /applications/:p/deploy': 'deploy',
  'POST /applications/:p/redeploy': 'deploy',
  'POST /applications/:p/rollback': 'deploy',
  'POST /applications/:p/stop': 'deploy',
  'POST /applications/:p/start': 'deploy',
  'GET /applications/:p/logs': 'read',
  'GET /applications/:p/events': 'read',
  'GET /applications/:p/metrics': 'read',
  'GET /applications/:p/metrics/history': 'read',
  'GET /applications/:p/volumes': 'read',
  'GET /applications/:p/volumes/:x/archive': 'admin',
  'PUT /applications/:p/volumes/:x/archive': 'admin',
  'PUT /applications/:p/static': 'deploy',
  'POST /applications/:p/images': 'deploy',
  'GET /applications/:p/jobs': 'read',
  'GET /applications/:p/runs': 'read',
  'GET /applications/:p/runs/:x': 'read',
  // Starting a container from the application's image is deploying, in effect.
  'POST /applications/:p/jobs/:x/run': 'deploy',
  'POST /applications/:p/run': 'deploy',
  'GET /deployments': 'read',
  'GET /deployments/:p': 'read',
  'GET /tokens': 'admin',
  'POST /tokens': 'admin',
  'DELETE /tokens/:p': 'admin',
  'GET /secrets': 'read',
  'PUT /secrets/:p': 'admin',
  'DELETE /secrets/:p': 'admin',
  'GET /volumes': 'read',
  'DELETE /volumes/:p': 'admin',
  ...(OLD_AGENT ? {} : ROUTES_05),
  ...(BEFORE_06 ? {} : ROUTES_06),
  ...(BEFORE_07 ? {} : ROUTES_07),
}

/** The route a request is looked up under: names and ids replaced by :p, :x and :y. */
function routeOf(method, segments) {
  const s = [...segments]
  if (COLLECTIONS.includes(s[0]) && s.length > 1) s[1] = ':p'
  if (s[0] === 'applications') {
    if (s.length > 3 && ['volumes', 'runs', 'jobs', 'backups'].includes(s[2])) s[3] = ':x'
    if (s.length > 5 && s[2] === 'backups' && s[4] === 'volumes') s[5] = ':y'
    if (s.length > 4 && s[2] === 'logs' && s[3] === 'archive') s[4] = ':x'
  }
  if (s[0] === 'server' && s[1] === 'backups' && s.length > 2 && s[2] !== 'adopt') s[2] = ':x'
  if (s[0] === 'access' && s.length > 2) s[2] = ':p'
  return `${method} /${s.join('/')}`
}

async function handle(req, res) {
  // An agent that is restarting answers nothing: the connection just ends.
  if (Date.now() < awayUntil) return req.socket.destroy()

  const url = new URL(req.url, `http://${req.headers.host ?? 'localhost'}`)
  const method = req.method ?? 'GET'
  const path = url.pathname

  if (method === 'GET' && path === '/api/v1/health') {
    return sendJSON(res, 200, { status: 'ok', version: VERSION })
  }
  // Not the agent: the page a sign-in provider would show.
  if (method === 'GET' && path === '/mock-idp/authorize' && !BEFORE_06) return mockProvider(url, res)

  const segments = path.startsWith('/api/v1/') ? path.split('/').filter(Boolean).slice(2).map(decodeURIComponent) : []
  const route = routeOf(method, segments)
  const param = segments[1]
  const required = ROUTES[route]

  // An agent before 0.7 knows the path of a token for DELETE only; another method is an operation it lacks.
  if (BEFORE_07 && method === 'PUT' && segments[0] === 'tokens' && segments.length === 2) {
    return sendError(res, 404, 'ENDPOINT_NOT_FOUND', `no such endpoint: ${method} ${path}`)
  }
  if (!required) {
    // The agent has no such operation, as opposed to NOT_FOUND: unknown application or deployment.
    return sendError(res, 404, 'ENDPOINT_NOT_FOUND', `no such endpoint: ${method} ${path}`)
  }
  // Too many failed authentications from this address: refused for a minute, the token not even looked at.
  const address = req.socket.remoteAddress ?? ''
  const retryAfter = rateLimited(address)
  if (retryAfter > 0) {
    res.setHeader('retry-after', String(retryAfter))
    return sendError(res, 429, 'RATE_LIMITED', 'too many failed authentications from this address; try again in a minute')
  }
  // The two endpoints a sign-in needs before anybody is signed in.
  if (required === 'none') {
    if (route === 'GET /auth') return sendJSON(res, 200, signInConfig())
    try {
      return exchange(req, res, await readJSON(req, ['code', 'code_verifier', 'nonce', 'redirect_uri'], 16 * 1024, '{"code", "code_verifier", "nonce", "redirect_uri"}'))
    }
    catch (error) {
      // A sign-in that failed counts like a wrong token; one that no rule covers does not.
      if (error instanceof HttpError && error.status === 401) recordAuthFailure(address)
      throw error
    }
  }
  let who
  try {
    who = identify(req.headers.authorization)
  }
  catch (error) {
    if (!(error instanceof Ended)) throw error
    // Told to the holder only, and not a failed authentication: the credential was a real one.
    res.setHeader('www-authenticate', 'Bearer realm="shipwick", error="invalid_token"')
    return sendError(res, 401, error.code, error.message, error.details)
  }
  if (!who) {
    recordAuthFailure(address)
    res.setHeader('www-authenticate', 'Bearer realm="shipwick"')
    return sendError(res, 401, 'UNAUTHORIZED', 'missing or invalid API token')
  }
  // Written when the request has been answered, whatever the answer: refusals are part of the trail.
  if (!BEFORE_06 && AUDITED[route]) auditWhenAnswered(req, res, route, segments, who)
  if (!roleCovers(who.role, required)) {
    return sendError(res, 403, 'FORBIDDEN', forbiddenMessage(who, required), { role: who.role, required })
  }
  // A document names its application itself: a limited token is held to that name once the document has been read.
  const document = route === 'POST /applications' || route === 'POST /validate'
  if (!BEFORE_06 && !document) refuseLimited(who, required, segments)
  // Stop/start events name the actor unless it is root, as the agent does.
  const byWho = who.name === 'root' ? '' : ` by ${who.name}`

  switch (route) {
    case 'GET /server': {
      const routes = [...apps.values()].filter(a => appSummary(a).domain && a.desired_state === 'running' && a.active_deployment_id).length
      return sendJSON(res, 200, {
        agent_version: VERSION,
        hostname: SERVER_HOSTNAME,
        os: 'Ubuntu 24.04.1 LTS',
        kernel: '6.8.0-51-generic',
        architecture: 'x86_64',
        docker_version: '27.4.1',
        cpus: 4,
        memory_bytes: 8 * 1024 ** 3 - 212 * 1024 ** 2,
        applications: apps.size,
        containers: [...apps.values()].reduce((n, a) => n + a.containers.filter(c => c.state === 'running').length, 0),
        proxy: { enabled: PROXY_ENABLED, reachable: PROXY_ENABLED, error: '', routes: PROXY_ENABLED ? routes : 0, ...(OLD_AGENT ? {} : { dns_challenge: DNS_CHALLENGE }) },
        token: who,
        notifications: { webhook: WEBHOOK },
        ...(OLD_AGENT ? {} : { dashboard_url: DASHBOARD_URL, alerts: activeAlerts(), disk: diskUsage(), backups: backupStatus() }),
        ...(BEFORE_06
          ? {}
          : {
              network: NETWORK
                ? { proxy: 'proxy.corp.example:3128', docker_proxy: false, ca_file: true, dns_resolvers: ['system'], acme_directory: 'https://ca.corp.example/acme/acme/directory' }
                : { proxy: '', docker_proxy: false, ca_file: false, dns_resolvers: [], acme_directory: '' },
              sign_in: { configured: SIGN_IN, issuer: signInConfig().issuer, ...(BEFORE_07 ? {} : { name_claim: SIGN_IN ? NAME_CLAIM : '' }) },
            }),
        ...(BEFORE_07 ? {} : { log_archive: logArchiveStatus(), update: updateStatus() }),
      })
    }

    case 'GET /applications/:p/logs/archive': {
      const app = requireApp(param)
      const kind = url.searchParams.get('kind') ?? ''
      if (kind !== '' && kind !== 'replica' && kind !== 'run') throw new HttpError(400, 'INVALID_REQUEST', 'kind must be replica or run')
      const source = logSource(url, app)
      const before = idParam(url, 'before')
      const limit = intParam(url, 'limit', 50, 1, 500)
      const list = logArchive
        .filter(e => e.application === app.name && (kind === '' || e.kind === kind) && (source.deployment === 0 || e.deployment_id === source.deployment)
          && (source.replica === 0 || e.replica === source.replica) && (source.run === 0 || e.run_id === source.run) && (before === 0 || e.id < before))
        .sort((a, b) => b.id - a.id)
        .slice(0, limit)
      return sendJSON(res, 200, list.map(archiveView))
    }

    case 'GET /applications/:p/logs/archive/:x': {
      const app = requireApp(param)
      if (!/^\d+$/.test(segments[4] ?? '') || Number(segments[4]) < 1) throw new HttpError(400, 'INVALID_REQUEST', 'archive id must be a positive number')
      const tail = intParam(url, 'tail', 0, 1, 10000)
      const entry = logArchive.find(e => e.id === Number(segments[4]) && e.application === app.name)
      if (!entry) throw new HttpError(404, 'NOT_FOUND', 'not found')
      return sendJSON(res, 200, { ...entry, output: tail === 0 ? entry.output : entry.output.slice(-tail) })
    }

    case 'GET /applications/:p/logs/search': {
      const app = requireApp(param)
      const text = url.searchParams.get('q') ?? ''
      const cursor = url.searchParams.get('cursor') ?? ''
      if (Buffer.byteLength(text) > 256) throw new HttpError(400, 'INVALID_REQUEST', 'q must be at most 256 bytes')
      // eslint-disable-next-line no-control-regex
      if (/[\r\n\x00]/.test(text)) throw new HttpError(400, 'INVALID_REQUEST', 'q must be one line: a search looks inside lines')
      if (cursor.length > 64) throw new HttpError(400, 'INVALID_REQUEST', 'cursor must be the next of an earlier answer')
      const since = timeParam(url, 'since')
      const until = timeParam(url, 'until')
      if (since !== null && until !== null && until < since) throw new HttpError(400, 'INVALID_REQUEST', 'until must not be before since')
      const source = logSource(url, app)
      const limit = intParam(url, 'limit', 200, 1, 1000)
      return sendJSON(res, 200, searchLogs(app, { text, since, until, ...source, limit, cursor }))
    }

    case 'GET /applications/:p/config': {
      const escape = boolParam(url, 'escape')
      const app = requireApp(param)
      const active = requireActive(app)
      const written = documentOf(active.spec, references.get(active.id))
      return sendJSON(res, 200, {
        application: app.name,
        deployment_id: active.id,
        sequence: active.sequence,
        version: active.version,
        // With escape a literal ${NAME} outside the secret values is written $${NAME}, for a file the CLI reads.
        document: escape ? written.document.replace(/^((?:entrypoint|command|user|image|domain|path):.*)$/gm, line => line.replace(/(?<!\$)\$\{/g, '$$${')) : written.document,
        masked: written.masked,
        ...(active.static ? { static_digest: active.static.digest } : {}),
      })
    }

    case 'POST /validate': {
      const body = await readConfig(req)
      const name = documentName(who, body)
      const sp = parseSpec(name, body, { validating: true })
      checkConflicts(apps.get(name) ?? null, sp)
      return sendJSON(res, 200, { valid: true })
    }

    case 'POST /applications': {
      const digest = staticDigest(url)
      const body = await readConfig(req)
      const name = documentName(who, body)
      if (name !== '') res.auditApplication = name
      const d = deployDocument(name, body, digest, who)
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'PUT /tokens/:p': {
      if (param === 'root') throw new HttpError(400, 'INVALID_REQUEST', 'the root token is the one the agent is configured with: it is admin, has no end, and is changed on the agent, not here')
      if (!TOKEN_NAME.test(param ?? '')) throw new HttpError(400, 'INVALID_REQUEST', `invalid token name ${JSON.stringify(param)}: use lowercase letters, digits and dashes (max 40 characters), e.g. ci`)
      const body = await readJSON(req, ['applications', 'expires_at', 'never_expires'])
      const applications = body.applications ?? null
      const expiresAt = body.expires_at ?? null
      const never = body.never_expires === true
      if (applications === null && expiresAt === null && !never) {
        throw new HttpError(400, 'INVALID_REQUEST', 'nothing to change: name the applications, the end, or both, e.g. {"applications": ["my-api", "web"]} or {"expires_at": "2027-01-31T00:00:00Z"}')
      }
      if (expiresAt !== null && never) throw new HttpError(400, 'INVALID_REQUEST', 'expires_at and never_expires contradict each other: send one')
      let expiresAtMs = null
      if (expiresAt !== null) {
        expiresAtMs = typeof expiresAt === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(expiresAt) ? Date.parse(expiresAt) : Number.NaN
        if (Number.isNaN(expiresAtMs)) throw new HttpError(400, 'INVALID_REQUEST', 'invalid JSON body: expires_at must be an RFC 3339 time, e.g. 2027-01-31T23:59:59Z')
        if (expiresAtMs <= Date.now()) throw new HttpError(400, 'INVALID_REQUEST', `expires_at is in the past. To stop a token now, revoke it: DELETE /tokens/${param}`)
      }
      const t = tokens.get(param)
      if (!t) throw new HttpError(404, 'NOT_FOUND', 'not found')
      const before = { applications: t.applications ?? [], expires_at: t.expires_at ?? null }
      if (applications !== null) {
        const limits = validateLimits(t.role, applications)
        if (limits === null) throw new HttpError(400, 'INVALID_REQUEST', 'only a deploy token can be limited to applications: a read token changes nothing, and admin is for the whole server')
        t.applications = limits
      }
      // Kept in UTC, to the second.
      if (never) t.expires_at = null
      else if (expiresAtMs !== null) t.expires_at = iso(Math.floor(expiresAtMs / 1000) * 1000)
      const list = names => (names.length === 0 ? 'all' : names.join(' '))
      const end = at => (at === null ? 'never' : at.replace(/\.\d+Z$/, 'Z'))
      const changes = []
      if (before.applications.join(' ') !== t.applications.join(' ')) changes.push(`applications ${list(before.applications)} -> ${list(t.applications)}`)
      if (end(before.expires_at) !== end(t.expires_at)) changes.push(`expires ${end(before.expires_at)} -> ${end(t.expires_at)}`)
      res.auditDetail = changes.length > 0 ? changes.join(', ') : 'nothing changed'
      return sendJSON(res, 200, tokenView(t))
    }

    case 'GET /audit/export': {
      const format = url.searchParams.get('format') ?? ''
      if (format !== 'csv' && format !== 'json') throw new HttpError(400, 'INVALID_REQUEST', 'format is required: csv, or json for one entry per line')
      if (url.searchParams.has('limit') || url.searchParams.has('before')) {
        throw new HttpError(400, 'INVALID_REQUEST', 'an export is everything that matches, not a page: leave limit and before out, and narrow it with since, application, actor, action or outcome')
      }
      const filter = auditFilter(url)
      const list = audit.filter(filter.matches).sort((a, b) => b.id - a.id)
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-')
      res.auditDetail = [format, ...(filter.described ? [filter.described] : []), `${list.length} entries`].join(', ')
      res.writeHead(200, {
        'content-type': format === 'json' ? 'application/x-ndjson' : 'text/csv; charset=utf-8',
        'content-disposition': `attachment; filename="shipwick-audit-${stamp}.${format === 'json' ? 'ndjson' : 'csv'}"`,
        'trailer': 'X-Shipwick-Export-Error',
      })
      if (format === 'json') {
        for (const e of list) res.write(`${JSON.stringify(e)}\n`)
        return res.end()
      }
      // A cell a spreadsheet would run as a formula is written as text.
      const safe = value => (/^[=+\-@\t\r]/.test(value) ? `'${value}` : value)
      const cell = value => (/[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value)
      res.write('id,at,actor_kind,actor,address,forwarded_for,action,application,target,outcome,status,code,detail\n')
      for (const e of list) {
        const row = [String(e.id), e.at.replace(/\.\d+Z$/, 'Z'), ...[e.actor.kind, e.actor.name, e.address, e.forwarded_for, e.action, e.application, e.target, e.outcome].map(safe), String(e.status), safe(e.code), safe(e.detail)]
        res.write(`${row.map(cell).join(',')}\n`)
      }
      return res.end()
    }

    case 'GET /applications/:p/traffic': {
      const app = requireApp(param)
      const since = url.searchParams.get('since') || '1h'
      if (!TRAFFIC_WINDOWS[since]) throw new HttpError(400, 'INVALID_REQUEST', 'since must be 1h, 24h or 7d')
      requireTraffic()
      return sendJSON(res, 200, traffic(app, since))
    }

    case 'GET /applications/:p/requests': {
      const app = requireApp(param)
      const tail = intParam(url, 'tail', 50, 1, 200)
      requireTraffic()
      return sendJSON(res, 200, recentRequests(app, tail))
    }

    case 'POST /applications/:p/validate': {
      // What deploy would answer for the same document, without recording anything. An unknown application is not an error: it is what a first deployment looks like.
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(param)) throw new HttpError(400, 'INVALID_REQUEST', 'name: lowercase letters, digits and dashes only')
      const sp = parseSpec(param, await readConfig(req), { validating: true })
      checkConflicts(apps.get(param) ?? null, sp)
      return sendJSON(res, 200, { valid: true })
    }

    case 'POST /applications/:p/images/missing': {
      // The mock holds no image store: it lacks every layer it is asked about.
      const body = await readJSON(req, ['layers'])
      if (!Array.isArray(body.layers) || body.layers.some(l => typeof l !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(l))) {
        throw new HttpError(400, 'INVALID_REQUEST', 'layers must be a list of sha256:<64 hex characters> digests')
      }
      return sendJSON(res, 200, { missing: body.layers })
    }

    case 'GET /applications/:p/backups': {
      const app = requireApp(param)
      const limit = intParam(url, 'limit', 50, 1, 500)
      return sendJSON(res, 200, backupsOf(app.name).slice(0, limit).map(backupView))
    }

    case 'GET /applications/:p/backups/:x': {
      const app = requireApp(param)
      return sendJSON(res, 200, backupDetail(requireBackup(app.name, segments[3])))
    }

    case 'POST /applications/:p/backups': {
      const app = requireApp(param)
      const active = requireActive(app)
      const volumes = active.spec.volumes ?? []
      if (volumes.length === 0) throw new HttpError(409, 'NO_VOLUMES', 'the application has no volumes; a backup is an archive of its volumes')
      requireIdle(app)
      const sizes = volumes.map(v => ({ volume: v.name, size_bytes: managedVolumes().find(m => m.application === app.name && m.volume === v.name && m.size_bytes > 0)?.size_bytes ?? 4096 }))
      const run = startBackup(app.name, sizes, active.spec.backups?.keep ?? 0, 4 * SECOND)
      return sendJSON(res, 202, backupView(run), { location: `/api/v1/applications/${app.name}/backups/${run.id}` })
    }

    case 'POST /applications/:p/backups/:x/verify': {
      const app = requireApp(param)
      const run = requireBackup(app.name, segments[3], true)
      requireUsable(run)
      requireActive(app)
      run.activity = 'verify'
      setTimeout(() => {
        if (!backups.has(run.id)) return
        run.activity = ''
        if (VERIFY_FAILS) {
          Object.assign(run, { verified_at: null, verify_error: 'the container did not become healthy on the restored data within 2m: TCP :5432: connection refused', verify_output: 'LOG:  starting PostgreSQL 17.2 on x86_64-pc-linux-musl\nLOG:  invalid checkpoint record\nPANIC:  could not locate a valid checkpoint record' })
          addAppEvent(app.name, 'warn', 'backup', `Backup #${run.id} did not verify: ${run.verify_error}`)
        }
        else {
          Object.assign(run, { verified_at: iso(Date.now()), verify_error: '', verify_output: VERIFY_OUTPUT })
          addAppEvent(app.name, 'info', 'backup', `Backup #${run.id} verified: it restores, and a container of the current image passed its health check on it`)
        }
      }, 5 * SECOND).unref()
      return sendJSON(res, 202, backupView(run))
    }

    case 'POST /applications/:p/backups/:x/restore': {
      const app = requireApp(param)
      const run = requireBackup(app.name, segments[3])
      requireUsable(run)
      requireIdle(app)
      const active = requireActive(app)
      // A backup may hold a volume the application no longer mounts: there is nowhere to restore it to.
      if (run.volumes.some(v => !(active.spec.volumes ?? []).some(m => m.name === v.volume))) throw new HttpError(404, 'NOT_FOUND', 'the backup holds a volume the application no longer has')
      if (app.desired_state !== 'stopped' || app.containers.some(c => c.state === 'running')) {
        throw new HttpError(409, 'APPLICATION_RUNNING', 'the application is running; stop it first with: shipwick stop')
      }
      run.activity = 'restore'
      setTimeout(() => {
        if (!backups.has(run.id)) return
        Object.assign(run, { activity: '', restored_at: iso(Date.now()), restore_error: '' })
        if (apps.get(app.name) !== app) return
        // The replica is created again around the restored volumes, and stays stopped.
        app.containers = makeContainers(app, active, { 1: { state: 'created', started_at: null, health: active.spec.health ? 'unknown' : '' } })
        app.updated_at = iso(Date.now())
        for (const v of run.volumes) addAppEvent(app.name, 'info', 'app', `Volume ${v.volume} restored from a backup (${formatSize(v.size_bytes)})`)
      }, 4 * SECOND).unref()
      return sendJSON(res, 202, backupView(run))
    }

    case 'GET /applications/:p/backups/:x/volumes/:y/archive': {
      const app = requireApp(param)
      const run = requireBackup(app.name, segments[3])
      requireUsable(run)
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(segments[5] ?? '')) throw new HttpError(400, 'INVALID_REQUEST', 'volume: lowercase letters, digits and dashes only')
      if (!run.volumes.some(v => v.volume === segments[5])) throw new HttpError(404, 'NOT_FOUND', 'the backup holds no volume by that name')
      const archive = tarArchive(VOLUME_FILES)
      res.writeHead(200, {
        'content-type': 'application/x-tar',
        'content-disposition': `attachment; filename="${app.name}-${segments[5]}-backup-${run.id}.tar"`,
        'content-length': archive.length,
      })
      return res.end(archive)
    }

    case 'DELETE /applications/:p/backups/:x': {
      const app = requireApp(param)
      const run = requireBackup(app.name, segments[3])
      if (backupBusy(run)) throw new HttpError(409, 'BACKUP_BUSY', 'the backup is in use: it is still being taken, verified or restored')
      backups.delete(run.id)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /server/backups':
      return sendJSON(res, 200, backupsOf(STATE).slice(0, intParam(url, 'limit', 50, 1, 500)).map(backupView))

    case 'GET /server/backups/:x':
      return sendJSON(res, 200, backupView(requireBackup(STATE, segments[2])))

    case 'POST /server/backups': {
      // The key is never written unencrypted, so without a passphrase there is no backup of the state at all.
      if (!PASSPHRASE) throw new HttpError(409, 'BACKUPS_NOT_ENCRYPTED', STATE_NOT_ENCRYPTED)
      if (backupsOf(STATE).some(backupBusy)) throw new HttpError(409, 'BACKUP_BUSY', 'a backup of the agent\'s state is already running')
      const run = startBackup(STATE, [{ volume: 'shipwick.db', size_bytes: 1560576 }, { volume: 'encryption.key', size_bytes: 1536 }], STATE_KEEP, 3 * SECOND)
      return sendJSON(res, 202, backupView(run), { location: `/api/v1/server/backups/${run.id}` })
    }

    case 'POST /server/backups/adopt': {
      const body = await readJSON(req, ['application'])
      if (body.application !== undefined && (typeof body.application !== 'string' || !APPLICATION_NAME.test(body.application))) {
        throw new HttpError(400, 'INVALID_REQUEST', 'application: lowercase letters, digits and dashes only')
      }
      if (ADOPT_FOREIGN) {
        throw new HttpError(409, 'FOREIGN_BUCKET', 'the bucket holds the backups of another Shipwick installation under this prefix: to carry on as that installation, restore its state on this server (handbook: Restoring the agent\'s state); to keep the two apart, set SHIPWICK_BACKUP_S3_PREFIX to a prefix of this server\'s own')
      }
      // What a restored database has forgotten: two of postgres's backups, one of the state, and a state backup that was never finished.
      const found = adoptable && apps.has('postgres')
        ? [
            ['application', 'postgres', { startedAtMs: Date.now() - 26 * HOUR, volumes: [{ volume: 'data', size_bytes: 2254857830 }] }],
            ['application', 'postgres', { startedAtMs: Date.now() - 2 * HOUR, volumes: [{ volume: 'data', size_bytes: 2273208910 }] }],
            ['state', '', { startedAtMs: Date.now() - 20 * HOUR, volumes: [{ volume: 'shipwick.db', size_bytes: 1560576 }, { volume: 'encryption.key', size_bytes: 1536 }] }],
          ].filter(([, application]) => body.application === undefined || application === body.application)
        : []
      const adopted = found.map(([kind, application, run]) => ({
        kind,
        application,
        backup: backupView(addBackup(kind === 'state' ? STATE : application, { trigger: 'adopted', durationMs: 0, status: 'succeeded', ...run })),
      }))
      const skipped = found.length > 0 && body.application === undefined
        ? [{ kind: 'state', application: '', id: nextBackupId++, reason: 'encryption.key.enc is missing: the backup was not finished' }]
        : []
      if (found.length > 0 && body.application === undefined) adoptable = false
      return sendJSON(res, 200, { adopted, skipped })
    }

    case 'GET /audit': {
      const filter = auditFilter(url)
      const limit = intParam(url, 'limit', 50, 1, 500)
      // One more than asked for is read, to know whether older entries match.
      const list = audit.filter(filter.matches).sort((a, b) => b.id - a.id).slice(0, limit + 1)
      if (BEFORE_07) return sendJSON(res, 200, list.slice(0, limit))
      // `more` stands next to `data`, not inside it.
      const body = JSON.stringify({ data: list.slice(0, limit), more: list.length > limit })
      res.writeHead(200, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body) })
      return res.end(body)
    }

    case 'GET /standby/promotion':
      if (!promotion) throw new HttpError(404, 'NOT_FOUND', 'this server was never promoted')
      return sendJSON(res, 200, promotion)

    case 'DELETE /auth/session': {
      const value = String(req.headers.authorization ?? '').slice(7)
      if (who.kind !== 'user' || !sessions.has(value)) throw new HttpError(400, 'INVALID_REQUEST', 'this request was made with an API token, which has no session to end; a token is revoked with: shipwick token revoke')
      // Signed out by its holder: the session is gone, and its value is a wrong token from now on.
      sessions.delete(value)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /access/rules':
      return sendJSON(res, 200, accessRules)

    case 'POST /access/rules': {
      const body = await readJSON(req, ['kind', 'subject', 'role', 'applications'])
      if (!body.kind || !body.subject) throw new HttpError(400, 'INVALID_REQUEST', 'kind and subject are required, e.g. {"kind": "email", "subject": "ada@example.com", "role": "deploy"}')
      if (body.role === undefined || body.role === '') throw new HttpError(400, 'INVALID_REQUEST', 'role is required: read, deploy or admin')
      if (!ROLES.includes(body.role)) throw new HttpError(400, 'INVALID_REQUEST', `invalid role ${JSON.stringify(body.role)}: use read, deploy or admin`)
      let subject = String(body.subject)
      if (body.kind === 'email') {
        subject = subject.toLowerCase()
        if (!/^[a-z0-9._%+-]+@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(subject)) throw new HttpError(400, 'INVALID_REQUEST', 'not an e-mail address; use one like ada@example.com')
      }
      else if (body.kind === 'domain') {
        subject = subject.toLowerCase()
        if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(subject)) throw new HttpError(400, 'INVALID_REQUEST', `invalid domain ${JSON.stringify(subject)}: use the part after the @, e.g. example.com`)
      }
      else if (body.kind === 'group') {
        // eslint-disable-next-line no-control-regex
        if (subject.length > 256 || subject !== subject.trim() || /[\x00-\x1F\x7F]/.test(subject)) throw new HttpError(400, 'INVALID_REQUEST', 'invalid group: use the name as the provider sends it, at most 256 characters')
      }
      else if (body.kind === 'name' && !BEFORE_07) {
        // Exactly as the claim holds it: case kept, nothing normalised.
        if (subject.length > 254 || !/^[A-Za-z0-9._%+'@|:=#~-]+$/.test(subject)) throw new HttpError(400, 'INVALID_REQUEST', 'not a name the provider\'s claim can hold: at most 254 letters, digits and . _ % + \' @ | : = # ~ -, without spaces')
      }
      else throw new HttpError(400, 'INVALID_REQUEST', `invalid kind ${JSON.stringify(body.kind)}: use email, group${BEFORE_07 ? ' or domain' : ', domain or name'}`)
      const applications = validateLimits(body.role, body.applications)
      if (applications === null) throw new HttpError(400, 'INVALID_REQUEST', 'only the deploy role can be limited to applications: read changes nothing, and admin is for the whole server')
      const existing = accessRules.find(r => r.kind === body.kind && r.subject === subject)
      if (!existing && accessRules.length >= 200) throw new HttpError(400, 'INVALID_REQUEST', 'at most 200 access rules can be stored; a group or a domain covers many people with one')
      // Granting again replaces the rule: 200 instead of 201.
      const rule = existing ?? { id: nextRuleId++, kind: body.kind, subject }
      Object.assign(rule, { role: body.role, applications, created_at: iso(Date.now()), created_by: who.name })
      if (!existing) accessRules.push(rule)
      res.auditTarget = subjectOf(rule)
      res.auditDetail = grantDetail(rule)
      return sendJSON(res, existing ? 200 : 201, rule)
    }

    case 'DELETE /access/rules/:p': {
      const id = Number(segments[2])
      if (!Number.isInteger(id) || id < 1) throw new HttpError(400, 'INVALID_REQUEST', 'rule id must be a positive number')
      const index = accessRules.findIndex(r => r.id === id)
      res.auditTarget = index === -1 ? String(id) : subjectOf(accessRules[index])
      if (index === -1) throw new HttpError(404, 'NOT_FOUND', 'not found')
      accessRules.splice(index, 1)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /access/sessions':
      return sendJSON(res, 200, liveSessions().map(({ id, email, role, applications, created_at, expires_at, last_used_at }) => ({ id, email, role, applications, created_at, expires_at, last_used_at })))

    case 'DELETE /access/sessions/:p': {
      // The name as the sessions hold it: an address in lowercase with the email claim, otherwise exactly what the claim held.
      const byAddress = BEFORE_07 || NAME_CLAIM === 'email'
      const email = byAddress ? String(segments[2] ?? '').toLowerCase() : String(segments[2] ?? '')
      if (byAddress && !/^[^\s@]+@[^\s@]+$/.test(email)) throw new HttpError(400, 'INVALID_REQUEST', 'not an e-mail address; use one like ada@example.com')
      const mine = liveSessions().filter(s => s.email === email)
      for (const s of mine) s.ended = { reason: 'signed_out', message: 'an admin ended this session. Sign in again' }
      res.auditDetail = plural(mine.length, 'session')
      return sendJSON(res, 200, { sessions: mine.length })
    }

    case 'POST /server/rotate-key': {
      if (KEY_FROM_ENVIRONMENT && rotationPending) {
        throw new HttpError(409, 'KEY_ROTATION_PENDING', 'the key was already rotated since the agent started, and its environment still holds the old one: put the new key in the agent\'s environment and restart it first', { key_file: `${DATA_DIR}/encryption.key.new` })
      }
      // What is sealed: secrets, registry passwords and certificate keys, and every deployment whose spec holds a value.
      const counts = {
        values: secrets.size + registries.size + certificates.size,
        deployments: [...deployments.values()].filter(d => Object.keys(d.spec.env ?? {}).length > 0 || d.spec.proxy?.basic_auth?.length).length,
      }
      if (!KEY_FROM_ENVIRONMENT) return sendJSON(res, 200, { ...counts, key_source: 'file', key_file: `${DATA_DIR}/encryption.key` })
      rotationPending = true
      // The one time the key is shown: the agent cannot change its own environment.
      return sendJSON(res, 200, { ...counts, key_source: 'environment', key: randomBytes(32).toString('hex'), key_file: `${DATA_DIR}/encryption.key.new` })
    }

    case 'POST /export': {
      // The real answer is the whole server, encrypted with the passphrase; here it is a file that starts like one, sent in pieces as the real one is.
      let body
      try {
        body = await readJSON(req, ['passphrase', 'applications'], 64 * 1024, '{"passphrase": "…"}')
      }
      catch {
        // The body holds a passphrase: the error says nothing of its content.
        throw new HttpError(400, 'INVALID_REQUEST', 'the body must be JSON: {"passphrase": "…"}')
      }
      if (typeof body.passphrase !== 'string' || body.passphrase.length < 12) throw new HttpError(400, 'INVALID_REQUEST', 'passphrase must be at least 12 characters long: it is all that protects every secret of the server')
      const chosen = Array.isArray(body.applications) ? body.applications : []
      for (const name of chosen) if (typeof name !== 'string' || !APPLICATION_NAME.test(name)) throw new HttpError(400, 'INVALID_REQUEST', 'applications: name: lowercase letters, digits and dashes only')
      for (const name of chosen) if (!apps.has(name)) throw new HttpError(404, 'NOT_FOUND', `no application named ${JSON.stringify(name)}`)
      const busy = [...apps.values()].find(a => a.active_deployment_id && inFlight(a.name) && (chosen.length === 0 || chosen.includes(a.name)))
      if (busy) throw new HttpError(409, 'DEPLOYMENT_IN_PROGRESS', `another operation is in progress for ${busy.name}`)
      if (chosen.length > 0) res.auditDetail = `applications ${chosen.join(' ')}`
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-')
      // Chunked: the length is not known beforehand. The trailer is announced, and set only when the export breaks off.
      res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-disposition': `attachment; filename="shipwick-export-${stamp}.swexport"`, 'trailer': 'X-Shipwick-Export-Error' })
      const piece = randomBytes(64 * 1024)
      let sent = 0
      const write = chunk => new Promise((resolve) => {
        if (res.destroyed) return resolve()
        if (res.write(chunk)) resolve()
        else res.once('drain', resolve)
      })
      await write(Buffer.from('SWBACKUP'))
      while (sent < EXPORT_BYTES && !res.destroyed) {
        if (EXPORT_FAILS && sent >= EXPORT_BYTES / 2) {
          // Half-way: the file is left without its last chunk. The agent says why in the trailer; a connection that is cut says nothing.
          if (EXPORT_FAILS === 'breaks') return res.destroy()
          res.addTrailers({ 'X-Shipwick-Export-Error': 'archive volume data of postgres: read /var/lib/docker/volumes/shipwick_postgres_data: input/output error' })
          return res.end()
        }
        await write(piece)
        sent += piece.length
      }
      return res.end()
    }

    case 'GET /exports':
      return sendJSON(res, 200, backupsOf(EXPORTS).slice(0, intParam(url, 'limit', 50, 1, 500)).map(backupView))

    case 'GET /exports/:p':
      return sendJSON(res, 200, backupView(requireBackup(EXPORTS, param)))

    case 'POST /exports': {
      // An export holds every secret of the server: it is only ever written encrypted.
      if (!PASSPHRASE) throw new HttpError(409, 'BACKUPS_NOT_ENCRYPTED', 'an export holds every secret of the server and is only ever written encrypted: SHIPWICK_BACKUP_PASSPHRASE is not set on the agent')
      if (backupsOf(EXPORTS).some(backupBusy)) throw new HttpError(409, 'EXPORT_IN_PROGRESS', 'an export is being written already; see it with: shipwick export --list')
      // An application with nothing deployed yet has nothing to export, whatever is in flight for it.
      const busy = [...apps.values()].find(a => a.active_deployment_id && inFlight(a.name))
      if (busy) throw new HttpError(409, 'DEPLOYMENT_IN_PROGRESS', `another operation is in progress for ${busy.name}`)
      const run = startBackup(EXPORTS, [{ volume: 'export.tar', size_bytes: 3204448256 }], EXPORT_KEEP, 5 * SECOND)
      return sendJSON(res, 202, backupView(run), { location: `/api/v1/exports/${run.id}` })
    }

    case 'POST /import': {
      const [type] = String(req.headers['content-type'] ?? '').split(';')
      if (type.trim() !== 'application/octet-stream') throw new HttpError(400, 'INVALID_REQUEST', 'the body must be an export sent as Content-Type: application/octet-stream')
      const stopped = boolParam(url, 'stopped')
      const overwrite = boolParam(url, 'overwrite')
      requireNoPromotion()
      const passphrase = Buffer.from(String(req.headers['x-shipwick-passphrase'] ?? ''), 'base64').toString('utf8')
      if (passphrase === '' || !/^[A-Za-z0-9+/]+=*$/.test(String(req.headers['x-shipwick-passphrase']))) throw new HttpError(400, 'INVALID_REQUEST', 'the passphrase of the export is sent base64-encoded in the X-Shipwick-Passphrase header')
      requireNoImport()
      res.auditDetail = `stopped ${stopped}, overwrite ${overwrite}`
      // The file is imported as it arrives and nothing of it is kept: its first
      // bytes say whether it is an export and whether the passphrase opens it.
      const body = req[Symbol.asyncIterator]()
      let head = Buffer.alloc(0)
      let received = 0
      while (head.length < 8) {
        const { value, done } = await body.next()
        if (done) break
        received += value.length
        head = Buffer.concat([head, value])
      }
      if (!head.subarray(0, 8).equals(Buffer.from('SWBACKUP'))) throw new HttpError(400, 'INVALID_EXPORT', 'this is not an export: it does not start like a file shipwick export writes')
      // What stands for a passphrase that does not open the file.
      if (passphrase.startsWith('wrong')) throw new HttpError(400, 'INVALID_EXPORT', 'the passphrase does not match this backup, or the file is damaged')
      // The request stays open for the whole import and answers at the end; GET /import follows it meanwhile.
      const run = startImport({ source: 'upload', stopped, overwrite })
      let complete = true
      try {
        for (let next = await body.next(); !next.done; next = await body.next()) received += next.value.length
      }
      catch {
        complete = false
      }
      const declared = Number(req.headers['content-length'] ?? received)
      if (!complete || received < declared) {
        // The upload broke off: what was imported until then stays, and the import says so.
        Object.assign(run, { status: 'failed', completed_at: iso(Date.now()), error: 'the export breaks off: the upload ended before the file did' })
        lastImport = run
        throw new HttpError(400, 'INVALID_EXPORT', 'the export breaks off: the upload ended before the file did')
      }
      await new Promise((resolve) => {
        const timer = setInterval(() => {
          if (run.status !== 'running') {
            clearInterval(timer)
            resolve()
          }
        }, 250)
      })
      return sendJSON(res, 200, run)
    }

    case 'GET /import':
      if (!lastImport) throw new HttpError(404, 'NOT_FOUND', 'no import has run on this server since the agent started')
      return sendJSON(res, 200, lastImport)

    case 'GET /standby':
      return sendJSON(res, 200, standbyView())

    case 'POST /standby/pull': {
      if (!IS_STANDBY) throw new HttpError(409, 'STANDBY_NOT_CONFIGURED', 'this agent has no bucket to fetch exports from: set SHIPWICK_BACKUP_S3_* and SHIPWICK_BACKUP_PASSPHRASE to those of the server it stands by for, and SHIPWICK_STANDBY_SCHEDULE')
      requireNoImport()
      requireNoPromotion()
      standbyPull.last_export += 1
      standbyPull.last_at = iso(Date.now())
      const run = startImport({ source: `export #${standbyPull.last_export} from the bucket`, stopped: true, overwrite: true })
      return sendJSON(res, 202, run, { location: '/api/v1/import' })
    }

    case 'POST /standby/promote': {
      requireNoImport()
      if (!BEFORE_06) {
        const raw = url.searchParams.get('wait')
        if (raw !== null && raw !== 'true' && raw !== 'false') throw new HttpError(400, 'INVALID_REQUEST', 'wait must be true or false')
        requireNoPromotion()
        // Nothing waits: answered complete, and the record of the last real promotion stays.
        if (standbyApplications().length === 0) return sendJSON(res, 200, emptyPromotion())
        const record = startPromotion(3 * SECOND, byWho)
        if (raw === 'false') return sendJSON(res, 202, record, { location: '/api/v1/standby/promotion' })
        // Held until it has ended, as a client from before 0.6 expects.
        await new Promise((resolve) => {
          const timer = setInterval(() => {
            if (record.completed_at !== null) {
              clearInterval(timer)
              resolve()
            }
          }, 250)
        })
        return sendJSON(res, 200, record)
      }
      const waiting = standbyApplications()
      // Started in the order they were imported; the answer comes when the last one is ready.
      await new Promise(resolve => setTimeout(resolve, 1200 + waiting.length * 900))
      const started = waiting.map(([app, active]) => {
        if (apps.get(app.name) !== app) return { name: app.name, status: 'failed', message: 'the application was deleted while the promotion ran' }
        app.desired_state = 'running'
        app.containers = makeContainers(app, { ...active, completed_at: iso(Date.now()) })
        app.updated_at = iso(Date.now())
        addAppEvent(app.name, 'info', 'app', `Application started${byWho}`)
        return { name: app.name, status: 'running', message: '' }
      })
      return sendJSON(res, 200, { applications: started, records: recordsFor(waiting) })
    }

    case 'GET /registries':
      return sendJSON(res, 200, [...registries.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([registry, r]) => ({ registry, ...r })))

    case 'PUT /registries/:p': {
      const registry = normalizeRegistry(param)
      const body = await readJSON(req, ['username', 'password'])
      validateRegistryCredential(body.username, body.password)
      if (!registries.has(registry) && registries.size >= 50) throw new HttpError(400, 'INVALID_REQUEST', 'at most 50 registries can be stored; remove one first')
      // The registry is asked before anything is stored; the password is neither kept nor echoed.
      registryLogin(registry)
      const now = iso(Date.now())
      registries.set(registry, { username: body.username, created_at: registries.get(registry)?.created_at ?? now, updated_at: now })
      res.writeHead(204)
      return res.end()
    }

    case 'DELETE /registries/:p': {
      const registry = normalizeRegistry(param)
      if (!registries.delete(registry)) throw new HttpError(404, 'NOT_FOUND', `no credential is stored for ${registry}`)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /certificates':
      return sendJSON(res, 200, [...certificates.entries()].sort(([a], [b]) => a.localeCompare(b)).map(certificateView))

    case 'PUT /certificates/:p': {
      const hostname = certificateHostname(param)
      const body = await readJSON(req, ['certificate', 'key'], 513 * 1024, '{"certificate": "<PEM>", "key": "<PEM>"}')
      const said = checkCertificate(hostname, body)
      if (!certificates.has(hostname) && certificates.size >= 50) throw new HttpError(400, 'INVALID_REQUEST', 'at most 50 certificates can be stored; remove one first')
      const now = iso(Date.now())
      // Neither the PEM nor the key is kept: only what the certificate says about itself.
      certificates.set(hostname, { ...said, created_at: certificates.get(hostname)?.created_at ?? now, updated_at: now })
      return sendJSON(res, 200, certificateView([hostname, certificates.get(hostname)]))
    }

    case 'DELETE /certificates/:p': {
      const hostname = certificateHostname(param)
      if (!certificates.delete(hostname)) throw new HttpError(404, 'NOT_FOUND', 'not found')
      res.writeHead(204)
      return res.end()
    }

    case 'GET /applications':
      return sendJSON(res, 200, [...apps.values()].map(appSummary).sort((a, b) => a.name.localeCompare(b.name)))

    case 'GET /applications/:p':
      return sendJSON(res, 200, appDetail(requireApp(param)))

    case 'DELETE /applications/:p': {
      const app = requireApp(param)
      requireIdle(app)
      endFollowers(app.name)
      // The volumes stay, on purpose: GET /volumes lists them as orphans until someone removes them.
      for (const v of managedVolumes()) if (v.application === app.name && !v.orphan) orphanVolumes.push({ name: v.name, application: v.application, volume: v.volume, size_bytes: v.size_bytes })
      apps.delete(app.name)
      appEvents.delete(app.name)
      logBuffers.delete(app.name)
      uploads.delete(app.name)
      for (const d of [...deployments.values()]) if (d.application === app.name) deployments.delete(d.id)
      for (const r of [...runs.values()]) if (r.application === app.name) runs.delete(r.id)
      // Its kept output goes with it.
      for (let i = logArchive.length - 1; i >= 0; i--) if (logArchive[i].application === app.name) logArchive.splice(i, 1)
      res.writeHead(204)
      return res.end()
    }

    case 'POST /applications/:p/deploy': {
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(param)) throw new HttpError(400, 'INVALID_REQUEST', 'name: lowercase letters, digits and dashes only')
      const digest = staticDigest(url)
      const d = deployDocument(param, await readConfig(req), digest, who)
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'POST /applications/:p/redeploy': {
      const app = requireApp(param)
      const body = await readJSON(req, ['image'])
      requireIdle(app)
      const active = requireActive(app)
      const sp = structuredClone(active.spec)
      if (body.image !== undefined && body.image !== '') {
        if (isStaticSpec(sp)) throw new HttpError(400, 'INVALID_REQUEST', 'image: a static application has no image; to serve other files, deploy the folder again')
        if (typeof body.image !== 'string' || !IMAGE_PATTERN.test(body.image)) {
          throw new HttpError(400, 'INVALID_REQUEST', `image: invalid reference ${JSON.stringify(body.image)}`)
        }
        if (sp.build && !isLocalImage(body.image)) {
          throw new HttpError(400, 'INVALID_REQUEST', 'image: this application is built by shipwick deploy; run it from the project to deploy a new image, or remove build from deploy.yaml')
        }
        sp.image = body.image
      }
      // A stored configuration's hostnames and ports may have been taken since.
      checkConflicts(app, sp)
      const d = startDeployment(app, sp, { kind: 'redeploy', sourceId: active.id, by: who.name, files: active.static ?? null })
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'POST /applications/:p/rollback': {
      const app = requireApp(param)
      const body = await readJSON(req, ['deployment_id'])
      requireIdle(app)
      requireActive(app)
      // Only deployments that served successfully and were replaced since are targets:
      // not the active one, not a FAILED or ROLLED_BACK attempt, not another application's.
      const candidates = [...deployments.values()]
        .filter(d => d.application === app.name && d.status === 'SUPERSEDED')
        .sort((a, b) => b.id - a.id)
      const wanted = body.deployment_id
      if (wanted !== undefined && (!Number.isInteger(wanted) || wanted < 0)) {
        throw new HttpError(400, 'INVALID_REQUEST', 'deployment_id must be a positive number')
      }
      const target = wanted ? candidates.find(d => d.id === wanted) : candidates[0]
      if (!target) throw new HttpError(409, 'NO_ROLLBACK_TARGET', 'no earlier successful deployment to roll back to')
      checkConflicts(app, target.spec)
      const d = startDeployment(app, structuredClone(target.spec), { kind: 'rollback', sourceId: target.id, by: who.name, files: target.static ?? null })
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'POST /applications/:p/stop': {
      const app = requireApp(param)
      requireIdle(app)
      requireActive(app)
      if (app.desired_state !== 'stopped') {
        app.desired_state = 'stopped'
        for (const c of app.containers) if (c.state === 'running') archiveContainer(app, c, 'stopped', { exitCode: 0 })
        for (const c of app.containers) Object.assign(c, { state: 'exited', exit_code: 0, oom_killed: false, crash_loop: false })
        app.updated_at = iso(Date.now())
        addAppEvent(app.name, 'info', 'app', `Application stopped${byWho}`)
        endFollowers(app.name)
      }
      return sendJSON(res, 200, appDetail(app))
    }

    case 'POST /applications/:p/start': {
      const app = requireApp(param)
      requireIdle(app)
      const active = requireActive(app)
      if (app.desired_state !== 'running') {
        app.desired_state = 'running'
        for (const c of app.containers) {
          Object.assign(c, { state: 'running', exit_code: 0, health: active.spec.health ? 'healthy' : '', restarts: 0, started_at: iso(Date.now()) })
        }
        app.updated_at = iso(Date.now())
        addAppEvent(app.name, 'info', 'app', `Application started${byWho}`)
      }
      return sendJSON(res, 200, appDetail(app))
    }

    case 'GET /applications/:p/metrics/history': {
      const app = requireApp(param)
      refuseStatic(app)
      const since = url.searchParams.get('since') || '1h'
      if (!HISTORY_WINDOWS[since]) throw new HttpError(400, 'INVALID_REQUEST', 'since must be 1h, 24h or 7d')
      return sendJSON(res, 200, metricsHistory(app, since))
    }

    case 'GET /applications/:p/volumes': {
      const app = requireApp(param)
      const active = requireActive(app)
      return sendJSON(res, 200, (active.spec.volumes ?? []).map(v => ({ name: v.name, path: v.path })))
    }

    case 'PUT /applications/:p/static': {
      const app = requireApp(param)
      requireIdle(app)
      const archive = await readArchive(req, {
        limit: 512 * 1024 ** 2,
        what: 'the folder',
        wrongType: 'the body must be a tar archive of the folder sent as Content-Type: application/x-tar',
      })
      if (archive.files === 0) throw new HttpError(400, 'INVALID_REQUEST', 'the folder holds no files')
      const files = { digest: archive.digest, size_bytes: archive.fileBytes, files: archive.files }
      uploads.set(app.name, files)
      return sendJSON(res, 200, files)
    }

    case 'POST /applications/:p/images': {
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(param)) throw new HttpError(400, 'INVALID_REQUEST', 'name: lowercase letters, digits and dashes only')
      const archive = await readArchive(req, { limit: 4 * 1024 ** 3, what: 'the image' })
      // The archive's own tag is not read here; the CLI stamps the image the same way.
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-')
      return sendJSON(res, 201, { image: `${LOCAL_IMAGE_PREFIX}${param}:${stamp}-${randomHex(4)}`, size_bytes: archive.size })
    }

    case 'GET /applications/:p/jobs': {
      const app = requireApp(param)
      refuseStatic(app)
      return sendJSON(res, 200, jobsView(app))
    }

    case 'GET /applications/:p/runs': {
      const app = requireApp(param)
      const limit = intParam(url, 'limit', 50, 1, 500)
      const job = url.searchParams.get('job') ?? ''
      if (job !== '' && !JOB_NAME.test(job)) throw new HttpError(400, 'INVALID_REQUEST', 'job: lowercase letters, digits and dashes only')
      return sendJSON(res, 200, runsOf(app, job).slice(0, limit).map(runView))
    }

    case 'GET /applications/:p/runs/:x': {
      const app = requireApp(param)
      const id = Number(segments[3])
      if (!Number.isInteger(id) || id < 1) throw new HttpError(400, 'INVALID_REQUEST', 'run id must be a positive number')
      const run = runs.get(id)
      if (!run || run.application !== app.name) throw new HttpError(404, 'NOT_FOUND', 'not found')
      return sendJSON(res, 200, run)
    }

    case 'POST /applications/:p/jobs/:x/run': {
      const app = requireApp(param)
      const name = segments[3]
      if (!JOB_NAME.test(name ?? '')) throw new HttpError(400, 'INVALID_REQUEST', 'job: lowercase letters, digits and dashes only')
      refuseStatic(app)
      requireIdle(app)
      const active = requireActive(app)
      const job = (active.spec.jobs ?? []).find(j => j.name === name)
      if (!job) throw new HttpError(404, 'NOT_FOUND', `${app.name} has no job named ${JSON.stringify(name)}`)
      if (runsOf(app, name).some(r => r.status === 'running')) {
        throw new HttpError(409, 'JOB_ALREADY_RUNNING', `a run of job ${JSON.stringify(name)} has not finished yet`)
      }
      const run = startRun(app, active, { job: name, kind: 'manual', command: job.command, timeout: job.timeout })
      return sendJSON(res, 202, run, { location: `/api/v1/applications/${app.name}/runs/${run.id}` })
    }

    case 'POST /applications/:p/run': {
      const app = requireApp(param)
      const body = await readJSON(req, ['command'])
      validateCommand(body.command)
      refuseStatic(app)
      requireIdle(app)
      const active = requireActive(app)
      const run = startRun(app, active, { job: 'run', kind: 'manual', command: body.command, timeout: '10m0s' })
      return sendJSON(res, 202, run, { location: `/api/v1/applications/${app.name}/runs/${run.id}` })
    }

    case 'GET /applications/:p/volumes/:x/archive': {
      const app = requireApp(param)
      const volume = requireVolume(app, segments[3])
      requireIdle(app)
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-')
      const archive = tarArchive(VOLUME_FILES)
      res.writeHead(200, {
        'content-type': 'application/x-tar',
        'content-disposition': `attachment; filename="${app.name}-${volume.name}-${stamp}.tar"`,
        'content-length': archive.length,
      })
      return res.end(archive)
    }

    case 'PUT /applications/:p/volumes/:x/archive': {
      const app = requireApp(param)
      const volume = requireVolume(app, segments[3])
      requireIdle(app)
      if (app.desired_state !== 'stopped' || app.containers.some(c => c.state === 'running')) {
        throw new HttpError(409, 'APPLICATION_RUNNING', 'the application is running; stop it first with: shipwick stop')
      }
      const { size } = await readArchive(req)
      // The replica is created again around the fresh volume, and stays stopped.
      const active = requireActive(app)
      app.containers = makeContainers(app, active, { 1: { state: 'created', started_at: null, health: active.spec.health ? 'unknown' : '' } })
      app.updated_at = iso(Date.now())
      addAppEvent(app.name, 'info', 'app', `Volume ${volume.name} restored from a backup (${formatSize(size)})`)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /secrets':
      return sendJSON(res, 200, [...secrets.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, s]) => ({ name, ...s })))

    case 'PUT /secrets/:p': {
      requireSecretName(param)
      const body = await readJSON(req, ['value'])
      if (typeof body.value !== 'string' || body.value === '') throw new HttpError(400, 'INVALID_REQUEST', 'value is required and must not be empty')
      if (body.value.includes('\0')) throw new HttpError(400, 'INVALID_REQUEST', 'value must not contain a NUL byte')
      if (!secrets.has(param) && secrets.size >= 500) throw new HttpError(400, 'INVALID_REQUEST', 'at most 500 secrets can be stored; remove one first')
      const now = iso(Date.now())
      // The value is neither kept nor echoed: only that it was set, and when.
      secrets.set(param, { created_at: secrets.get(param)?.created_at ?? now, updated_at: now })
      res.writeHead(204)
      return res.end()
    }

    case 'DELETE /secrets/:p': {
      requireSecretName(param)
      if (!secrets.delete(param)) throw new HttpError(404, 'NOT_FOUND', 'not found')
      res.writeHead(204)
      return res.end()
    }

    case 'GET /volumes':
      return sendJSON(res, 200, managedVolumes())

    case 'DELETE /volumes/:p': {
      const parsed = parseVolumeName(param)
      if (!parsed) throw new HttpError(400, 'INVALID_REQUEST', `invalid volume name ${JSON.stringify(param)}: Shipwick names its volumes shipwick_<application>_<volume>`)
      const found = managedVolumes().find(v => v.name === param)
      if (!found) throw new HttpError(404, 'NOT_FOUND', 'no volume by that name is managed by Shipwick; list them with: shipwick volumes')
      if (!found.orphan) {
        throw new HttpError(409, 'VOLUME_IN_USE', `the volume belongs to application ${found.application}; delete the application first — its data stays until the volume is removed`, { application: found.application })
      }
      orphanVolumes.splice(orphanVolumes.findIndex(v => v.name === param), 1)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /tokens':
      return sendJSON(res, 200, [...tokens.values()].sort((a, b) => a.id - b.id).map(tokenView))

    case 'POST /tokens': {
      const body = await readJSON(req, BEFORE_06 ? ['name', 'role'] : ['name', 'role', 'applications', 'expires_at'])
      if (body.name === undefined || body.name === '') throw new HttpError(400, 'INVALID_REQUEST', 'name is required, e.g. {"name": "ci", "role": "deploy"}')
      if (body.role === undefined || body.role === '') throw new HttpError(400, 'INVALID_REQUEST', 'role is required: read, deploy or admin')
      if (typeof body.name !== 'string' || !TOKEN_NAME.test(body.name)) {
        throw new HttpError(400, 'INVALID_REQUEST', `invalid token name ${JSON.stringify(body.name)}: use lowercase letters, digits and dashes (max 40 characters), e.g. ci`)
      }
      if (body.name === 'root') throw new HttpError(400, 'INVALID_REQUEST', '"root" is the name of the token the agent is configured with; choose another')
      if (!ROLES.includes(body.role)) throw new HttpError(400, 'INVALID_REQUEST', `invalid role ${JSON.stringify(body.role)}: use read, deploy or admin`)
      if (tokens.has(body.name)) throw new HttpError(409, 'TOKEN_EXISTS', `a token named ${JSON.stringify(body.name)} exists`)
      const applications = validateLimits(body.role, body.applications)
      if (applications === null) throw new HttpError(400, 'INVALID_REQUEST', 'only a deploy token can be limited to applications: a read token changes nothing, and admin is for the whole server')
      let expiresAtMs = null
      if (body.expires_at !== undefined && body.expires_at !== null) {
        expiresAtMs = typeof body.expires_at === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(body.expires_at) ? Date.parse(body.expires_at) : Number.NaN
        if (Number.isNaN(expiresAtMs)) throw new HttpError(400, 'INVALID_REQUEST', 'invalid JSON body: expires_at must be an RFC 3339 time, e.g. 2027-01-31T23:59:59Z')
        if (expiresAtMs <= Date.now()) throw new HttpError(400, 'INVALID_REQUEST', 'expires_at is in the past: a token that has expired already would be of no use')
      }
      const t = addToken(body.name, body.role, Date.now(), null, undefined, { applications, expiresAtMs })
      res.auditTarget = t.name
      res.auditDetail = `role ${t.role}${applications.length ? `, limited to ${applications.join(' ')}` : ''}${expiresAtMs === null ? '' : `, expires ${iso(expiresAtMs).replace(/\.\d+Z$/, 'Z')}`}`

      const { last_used_at: _unused, ...created } = tokenView(t)
      return sendJSON(res, 201, { ...created, token: t.value })
    }

    case 'DELETE /tokens/:p': {
      if (param === 'root') throw new HttpError(400, 'INVALID_REQUEST', 'the root token is the one the agent is configured with; change it on the agent, not here')
      if (!TOKEN_NAME.test(param ?? '')) throw new HttpError(400, 'INVALID_REQUEST', `invalid token name ${JSON.stringify(param)}: use lowercase letters, digits and dashes (max 40 characters), e.g. ci`)
      if (!tokens.delete(param)) throw new HttpError(404, 'NOT_FOUND', 'not found')
      res.writeHead(204)
      return res.end()
    }

    case 'GET /applications/:p/events': {
      const app = requireApp(param)
      const limit = intParam(url, 'limit', 50, 1, 500)
      return sendJSON(res, 200, [...appEvents.get(app.name)].reverse().slice(0, limit))
    }

    case 'GET /applications/:p/metrics': {
      const app = requireApp(param)
      refuseStatic(app)
      requireActive(app)
      return sendJSON(res, 200, metrics(app))
    }

    case 'GET /applications/:p/logs': {
      const app = requireApp(param)
      refuseStatic(app)
      const follow = boolParam(url, 'follow')
      if (app.containers.length === 0) throw new HttpError(409, 'NOT_DEPLOYED', `${app.name} has no containers`)
      if (!follow) {
        return sendJSON(res, 200, tailLines(app, intParam(url, 'tail', 100, 1, 5000), false))
      }
      const tail = intParam(url, 'tail', 100, 0, 5000)
      res.writeHead(200, { 'content-type': 'application/x-ndjson' })
      res.flushHeaders()
      for (const line of tailLines(app, tail, true)) res.write(`${JSON.stringify(line)}\n`)
      // Nothing is running (stopped application): there is nothing to follow.
      if (!app.containers.some(c => c.state === 'running')) return res.end()
      const follower = {
        onLine: line => res.write(`${JSON.stringify(line)}\n`),
        onEnd: () => res.end(),
      }
      if (!followers.has(app.name)) followers.set(app.name, new Set())
      followers.get(app.name).add(follower)
      res.on('close', () => followers.get(app.name)?.delete(follower))
      return
    }

    case 'GET /deployments': {
      const limit = intParam(url, 'limit', 50, 1, 500)
      const application = url.searchParams.get('application') ?? ''
      if (application !== '' && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(application)) {
        throw new HttpError(400, 'INVALID_REQUEST', 'name: lowercase letters, digits and dashes only')
      }
      const list = [...deployments.values()]
        .filter(d => application === '' || d.application === application)
        .sort((a, b) => b.started_at.localeCompare(a.started_at) || b.id - a.id)
        .slice(0, limit)
        .map(deploymentView)
      return sendJSON(res, 200, list)
    }

    case 'GET /deployments/:p': {
      const id = Number(param)
      if (!Number.isInteger(id) || id < 1) throw new HttpError(400, 'INVALID_REQUEST', 'deployment id must be a positive number')
      const d = deployments.get(id)
      if (!d) throw new HttpError(404, 'NOT_FOUND', 'not found')
      return sendJSON(res, 200, d)
    }

    default:
      return sendError(res, 404, 'ENDPOINT_NOT_FOUND', `no such endpoint: ${method} ${path}`)
  }
}

seed()
startBackground()

const server = createServer((req, res) => {
  handle(req, res).catch((error) => {
    if (res.headersSent) return res.destroy()
    if (error instanceof HttpError) return sendError(res, error.status, error.code, error.message, error.details)
    console.error(error)
    sendError(res, 500, 'INTERNAL_ERROR', String(error?.message ?? error))
  })
})

// The agent gives an archive or an export as long as its upload takes, and only a read that stalls ends it; Node's five minutes for a whole request would cut a large one.
server.requestTimeout = 0

server.listen(PORT, HOST, () => {
  console.log(`Shipwick mock agent on http://${HOST}:${PORT}/api/v1`)
  console.log(`token: ${TOKEN} (${TOKEN_NAME_OF_ROLE}, ${ROLE}${TOKEN_LIMIT.length && ROLE === 'deploy' ? `, limited to ${TOKEN_LIMIT.join(' ')}` : ''})`)
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    for (const name of [...followers.keys()]) endFollowers(name)
    server.close(() => process.exit(0))
    setTimeout(() => process.exit(0), 500).unref()
  })
}
