import type { IncomingMessage } from 'node:http'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import type { Readable } from 'node:stream'
import type { H3Event } from 'h3'
import type { ConfiguredAgent } from './agents'
import { AgentsConfigError, parseAgents, sessionCookieFor } from './agents'
import { HEADERS_TIMEOUT_MS } from './timeouts'

/** Header every state-changing request must carry; see assertSameOriginRequest. */
export const CSRF_HEADER = 'x-shipwick-request'

/** Everything the dashboard may reach on the agent lives under this prefix. */
export const AGENT_API_PREFIX = '/api/v1/'

/** Seven days; the token is root-equivalent, so sessions do not live forever. */
export const SESSION_MAX_AGE_SECONDS = 7 * 24 * 60 * 60

/** A failure the proxy reports itself, in the agent's own error envelope. */
export class AgentProxyError extends Error {
  readonly status: number
  readonly code: string
  readonly details: Record<string, unknown>

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
    super(message)
    this.name = 'AgentProxyError'
    this.status = status
    this.code = code
    this.details = details
  }
}

/**
 * The agents this dashboard serves, read at runtime (not baked in at build
 * time), so one image serves every environment: SHIPWICK_AGENTS for several,
 * SHIPWICK_AGENT_URL for the one. Parsed once per value.
 */
let parsedFrom: string | null = null
let parsed: ConfiguredAgent[] = []

export function configuredAgents(): ConfiguredAgent[] {
  const agents = process.env.SHIPWICK_AGENTS ?? ''
  const single = process.env.SHIPWICK_AGENT_URL || String(useRuntimeConfig().agentUrl || '')
  const key = `${agents}\n${single}`
  if (parsedFrom !== key) {
    try {
      parsed = parseAgents(agents, single)
    }
    catch (error) {
      if (error instanceof AgentsConfigError) throw new AgentProxyError(500, 'INTERNAL_ERROR', error.message)
      throw error
    }
    parsedFrom = key
  }
  return parsed
}

/**
 * The agent a request is for. With one server configured the name may be left
 * out; with several it must be given, so that nothing is ever sent to a server
 * the page did not mean.
 */
export function agentFor(name: string | null | undefined): ConfiguredAgent {
  const agents = configuredAgents()
  if (name === null || name === undefined || name === '') {
    if (agents.length === 1) return agents[0]!
    throw new AgentProxyError(400, 'SERVER_REQUIRED', 'Several servers are configured: name one, as in /api/servers/<name>/agent/…', { servers: agents.map(a => a.name) })
  }
  const agent = agents.find(a => a.name === name)
  if (!agent) throw new AgentProxyError(404, 'UNKNOWN_SERVER', `This dashboard knows no server named ${JSON.stringify(String(name).slice(0, 64))}`, { servers: agents.map(a => a.name) })
  return agent
}

/** The name of the httpOnly cookie that carries the token for one server. */
export function sessionCookieName(agent: ConfiguredAgent): string {
  return sessionCookieFor(agent, configuredAgents())
}

/** Writes `{error:{code,message,details}}`, the same shape the agent uses. */
export function sendErrorEnvelope(event: H3Event, status: number, code: string, message: string, details: Record<string, unknown> = {}) {
  setResponseStatus(event, status)
  setResponseHeader(event, 'content-type', 'application/json; charset=utf-8')
  setResponseHeader(event, 'cache-control', 'no-store')
  return { error: { code, message, details } }
}

export function sendProxyError(event: H3Event, error: unknown) {
  if (error instanceof AgentProxyError) {
    return sendErrorEnvelope(event, error.status, error.code, error.message, error.details)
  }
  return sendErrorEnvelope(event, 500, 'INTERNAL_ERROR', 'The dashboard server failed to handle the request')
}

/**
 * CSRF protection for state-changing requests.
 *
 * Browsers do not let another origin attach a custom header to a request
 * without a CORS preflight, and this server never answers one. Requiring the
 * header therefore proves the request was made by this application's own
 * JavaScript. Fetch metadata is checked as well where the browser sends it.
 */
export function assertSameOriginRequest(event: H3Event): void {
  if (getRequestHeader(event, CSRF_HEADER) !== '1') {
    throw new AgentProxyError(403, 'CSRF_REJECTED', 'Missing X-Shipwick-Request header: state-changing requests must come from the dashboard itself')
  }
  const site = getRequestHeader(event, 'sec-fetch-site')
  if (site && site !== 'same-origin' && site !== 'none') {
    throw new AgentProxyError(403, 'CSRF_REJECTED', 'Cross-site requests are not allowed')
  }
}

