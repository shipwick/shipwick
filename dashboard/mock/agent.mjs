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
// taking over from its predecessor.
//
// Magic image tags for POST /applications/:name/redeploy {"image": ...}:
//   *:fail      replica 1 crashes: FAILED, nothing of the old version was touched
//   *:rollback  replica 1 is replaced, replica 2 crashes: FAILED → ROLLBACK →
//               RESTORING → ROLLED_BACK (needs an application with 2+ replicas;
//               with one replica it behaves like *:fail)
//   *:local     the pull fails but a local copy exists (a `warn` step)
//
// Environment:
//   MOCK_PORT (9100), MOCK_HOST (127.0.0.1), MOCK_TOKEN
//   MOCK_NO_PROXY=1  server.proxy.enabled is false, and deploying an application
//                    with a domain produces the "No reverse proxy" warn step

import { createServer } from 'node:http'

const PORT = Number(process.env.MOCK_PORT || 9100)
const HOST = process.env.MOCK_HOST || '127.0.0.1'
const TOKEN = process.env.MOCK_TOKEN || 'mock-token-0123456789abcdef'
const PROXY_ENABLED = process.env.MOCK_NO_PROXY !== '1'
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

let nextDeploymentId = 1
let nextEventId = 1

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

function addDeployment(app, sp, { status, startedAtMs, durationMs, error = '', events = [], kind = 'deploy', sourceId = null }) {
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
      ...extra,
    })
    // tag, age, previous version, kind, index (in this list) of the deployment whose configuration was re-used
    const history = [
      ['1.3.8', 41 * DAY, null, 'deploy', null],
      ['1.3.9', 27 * DAY, '1.3.8', 'deploy', null],
      ['1.4.0', 12 * DAY, '1.3.9', 'deploy', null],
      ['1.4.1', 8 * DAY, '1.4.0', 'deploy', null],
      // 1.4.1 misbehaved: rolled back to #3, then redeployed once the image had been rebuilt under the same tag.
      ['1.4.0', 8 * DAY - 3 * HOUR, '1.4.1', 'rollback', 2],
      ['1.4.1', 5 * DAY, '1.4.0', 'redeploy', 4],
    ]
    const made = []
    for (const [tag, age, prev, kind, sourceIndex] of history) {
      const sp = base(tag)
      made.push(addDeployment(app, sp, {
        status: 'SUPERSEDED',
        startedAtMs: startedAt - age,
        durationMs: prev ? 7300 : 4600,
        events: successEvents(sp, prev),
        kind,
        sourceId: sourceIndex === null ? null : made[sourceIndex].id,
      }))
    }
    const bad = base('1.4.2-rc1')
    addDeployment(app, bad, {
      status: 'FAILED',
      startedAtMs: startedAt - 26 * HOUR,
      durationMs: 5200,
      error: 'replica 1 exited with code 1 shortly after start',
      events: failureEvents(bad, 'replica 1 exited with code 1 shortly after start',
        'time=2026-03-01T10:00:01Z level=info msg="starting my-api" version=1.4.2-rc1\ntime=2026-03-01T10:00:01Z level=info msg="running migrations"\npanic: DATABASE_URL is not set\n\ngoroutine 1 [running]:\nmain.mustEnv(...)\n\t/src/cmd/api/main.go:41 +0x9c\nmain.main()\n\t/src/cmd/api/main.go:18 +0x2f\nexit status 2'),
    })
    const sp = base('1.4.2')
    const active = addDeployment(app, sp, { status: 'ACTIVE', startedAtMs: startedAt - 2 * HOUR, durationMs: 6100, events: successEvents(sp, '1.4.1') })
    app.active_deployment_id = active.id
    app.updated_at = active.completed_at
    app.containers = makeContainers(app, active)
    addAppEvent('my-api', 'warn', 'supervisor', 'Replica 2 exited with code 137 (out of memory); restarting in 1s', startedAt - 3 * DAY)
    addAppEvent('my-api', 'info', 'supervisor', 'Replica 2 restarted', startedAt - 3 * DAY + 1200)
    addAppEvent('my-api', 'info', 'supervisor', 'Replica 2 is healthy again', startedAt - 3 * DAY + 4100)
  }

  // DEGRADED: three replicas desired, one of them is down and being restarted.
  {
    const app = addApp('web', ago(30 * DAY))
    const base = tag => spec('web', `ghcr.io/acme/web:${tag}`, {
      port: 3000,
      domain: 'www.example.com',
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
  {
    const app = addApp('worker', ago(19 * DAY))
    const base = tag => spec('worker', `registry.example.com:5000/acme/worker:${tag}`, {
      port: 9090,
      replicas: 2,
      env: { QUEUE_URL: MASK, DATABASE_URL: MASK, CONCURRENCY: MASK },
      health: { path: '/ready', interval: '10s', timeout: '3s', retries: 3 },
      resources: { cpu: 2 },
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
    addAppEvent('docs', 'info', 'app', 'Application stopped on request', startedAt - 6 * DAY)
  }

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
function startDeployment(app, sp, { kind, sourceId }) {
  const previous = app.active_deployment_id ? deployments.get(app.active_deployment_id) : null
  const d = addDeployment(app, sp, { status: 'PENDING', startedAtMs: Date.now(), durationMs: null, kind, sourceId })
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
    state('STARTING')
  })

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
// Background activity
// ---------------------------------------------------------------------------

function startBackground() {
  // A log line per running application roughly every 700ms.
  setInterval(() => {
    for (const app of apps.values()) generateLogLine(app)
  }, 700).unref()

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
  constructor(status, code, message) {
    super(message)
    this.status = status
    this.code = code
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
    throw new HttpError(400, 'INVALID_REQUEST', 'request body must be a JSON object')
  }
  // Like the agent: a typo such as "imgae" must not quietly redeploy the old image.
  for (const key of Object.keys(value)) {
    if (!allowed.includes(key)) throw new HttpError(400, 'INVALID_REQUEST', `json: unknown field ${JSON.stringify(key)}`)
  }
  return value
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

async function handle(req, res) {
  const url = new URL(req.url, `http://${req.headers.host ?? 'localhost'}`)
  const method = req.method ?? 'GET'
  const path = url.pathname

  if (method === 'GET' && path === '/api/v1/health') {
    return sendJSON(res, 200, { status: 'ok', version: VERSION })
  }

  if (path.startsWith('/api/v1/') && req.headers.authorization !== `Bearer ${TOKEN}`) {
    return sendError(res, 401, 'UNAUTHORIZED', 'missing or invalid API token')
  }

  const segments = path.split('/').filter(Boolean).slice(2).map(decodeURIComponent)
  const route = `${method} /${segments.map((s, i) => (i === 1 && (segments[0] === 'applications' || segments[0] === 'deployments') ? ':p' : s)).join('/')}`
  const param = segments[1]

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
      res.writeHead(204)
      return res.end()
    }

    case 'POST /applications/:p/deploy':
      // The dashboard never deploys a raw spec (it only ever sees masked env values).
      throw new HttpError(400, 'INVALID_REQUEST', 'the mock agent does not accept deploy.yaml; use /redeploy or /rollback')

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
      const d = startDeployment(app, sp, { kind: 'redeploy', sourceId: active.id })
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
      const d = startDeployment(app, structuredClone(target.spec), { kind: 'rollback', sourceId: target.id })
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
        addAppEvent(app.name, 'info', 'app', 'Application stopped on request')
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
        addAppEvent(app.name, 'info', 'app', 'Application started on request')
      }
      return sendJSON(res, 200, appDetail(app))
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
      // The agent has no such operation, as opposed to NOT_FOUND: unknown application or deployment.
      return sendError(res, 404, 'ENDPOINT_NOT_FOUND', `no such endpoint: ${method} ${path}`)
  }
}

seed()
startBackground()

const server = createServer((req, res) => {
  handle(req, res).catch((error) => {
    if (res.headersSent) return res.destroy()
    if (error instanceof HttpError) return sendError(res, error.status, error.code, error.message)
    console.error(error)
    sendError(res, 500, 'INTERNAL_ERROR', String(error?.message ?? error))
  })
})

server.listen(PORT, HOST, () => {
  console.log(`Shipwick mock agent on http://${HOST}:${PORT}/api/v1`)
  console.log(`token: ${TOKEN}`)
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    for (const name of [...followers.keys()]) endFollowers(name)
    server.close(() => process.exit(0))
    setTimeout(() => process.exit(0), 500).unref()
  })
}
