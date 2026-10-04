// What the mock agent answers when what is underneath it fails or falls
// short, in the agent's words (agent/internal/deploy/outage.go, limits.go):
// a Docker daemon that does not answer, a disk without room, a daemon that
// takes a limit and does not apply it. Apart from the mock agent so that the
// answers are unit-tested.

/** The limits a daemon of the given kind accepts and does not apply: `MOCK_DOCKER`'s values. */
export function unenforcedLimits(docker) {
  if (docker === 'nocgroups') return ['memory', 'cpu']
  if (docker === 'nomemory') return ['memory']
  return []
}

// The requests the agent cannot answer without asking Docker: the server's
// facts, the applications and their containers, what the containers write
// and use, and everything that starts, stops or removes one. What it reads
// from its own database is answered while Docker is silent: deployments,
// events, the history of CPU and memory, runs, backups, tokens, the trail.
const NEEDS_DOCKER = new Set([
  'GET /server',
  'GET /applications',
  'GET /applications/:p',
  'DELETE /applications/:p',
  'POST /applications/:p/stop',
  'POST /applications/:p/start',
  'GET /applications/:p/logs',
  'GET /applications/:p/metrics',
  'POST /applications/:p/images/missing',
  'POST /applications/:p/jobs/:x/run',
  'POST /applications/:p/run',
])

export function needsDocker(route) {
  return NEEDS_DOCKER.has(route)
}

/**
 * The answer to a request that needs Docker while Docker does not answer.
 * From 0.8 the agent gives the daemon fifteen seconds and then says so; an
 * agent before that answers 500 for a daemon that is down.
 */
export function dockerSilent(before08 = false) {
  if (before08) return { status: 500, code: 'INTERNAL_ERROR', message: 'list containers: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?' }
  return {
    status: 503,
    code: 'RUNTIME_UNAVAILABLE',
    message: 'Docker does not answer on the server. Applications that are running keep running; look at the daemon there with: systemctl status docker. The cause: no answer within 15s',
  }
}

// What a request writes when the disk has no room for it.
const WRITES = {
  'POST /applications/:p/deploy': 'create deployment: database or disk is full (13)',
  'POST /applications': 'create deployment: database or disk is full (13)',
  'POST /applications/:p/redeploy': 'create deployment: database or disk is full (13)',
  'POST /applications/:p/rollback': 'create deployment: database or disk is full (13)',
  'PUT /applications/:p/static': 'store the folder: write /var/lib/shipwick/uploads/upload.tmp: no space left on device',
  'POST /applications/:p/images': 'load image: write /var/lib/docker/tmp/docker-import: no space left on device',
}

export function writes(route) {
  return Object.hasOwn(WRITES, route)
}

/** The answer to a request the disk has no room for. An agent before 0.8 has no code for it: 500, or 400 for a folder, which it took for a broken archive. */
export function diskFull(route, before08 = false) {
  const cause = WRITES[route] ?? WRITES['POST /applications/:p/deploy']
  if (before08) {
    if (route === 'PUT /applications/:p/static') return { status: 400, code: 'INVALID_REQUEST', message: 'the body is not a tar archive: unexpected EOF' }
    return { status: 500, code: 'INTERNAL_ERROR', message: cause }
  }
  return {
    status: 507,
    code: 'DISK_FULL',
    message: `The server's disk is full (${cause}). Nothing was changed. Free space on the server — docker system df shows what takes it, docker image prune -a removes images nothing uses — and try again`,
  }
}

/** The `warn` step of a deployment that asks for a limit the daemon does not apply; "" when it asks for none of those. */
export function unenforcedStep(resources, unenforced) {
  const keys = []
  if (resources?.memory_bytes > 0 && unenforced.includes('memory')) keys.push('resources.memory')
  if (resources?.cpu > 0 && unenforced.includes('cpu')) keys.push('resources.cpu')
  if (keys.length === 0) return ''
  return `Docker on this server does not enforce ${keys.join(' and ')}: the replicas run without a limit. shipwick doctor says what the server lacks`
}