/** Tokens travel in an HTTP header, so restrict them to visible ASCII. */
export function isPlausibleToken(value: unknown): value is string {
  return typeof value === 'string' && value.length >= 1 && value.length <= 512 && /^[\x21-\x7E]+$/.test(value)
}

export interface AgentRequestOptions {
  /** Which agent: one of configuredAgents(). */
  agent: ConfiguredAgent
  method: string
  /** Path below the agent's base URL; must start with /api/v1/. */
  path: string
  /** Raw query string including the leading "?", or "". */
  search?: string
  /** Left out for the two endpoints that take none: what a sign-in asks before there is a session. */
  token?: string
  headers?: Record<string, string>
  /** A buffered body, or a stream that is piped to the agent as it arrives (an archive upload). */
  body?: Buffer | Readable
  /**
   * How long the agent may take to answer with headers. For a streamed body
   * the clock starts once the last byte has been sent, so a slow upload is
   * never mistaken for a silent agent. Default HEADERS_TIMEOUT_MS; a stream
   * is not limited once its headers are there.
   */
  headersTimeoutMs?: number
  /** Aborts the upstream request, e.g. when the browser went away. */
  signal?: AbortSignal
}

/**
 * Performs a request against the agent and resolves as soon as the response
 * headers arrive; the body is handed back as a stream.
 *
 * node:http is used rather than fetch on purpose: undici aborts a response
 * whose body stays silent for five minutes, which is exactly what following
 * the logs of a quiet application looks like.
 */
export function agentRequest(options: AgentRequestOptions): Promise<IncomingMessage> {
  const base = options.agent.url
  let target: URL
  try {
    if (!options.path.startsWith(AGENT_API_PREFIX)) throw outsideApi()
    target = new URL(base + options.path + (options.search ?? ''))
    // Belt and braces: whatever normalization URL applied, the result must still be inside the API.
    if (!target.pathname.startsWith(new URL(base).pathname.replace(/\/+$/, '') + AGENT_API_PREFIX)) throw outsideApi()
  }
  catch (error) {
    return Promise.reject(error)
  }

  const send = target.protocol === 'https:' ? httpsRequest : httpRequest
  const body = options.body
  const streamed = body !== undefined && !Buffer.isBuffer(body)
  const timeoutMs = options.headersTimeoutMs ?? HEADERS_TIMEOUT_MS

  return new Promise<IncomingMessage>((resolve, reject) => {
    const req = send(target, {
      method: options.method,
      signal: options.signal,
      headers: {
        ...options.headers,
        ...(options.token === undefined ? {} : { authorization: `Bearer ${options.token}` }),
        'user-agent': 'shipwick-dashboard',
        ...(Buffer.isBuffer(body) ? { 'content-length': String(body.length) } : {}),
      },
    })

    let timer: ReturnType<typeof setTimeout> | null = null
    const armTimer = () => {
      timer = setTimeout(() => {
        req.destroy(new AgentProxyError(504, 'AGENT_TIMEOUT', `The Shipwick agent at ${base} did not answer within ${timeoutMs / 1000}s`, { agent_url: base }))
      }, timeoutMs)
    }

    req.once('response', (res) => {
      if (timer) clearTimeout(timer)
      resolve(res)
    })
    req.once('error', (error: NodeJS.ErrnoException) => {
      if (timer) clearTimeout(timer)
      if (error instanceof AgentProxyError || error.name === 'AbortError') return reject(error)
      // Report the cause by its code only. Never serialize the request: its headers hold the token.
      reject(new AgentProxyError(502, 'AGENT_UNREACHABLE', `Cannot reach the Shipwick agent at ${base}`, {
        agent_url: base,
        cause: error.code ?? 'connection failed',
      }))
    })

    if (streamed) {
      // The browser's upload flows straight through; the agent may answer early (a refusal) and then the pipe simply ends.
      body.once('error', () => req.destroy(new AgentProxyError(400, 'INVALID_REQUEST', 'The upload was interrupted')))
      req.once('finish', armTimer)
      body.pipe(req)
    }
    else {
      armTimer()
      req.end(body)
    }
  })
}

function outsideApi(): AgentProxyError {
  return new AgentProxyError(400, 'INVALID_REQUEST', 'Only paths under /api/v1/ can be reached')
}

export function sessionCookieOptions(event: H3Event) {
  // Secure whenever the browser used https (directly or via a TLS-terminating
  // proxy that sets X-Forwarded-Proto). SHIPWICK_COOKIE_SECURE overrides detection.
  const forced = process.env.SHIPWICK_COOKIE_SECURE
  const secure = forced === 'true' || (forced !== 'false' && getRequestProtocol(event, { xForwardedProto: true }) === 'https')
  return {
    httpOnly: true,
    sameSite: 'strict' as const,
    secure,
    path: '/',
  }
}
