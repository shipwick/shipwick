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
// taking over from its predecessor. POST …/deploy takes the deploy.yaml as
// JSON (the agent accepts JSON too; there is no YAML parser here), validates
// the hostnames, ports and health block, and refuses a hostname or server port
// another application holds with the agent's INVALID_CONFIG shape.
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
// Magic image tags for POST /applications/:name/redeploy {"image": ...}:
//   *:fail      replica 1 crashes: FAILED, nothing of the old version was touched
//   *:rollback  replica 1 is replaced, replica 2 crashes: FAILED → ROLLBACK →
//               RESTORING → ROLLED_BACK (needs an application with 2+ replicas;
//               with one replica it behaves like *:fail)
//   *:local     the pull fails but a local copy exists (a `warn` step)
//   *:hookfail  the pre-deploy command exits 1: FAILED with its output as a
//               `log` event, no replica touched (needs an app with pre_deploy)
//
// Environment:
//   MOCK_PORT (9100), MOCK_HOST (127.0.0.1), MOCK_TOKEN
//   MOCK_ROLE=read|deploy|admin  the role of MOCK_TOKEN (default admin, as the
//                    root token); endpoints above it answer 403 FORBIDDEN
//   MOCK_WEBHOOK=1   server.notifications.webhook is true
//   MOCK_NO_PROXY=1  server.proxy.enabled is false, and deploying an application
//                    with a domain produces the "No reverse proxy" warn step

import { createServer } from 'node:http'
import { randomBytes } from 'node:crypto'

const PORT = Number(process.env.MOCK_PORT || 9100)
const HOST = process.env.MOCK_HOST || '127.0.0.1'
const TOKEN = process.env.MOCK_TOKEN || 'mock-token-0123456789abcdef'
const PROXY_ENABLED = process.env.MOCK_NO_PROXY !== '1'
const WEBHOOK = process.env.MOCK_WEBHOOK === '1'
const ROLES = ['read', 'deploy', 'admin']
const ROLE = ROLES.includes(process.env.MOCK_ROLE) ? process.env.MOCK_ROLE : 'admin'
// The configured token is the root token when it is admin; a lesser role gets a plausible name.
const TOKEN_IDENTITY = { name: ROLE === 'admin' ? 'root' : ROLE === 'deploy' ? 'ci' : 'viewer', role: ROLE }
// Hostnames and server ports Shipwick itself holds: the dashboard's route, the proxy's and the agent's ports.
const OWN_HOSTNAMES = ['shipwick.example.com']
const RESERVED_PORTS = [80, 443, 8080, 8443, 9000]
const VERSION = '0.1.0-mock'
const LOG_BUFFER = 5000
const MASK = '********'

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

const startedAt = Date.now()
const iso = (ms) => new Date(ms).toISOString()
const ago = (ms) => iso(startedAt - ms)

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

