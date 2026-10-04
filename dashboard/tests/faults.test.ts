import { describe, expect, it } from 'vitest'
import type { Alert } from '../app/types/api'
import { APPLICATION_CALLER_MESSAGE, AgentError, errorFromResponse, failureTitle } from '../app/utils/agentError'
import { alertKindLabel, alertSubject, alertTone, sortAlerts, summarizeAlerts } from '../app/utils/alerts'
import { verdict } from '../app/utils/overview'
import { parseRefusal, tokenVerdict } from '../server/utils/tokenCheck'

const { diskFull, dockerSilent, needsDocker, unenforcedLimits, unenforcedStep, writes } = await import('../mock/faults.mjs' as string) as {
  diskFull: (route: string, before08?: boolean) => { status: number, code: string, message: string }
  dockerSilent: (before08?: boolean) => { status: number, code: string, message: string }
  needsDocker: (route: string) => boolean
  unenforcedLimits: (docker: string) => string[]
  unenforcedStep: (resources: { cpu?: number, memory_bytes?: number }, unenforced: string[]) => string
  writes: (route: string) => boolean
}

function response(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
const envelope = (answer: { code: string, message: string }) => ({ error: { code: answer.code, message: answer.message, details: {} } })

const SILENT = 'Docker does not answer on the server. Applications that are running keep running; look at the daemon there with: systemctl status docker. The cause: no answer within 15s'

describe('a Docker daemon that does not answer', () => {
  it('is recognized by the agent\'s sentence, and the sentence is shown as it is', async () => {
    const error = await errorFromResponse(response(503, envelope(dockerSilent())))
    expect(error.code).toBe('RUNTIME_UNAVAILABLE')
    expect(error.dockerSilent).toBe(true)
    expect(error.displayMessage).toBe(SILENT)
  })

  it('is not an agent that is shutting down, which answers with the same code', () => {
    const error = new AgentError(503, 'RUNTIME_UNAVAILABLE', 'agent is shutting down')
    expect(error.dockerSilent).toBe(false)
    expect(failureTitle(error, 'applications')).toBe('Could not load applications')
  })

  it('is headed the same on every page, whatever the page was loading', () => {
    const error = new AgentError(503, 'RUNTIME_UNAVAILABLE', SILENT)
    expect(failureTitle(error, 'applications')).toBe('Docker does not answer')
    expect(failureTitle(error, 'the server')).toBe('Docker does not answer')
  })

  it('leaves the headings of other failures as they were', () => {
    expect(failureTitle(new AgentError(502, 'AGENT_UNREACHABLE', 'x'), 'applications')).toBe('Agent unreachable')
    expect(failureTitle(new AgentError(0, 'NETWORK', 'x'), 'applications')).toBe('Dashboard server unreachable')
    expect(failureTitle(new AgentError(404, 'NOT_FOUND', 'not found'), 'the deployment')).toBe('Not found')
    expect(failureTitle(new AgentError(500, 'INTERNAL_ERROR', 'x'), 'the history')).toBe('Could not load the history')
  })
})

describe('a full disk', () => {
  it('is shown with the agent\'s message, which says what to do', async () => {
    const error = await errorFromResponse(response(507, envelope(diskFull('POST /applications'))))
    expect(error.status).toBe(507)
    expect(error.code).toBe('DISK_FULL')
    expect(error.displayMessage).toBe('The server\'s disk is full (create deployment: database or disk is full (13)). Nothing was changed. Free space on the server — docker system df shows what takes it, docker image prune -a removes images nothing uses — and try again')
    expect(failureTitle(error, 'applications')).toBe('The server\'s disk is full')
  })
})

describe('a dashboard the agent refuses for where it calls from', () => {
  const refused = new AgentError(403, 'APPLICATION_CALLER', 'the API answers the proxy, the dashboard and the server itself, not the containers of applications; from a container, call it at its hostname')

  it('is said in a sentence of its own, with what to do on the server', () => {
    expect(refused.applicationCaller).toBe(true)
    expect(refused.displayMessage).toBe(APPLICATION_CALLER_MESSAGE)
    expect(APPLICATION_CALLER_MESSAGE).toBe('The agent refuses this dashboard: it calls from a network applications are on. On the server, run the compose command again (handbook, Installation from a package).')
  })

  it('is never presented as a wrong token, a missing role or a rate limit', () => {
    expect(refused.status).not.toBe(401)
    expect(refused.rateLimited).toBe(false)
    expect(refused.displayMessage).not.toMatch(/token|role/i)
    expect(failureTitle(refused, 'applications')).toBe('Refused by the agent')
  })
})

describe('what the agent\'s answer says about a token at sign-in', () => {
  const url = 'http://agent:9000'

  it('accepts it on 200', () => {
    expect(tokenVerdict(200, null, url)).toEqual({ accepted: true })
  })

  it('rejects it on 401, and passes on that it expired', () => {
    expect(tokenVerdict(401, { code: 'UNAUTHORIZED', message: 'missing or invalid API token', details: {} }, url)).toEqual({ accepted: false, status: 401, code: 'UNAUTHORIZED', message: 'The agent rejected this token', details: {} })
    expect(tokenVerdict(401, { code: 'TOKEN_EXPIRED', message: 'the token ci expired', details: { name: 'ci' } }, url)).toEqual({ accepted: false, status: 401, code: 'TOKEN_EXPIRED', message: 'the token ci expired', details: { name: 'ci' } })
  })

  it('does not call it wrong when the agent refused the dashboard itself', () => {
    const verdict = tokenVerdict(403, { code: 'APPLICATION_CALLER', message: 'the API answers the proxy', details: {} }, url)
    expect(verdict).toEqual({ accepted: false, status: 403, code: 'APPLICATION_CALLER', message: 'the API answers the proxy', details: {} })
  })

  it('accepts it while Docker does not answer: the agent checked the token before it asked Docker', () => {
    expect(tokenVerdict(503, { code: 'RUNTIME_UNAVAILABLE', message: SILENT, details: {} }, url)).toEqual({ accepted: true })
  })

  it('does not take any other 503, or a 403 of another kind, for an agent', () => {
    expect(tokenVerdict(503, null, url)).toMatchObject({ accepted: false, status: 502, code: 'AGENT_UNREACHABLE', message: 'http://agent:9000 answered with status 503. Is it a Shipwick agent?' })
    expect(tokenVerdict(403, { code: 'FORBIDDEN', message: 'x', details: {} }, url)).toMatchObject({ accepted: false, code: 'AGENT_UNREACHABLE' })
    expect(tokenVerdict(429, null, url)).toMatchObject({ accepted: false, status: 429, code: 'RATE_LIMITED' })
  })

  it('reads an envelope and nothing else', () => {
    expect(parseRefusal(JSON.stringify(envelope(dockerSilent())))).toEqual({ code: 'RUNTIME_UNAVAILABLE', message: SILENT, details: {} })
    expect(parseRefusal('<html>')).toBeNull()
    expect(parseRefusal('{"error": "no"}')).toBeNull()
    expect(parseRefusal('null')).toBeNull()
  })
})

describe('the docker alert', () => {
  const docker: Alert = { kind: 'docker', severity: 'critical', application: '', replica: 0, message: 'Docker does not answer. Applications that are running keep running, but nothing is restarted, deployed or routed until it does. On the server: systemctl status docker', since: '2026-10-04T08:26:12Z' }
  const memory: Alert = { kind: 'memory', severity: 'warning', application: 'web', replica: 3, message: 'x', since: '2026-10-04T08:00:00Z' }

  it('is about the server, critical, and labelled', () => {
    expect(alertKindLabel(docker)).toBe('Docker')
    expect(alertSubject(docker)).toBe('the server')
    expect(alertTone(docker)).toBe('danger')
  })

  it('comes first and counts as the server needing attention', () => {
    expect(sortAlerts([memory, docker])[0]).toBe(docker)
    expect(summarizeAlerts([memory, docker])).toBe('2 alerts, 1 critical')
    expect(verdict([{ status: 'HEALTHY' }], [docker])).toMatchObject({ tone: 'danger', title: 'The server needs attention' })
  })
})

describe('the mock agent', () => {
  it('answers 503 RUNTIME_UNAVAILABLE where the agent asks Docker, and goes on answering what it reads from its database', () => {
    for (const route of ['GET /server', 'GET /applications', 'GET /applications/:p', 'GET /applications/:p/logs', 'GET /applications/:p/metrics', 'POST /applications/:p/stop']) expect(needsDocker(route)).toBe(true)
    for (const route of ['GET /deployments', 'GET /deployments/:p', 'GET /applications/:p/events', 'GET /applications/:p/metrics/history', 'GET /tokens', 'GET /audit']) expect(needsDocker(route)).toBe(false)
    expect(dockerSilent()).toEqual({ status: 503, code: 'RUNTIME_UNAVAILABLE', message: SILENT })
  })

  it('answers a silent Docker as an agent before 0.8 does: 500, without the sentence', () => {
    const before = dockerSilent(true)
    expect(before.status).toBe(500)
    expect(before.code).toBe('INTERNAL_ERROR')
    expect(new AgentError(before.status, before.code, before.message).dockerSilent).toBe(false)
  })

  it('answers 507 DISK_FULL to what writes, and names what could not be written', () => {
    for (const route of ['POST /applications/:p/deploy', 'POST /applications', 'POST /applications/:p/redeploy', 'POST /applications/:p/rollback', 'PUT /applications/:p/static', 'POST /applications/:p/images']) expect(writes(route)).toBe(true)
    for (const route of ['POST /validate', 'POST /applications/:p/validate', 'GET /applications', 'POST /applications/:p/stop']) expect(writes(route)).toBe(false)
    expect(diskFull('PUT /applications/:p/static')).toMatchObject({ status: 507, code: 'DISK_FULL' })
    expect(diskFull('PUT /applications/:p/static').message).toContain('no space left on device')
    expect(diskFull('POST /applications/:p/deploy').message).toContain('Nothing was changed.')
  })

  it('answers a full disk as an agent before 0.8 does: 500, and 400 for a folder', () => {
    expect(diskFull('POST /applications/:p/deploy', true)).toEqual({ status: 500, code: 'INTERNAL_ERROR', message: 'create deployment: database or disk is full (13)' })
    expect(diskFull('PUT /applications/:p/static', true)).toMatchObject({ status: 400, code: 'INVALID_REQUEST' })
  })

  it('knows which limits each kind of daemon does not apply', () => {
    expect(unenforcedLimits('nocgroups')).toEqual(['memory', 'cpu'])
    expect(unenforcedLimits('nomemory')).toEqual(['memory'])
    expect(unenforcedLimits('rootless')).toEqual([])
    expect(unenforcedLimits('')).toEqual([])
  })

  it('records the agent\'s warning on a deployment that asks for a limit the daemon does not apply', () => {
    expect(unenforcedStep({ cpu: 1, memory_bytes: 512 }, ['memory', 'cpu'])).toBe('Docker on this server does not enforce resources.memory and resources.cpu: the replicas run without a limit. shipwick doctor says what the server lacks')
    expect(unenforcedStep({ cpu: 1 }, ['memory', 'cpu'])).toBe('Docker on this server does not enforce resources.cpu: the replicas run without a limit. shipwick doctor says what the server lacks')
    expect(unenforcedStep({ cpu: 1 }, ['memory'])).toBe('')
    expect(unenforcedStep({}, ['memory', 'cpu'])).toBe('')
    expect(unenforcedStep({ cpu: 1, memory_bytes: 512 }, [])).toBe('')
  })
})