let nextDeploymentId = 1
let nextEventId = 1
let nextTokenId = 1
let nextRunId = 1

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
// agent does for deployments recorded before tokens had names.
function addDeployment(app, sp, { status, startedAtMs, durationMs, error = '', events = [], kind = 'deploy', sourceId = null, by = 'root' }) {
  const id = nextDeploymentId++
  const sequence = [...deployments.values()].filter(d => d.application === app.name).length + 1
  const d = {
    id,
    application: app.name,
    sequence,
    version: tagOf(sp.image),
    image: sp.image,
    status,
    error,
    started_at: iso(startedAtMs),
    completed_at: durationMs === null ? null : iso(startedAtMs + durationMs),
    kind,
    source_deployment_id: sourceId,
    ...(by ? { by } : {}),
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
const routedMessage = sp => (PROXY_ENABLED
  ? ['step', `Routed https://${sp.domain} to ${plural(sp.replicas, 'replica')}`, 'info']
  : ['step', `No reverse proxy is configured, so ${sp.domain} is not being served. Set SHIPWICK_CADDY_ADMIN on the agent`, 'warn'])

/** Events of a finished successful deployment, for fixtures. Rolling when there was a previous version. */
function successEvents(sp, previousVersion) {
  const n = sp.replicas
  const version = tagOf(sp.image)
  const events = [
    ['state', 'BUILDING', 'info', 50],
    ['step', `Pulled image ${sp.image}`, 'info', 1900],
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

/** A deployment whose first replica never came up: FAILED, nothing of the old version was touched. */
function failureEvents(sp, reason, output) {
  return [
    ['state', 'BUILDING', 'info', 50],
    ['step', `Pulled image ${sp.image}`, 'info', 1700],
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
    ['step', `Pulled image ${sp.image}`, 'info', 1800],
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
      env: { POSTGRES_USER: MASK, POSTGRES_PASSWORD: MASK, POSTGRES_DB: MASK },
      health: { tcp: 5432, interval: '10s', timeout: '3s', retries: 3 },
      resources: { memory_bytes: 2 * 1024 ** 3 },
      volumes: [{ name: 'data', path: '/var/lib/postgresql/data' }],
      publish: [{ port: 5432, host: 15432, address: '10.0.0.5', protocol: 'tcp' }],
      deploy: { strategy: 'recreate' },
    })
    const dbActive = addDeployment(db, dbSpec, { status: 'ACTIVE', startedAtMs: startedAt - 9 * DAY, durationMs: 8300, events: successEvents(dbSpec, null) })
    db.active_deployment_id = dbActive.id
    db.updated_at = dbActive.completed_at
    db.containers = makeContainers(db, dbActive)

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

  // STOPPED on request.
  {
    const app = addApp('docs', ago(60 * DAY))
    const sp = spec('docs', 'nginx:1.27-alpine', { port: 80, domain: 'docs.example.com' })
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

  // DEPLOYING: first deployment in flight, waiting on a slow starter. It gives up after 15 minutes.
  {
    const app = addApp('billing', ago(90 * SECOND))
    const sp = spec('billing', 'ghcr.io/acme/billing:3.0.0', {
      port: 8080,
      domain: 'billing.example.com',
      replicas: 2,
      env: { STRIPE_KEY: MASK, DATABASE_URL: MASK },
      health: { path: '/actuator/health', interval: '30s', timeout: '5s', retries: 30 },
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

function appSummary(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const flying = inFlight(app.name)
  // Mid-rollout, replica slots are filled by containers of two deployments: count slots, not containers.
  const own = active ? app.containers.filter(c => flying || c.deployment_id === active.id) : []
  const desired = active ? active.spec.replicas : 0
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
    replicas: { desired, running, healthy },
    deploying: Boolean(flying),
    in_flight_deployment_id: flying ? flying.id : null,
    created_at: app.created_at,
    updated_at: app.updated_at,
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
    containers: app.containers,
  }
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
function startDeployment(app, sp, { kind, sourceId, by }) {
  const previous = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const d = addDeployment(app, sp, { status: 'PENDING', startedAtMs: Date.now(), durationMs: null, kind, sourceId, by })
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
        removeContainer(old.id, i)
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
    if (previous) app.containers = app.containers.filter(c => c.deployment_id !== previous.id)
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
  then(700, complete)
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

function addToken(name, role, createdAtMs, lastUsedAtMs, value = newTokenValue()) {
  const t = { id: nextTokenId++, name, role, created_at: iso(createdAtMs), last_used_at: lastUsedAtMs === null ? null : iso(lastUsedAtMs), value }
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

/** Who a bearer value is: the configured token, or one created here and not revoked since. */
function identify(authorization) {
  const value = typeof authorization === 'string' && authorization.startsWith('Bearer ') ? authorization.slice(7) : ''
  if (value === '') return null
  if (value === TOKEN) return TOKEN_IDENTITY
  for (const t of tokens.values()) {
    if (t.value === value) {
      // Kept to the minute, as the agent does: it says whether the token is still in use.
      t.last_used_at = iso(Math.floor(Date.now() / MINUTE) * MINUTE)
      return { name: t.name, role: t.role }
    }
  }
  return null
}

const roleCovers = (have, required) => ROLES.indexOf(have) >= ROLES.indexOf(required)

function forbiddenMessage(have, required) {
  const need = required === 'deploy' ? 'deploying needs deploy or admin' : `this needs ${required}`
  return `this token has the ${have} role; ${need}`
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
 * Turns a deploy.yaml sent as JSON into a stored spec: defaults filled in,
 * hostnames lowercased, the health block checked for exactly one kind. Far
 * from the agent's whole validation, but the same fields and wording where it
 * checks at all.
 */
function parseSpec(name, body) {
  if (body.name !== undefined && body.name !== name) {
    throw new HttpError(400, 'INVALID_REQUEST', `the configuration names ${JSON.stringify(body.name)} but the URL names ${JSON.stringify(name)}`)
  }
  const fields = []
  const problem = (field, message, expected) => fields.push({ field, message, ...(expected ? { expected } : {}) })
  if (typeof body.image !== 'string' || !IMAGE_PATTERN.test(body.image)) problem('image', 'is required', 'ghcr.io/org/app:1.0.0')

  const hosts = (field, list) => {
    if (list === undefined) return undefined
    if (!Array.isArray(list)) problem(field, 'must be a list of hostnames')
    else if (list.length > 20) problem(field, `too many (${list.length})`, 'at most 20')
    else if (!body.domain) problem(field, `requires domain: ${field} are served next to it`)
    return Array.isArray(list) ? list.map(h => String(h ?? '').trim().toLowerCase()) : []
  }
  const domain = body.domain ? String(body.domain).trim().toLowerCase() : undefined
  if (domain !== undefined && !HOSTNAME.test(domain)) problem('domain', `invalid value "${domain}": not a valid hostname`)
  const aliases = hosts('aliases', body.aliases)
  const redirects = hosts('redirects', body.redirects)
  const seen = new Map(domain ? [[domain, 'domain']] : [])
  for (const [field, list] of [['aliases', aliases], ['redirects', redirects]]) {
    for (const [i, host] of (list ?? []).entries()) {
      if (host === '') problem(`${field}[${i}]`, 'is empty')
      else if (!HOSTNAME.test(host)) problem(`${field}[${i}]`, `invalid value "${host}": not a valid hostname`)
      else if (seen.has(host)) problem(`${field}[${i}]`, `"${host}" is already listed under ${seen.get(host)}`)
      else seen.set(host, `${field}[${i}]`)
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
    health = { ...(h.path ? { path: h.path } : {}), ...(h.tcp ? { tcp: h.tcp } : {}), ...(h.command ? { command: h.command } : {}), interval: h.interval ?? '10s', timeout: h.timeout ?? '3s', retries: h.retries ?? 3 }
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

  if (fields.length > 0) throw configError(fields)
  return spec(name, body.image, {
    ...(body.port ? { port: body.port } : {}),
    ...(domain ? { domain } : {}),
    ...(aliases?.length ? { aliases } : {}),
    ...(redirects?.length ? { redirects } : {}),
    replicas: body.replicas ?? 1,
    ...(body.env ? { env: Object.fromEntries(Object.keys(body.env).map(k => [k, MASK])) } : {}),
    ...(health ? { health } : {}),
    resources: body.resources ?? {},
    ...(body.volumes?.length ? { volumes: body.volumes } : {}),
    ...(publish?.length ? { publish } : {}),
    ...(body.entrypoint ? { entrypoint: [].concat(body.entrypoint) } : {}),
    ...(body.command ? { command: [].concat(body.command) } : {}),
    ...(body.user ? { user: body.user } : {}),
    ...(body.logging ? { logging: body.logging } : {}),
    restart: body.restart ?? { policy: 'always' },
    deploy: body.deploy ?? { strategy: 'rolling' },
  })
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
  for (const [field, host] of hostLines) {
    if (OWN_HOSTNAMES.includes(host)) fields.push({ field, message: 'already served by Shipwick itself (the agent or the dashboard)' })
    else {
      const owner = others.find(([, other]) => hostnamesOf(other).includes(host))
      if (owner) fields.push({ field, message: `already served by application "${owner[0]}"` })
    }
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
  res.writeHead(status, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body), ...headers })
  res.end(body)
}

function sendError(res, status, code, message, details = {}) {
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

async function readJSON(req, allowed) {
  const chunks = []
  let size = 0
  for await (const chunk of req) {
    size += chunk.length
    if (size > 64 * 1024) throw new HttpError(413, 'INVALID_REQUEST', 'request body larger than 64 KB')
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
    throw new HttpError(400, 'INVALID_REQUEST', allowed ? 'request body must be a JSON object' : 'the mock agent reads deploy.yaml as JSON only; send the document as a JSON object')
  }
  // Like the agent: a typo such as "imgae" must not quietly redeploy the old image.
  for (const key of Object.keys(value)) {
    if (allowed && !allowed.includes(key)) throw new HttpError(400, 'INVALID_REQUEST', `json: unknown field ${JSON.stringify(key)}`)
  }
  return value
}

/**
 * Reads an archive upload: the content type must say tar, the first block
 * must be a tar header, and the rest is counted, not kept. Answers the size.
 */
async function readArchive(req) {
  const [type] = String(req.headers['content-type'] ?? '').split(';')
  if (type.trim() !== 'application/x-tar') {
    throw new HttpError(400, 'INVALID_REQUEST', 'the body must be a tar archive sent as Content-Type: application/x-tar')
  }
  if (Number(req.headers['content-length'] ?? 0) > 10 * 1024 ** 3) throw new HttpError(413, 'INVALID_REQUEST', 'the archive exceeds 10 GB')
  let size = 0
  let head = Buffer.alloc(0)
  for await (const chunk of req) {
    size += chunk.length
    if (head.length < 512) head = Buffer.concat([head, chunk]).subarray(0, 512)
    if (size > 10 * 1024 ** 3) throw new HttpError(413, 'INVALID_REQUEST', 'the archive exceeds 10 GB')
  }
  if (head.length < 512 || head.toString('ascii', 257, 262) !== 'ustar') {
    throw new HttpError(400, 'INVALID_REQUEST', 'the archive is not a tar file')
  }
  return size
}

function requireApp(name) {
  const app = apps.get(name)
  if (!app) throw new HttpError(404, 'NOT_FOUND', 'not found')
  return app
}

function requireIdle(app) {
  if (inFlight(app.name)) {
    throw new HttpError(409, 'DEPLOYMENT_IN_PROGRESS', `another operation is in progress for ${app.name}`)
  }
}

function requireActive(app) {
  const active = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  if (!active) throw new HttpError(409, 'NOT_DEPLOYED', `${app.name} has no active deployment`)
  return active
}

const IMAGE_PATTERN = /^[a-z0-9]+([._\-/:][a-z0-9]+)*(:[\w][\w.-]{0,127})?(@sha256:[a-f0-9]{64})?$/i

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
}

async function handle(req, res) {
  const url = new URL(req.url, `http://${req.headers.host ?? 'localhost'}`)
  const method = req.method ?? 'GET'
  const path = url.pathname

  if (method === 'GET' && path === '/api/v1/health') {
    return sendJSON(res, 200, { status: 'ok', version: VERSION })
  }

  const segments = path.startsWith('/api/v1/') ? path.split('/').filter(Boolean).slice(2).map(decodeURIComponent) : []
  const collection = ['applications', 'deployments', 'tokens'].includes(segments[0])
  const route = `${method} /${segments.map((s, i) => (collection && i === 1 ? ':p' : collection && i === 3 && ['volumes', 'runs', 'jobs'].includes(segments[2]) ? ':x' : s)).join('/')}`
  const param = segments[1]
  const required = ROUTES[route]

  if (!required) {
    // The agent has no such operation, as opposed to NOT_FOUND: unknown application or deployment.
    return sendError(res, 404, 'ENDPOINT_NOT_FOUND', `no such endpoint: ${method} ${path}`)
  }
  const who = identify(req.headers.authorization)
  if (!who) {
    res.setHeader('www-authenticate', 'Bearer realm="shipwick"')
    return sendError(res, 401, 'UNAUTHORIZED', 'missing or invalid API token')
  }
  if (!roleCovers(who.role, required)) {
    return sendError(res, 403, 'FORBIDDEN', forbiddenMessage(who.role, required), { role: who.role, required })
  }
  // Stop/start events name the actor unless it is root, as the agent does.
  const byWho = who.name === 'root' ? '' : ` by ${who.name}`

  switch (route) {
    case 'GET /server': {
      const routes = [...apps.values()].filter(a => appSummary(a).domain && a.desired_state === 'running' && a.active_deployment_id).length
      return sendJSON(res, 200, {
        agent_version: VERSION,
        hostname: 'shipwick-fsn1-01',
        os: 'Ubuntu 24.04.1 LTS',
        kernel: '6.8.0-51-generic',
        architecture: 'x86_64',
        docker_version: '27.4.1',
        cpus: 4,
        memory_bytes: 8 * 1024 ** 3 - 212 * 1024 ** 2,
        applications: apps.size,
        containers: [...apps.values()].reduce((n, a) => n + a.containers.filter(c => c.state === 'running').length, 0),
        proxy: { enabled: PROXY_ENABLED, reachable: PROXY_ENABLED, error: '', routes: PROXY_ENABLED ? routes : 0 },
        token: who,
        notifications: { webhook: WEBHOOK },
      })
    }

    case 'GET /applications':
      return sendJSON(res, 200, [...apps.values()].map(appSummary).sort((a, b) => a.name.localeCompare(b.name)))

    case 'GET /applications/:p':
      return sendJSON(res, 200, appDetail(requireApp(param)))

    case 'DELETE /applications/:p': {
      const app = requireApp(param)
      requireIdle(app)
      endFollowers(app.name)
      apps.delete(app.name)
      appEvents.delete(app.name)
      logBuffers.delete(app.name)
      for (const d of [...deployments.values()]) if (d.application === app.name) deployments.delete(d.id)
      for (const r of [...runs.values()]) if (r.application === app.name) runs.delete(r.id)
      res.writeHead(204)
      return res.end()
    }

    case 'POST /applications/:p/deploy': {
      // The dashboard never deploys a raw spec; the CLI would. JSON only: see the header.
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(param)) throw new HttpError(400, 'INVALID_REQUEST', 'name: lowercase letters, digits and dashes only')
      const body = await readJSON(req, null)
      const sp = parseSpec(param, body)
      const app = apps.get(param) ?? addApp(param, iso(Date.now()))
      requireIdle(app)
      checkConflicts(app, sp)
      const d = startDeployment(app, sp, { kind: 'deploy', sourceId: null, by: who.name })
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'POST /applications/:p/redeploy': {
      const app = requireApp(param)
      const body = await readJSON(req, ['image'])
      requireIdle(app)
      const active = requireActive(app)
      const sp = structuredClone(active.spec)
      if (body.image !== undefined && body.image !== '') {
        if (typeof body.image !== 'string' || !IMAGE_PATTERN.test(body.image)) {
          throw new HttpError(400, 'INVALID_REQUEST', `image: invalid reference ${JSON.stringify(body.image)}`)
        }
        sp.image = body.image
      }
      // A stored configuration's hostnames and ports may have been taken since.
      checkConflicts(app, sp)
      const d = startDeployment(app, sp, { kind: 'redeploy', sourceId: active.id, by: who.name })
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
      const d = startDeployment(app, structuredClone(target.spec), { kind: 'rollback', sourceId: target.id, by: who.name })
      return sendJSON(res, 202, deploymentView(d), { location: `/api/v1/deployments/${d.id}` })
    }

    case 'POST /applications/:p/stop': {
      const app = requireApp(param)
      requireIdle(app)
      requireActive(app)
      if (app.desired_state !== 'stopped') {
        app.desired_state = 'stopped'
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
      const since = url.searchParams.get('since') || '1h'
      if (!HISTORY_WINDOWS[since]) throw new HttpError(400, 'INVALID_REQUEST', 'since must be 1h, 24h or 7d')
      return sendJSON(res, 200, metricsHistory(app, since))
    }

    case 'GET /applications/:p/volumes': {
      const app = requireApp(param)
      const active = requireActive(app)
      return sendJSON(res, 200, (active.spec.volumes ?? []).map(v => ({ name: v.name, path: v.path })))
    }

    case 'GET /applications/:p/jobs':
      return sendJSON(res, 200, jobsView(requireApp(param)))

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
      const size = await readArchive(req)
      // The replica is created again around the fresh volume, and stays stopped.
      const active = requireActive(app)
      app.containers = makeContainers(app, active, { 1: { state: 'created', started_at: null, health: active.spec.health ? 'unknown' : '' } })
      app.updated_at = iso(Date.now())
      addAppEvent(app.name, 'info', 'app', `Volume ${volume.name} restored from a backup (${formatSize(size)})`)
      res.writeHead(204)
      return res.end()
    }

    case 'GET /tokens':
      return sendJSON(res, 200, [...tokens.values()].sort((a, b) => a.id - b.id).map(tokenView))

    case 'POST /tokens': {
      const body = await readJSON(req, ['name', 'role'])
      if (body.name === undefined || body.name === '') throw new HttpError(400, 'INVALID_REQUEST', 'name is required, e.g. {"name": "ci", "role": "deploy"}')
      if (body.role === undefined || body.role === '') throw new HttpError(400, 'INVALID_REQUEST', 'role is required: read, deploy or admin')
      if (typeof body.name !== 'string' || !TOKEN_NAME.test(body.name)) {
        throw new HttpError(400, 'INVALID_REQUEST', `invalid token name ${JSON.stringify(body.name)}: use lowercase letters, digits and dashes (max 40 characters), e.g. ci`)
      }
      if (body.name === 'root') throw new HttpError(400, 'INVALID_REQUEST', '"root" is the name of the token the agent is configured with; choose another')
      if (!ROLES.includes(body.role)) throw new HttpError(400, 'INVALID_REQUEST', `invalid role ${JSON.stringify(body.role)}: use read, deploy or admin`)
      if (tokens.has(body.name)) throw new HttpError(409, 'TOKEN_EXISTS', `a token named ${JSON.stringify(body.name)} exists`)
      const t = addToken(body.name, body.role, Date.now(), null)
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
      requireActive(app)
      return sendJSON(res, 200, metrics(app))
    }

    case 'GET /applications/:p/logs': {
      const app = requireApp(param)
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

server.listen(PORT, HOST, () => {
  console.log(`Shipwick mock agent on http://${HOST}:${PORT}/api/v1`)
  console.log(`token: ${TOKEN} (${TOKEN_IDENTITY.name}, ${TOKEN_IDENTITY.role})`)
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    for (const name of [...followers.keys()]) endFollowers(name)
    server.close(() => process.exit(0))
    setTimeout(() => process.exit(0), 500).unref()
  })
}
