/**
 * /api/agent/** → ${SHIPWICK_AGENT_URL}/api/v1/**
 *
 * The browser never talks to the agent and never sees the token: this handler
 * takes it from the httpOnly session cookie and adds the Authorization header.
 *
 * - Status code and body pass through untouched (the JSON envelope is the agent's).
 * - The response body is piped, never buffered: `logs?follow=true` arrives line
 *   by line and a volume archive is downloaded as it streams from the agent.
 * - Only an allowlist of request headers is forwarded. Cookies never are.
 * - POST/PUT/DELETE require the X-Shipwick-Request header (CSRF).
 * - A PUT body is streamed to the agent as it arrives, without a size limit of
 *   its own: a volume archive is gigabytes and the agent enforces 10 GB; a
 *   secret's value, a registry credential and a certificate are small JSON
 *   bodies and the agent enforces its limits on those too.
 *   The dashboard uploads neither static folders nor images; that is the CLI's.
 * - How long the agent gets to answer depends on what was asked: stopping or
 *   deleting an application waits for its replicas to exit, which
 *   deploy.stop_timeout allows to take minutes (server/utils/timeouts.ts).
 */

import type { Readable } from 'node:stream'

const ALLOWED_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'DELETE'])
const FORWARDED_REQUEST_HEADERS = ['accept', 'content-type'] as const
// retry-after: a 429 says when the agent will look at tokens from this address again.
const FORWARDED_RESPONSE_HEADERS = ['content-type', 'content-length', 'content-disposition', 'retry-after'] as const

/** The agent refuses JSON bodies over 64 KB; leave it a little room to say so itself. */
const MAX_BODY_BYTES = 128 * 1024

export default defineEventHandler(async (event) => {
  try {
    const method = event.method.toUpperCase()
    if (!ALLOWED_METHODS.has(method)) {
      throw new AgentProxyError(405, 'METHOD_NOT_ALLOWED', `${method} is not supported`)
    }
    if (method !== 'GET' && method !== 'HEAD') assertSameOriginRequest(event)

    const token = getCookie(event, SESSION_COOKIE)
    if (!token || !isPlausibleToken(token)) {
      throw new AgentProxyError(401, 'UNAUTHORIZED', 'Not signed in')
    }

    const segments = safeSegments(getRouterParam(event, 'path') ?? '')
    const path = AGENT_API_PREFIX + segments.map(encodeURIComponent).join('/')
    const queryStart = event.path.indexOf('?')
    const search = queryStart === -1 ? '' : event.path.slice(queryStart)

    const headers: Record<string, string> = {}
    for (const name of FORWARDED_REQUEST_HEADERS) {
      const value = getRequestHeader(event, name)
      if (value) headers[name] = value
    }

    let body: Buffer | Readable | undefined
    const headersTimeoutMs = answerTimeoutMs(method, segments)
    if (method === 'POST') {
      if (Number(getRequestHeader(event, 'content-length') ?? 0) > MAX_BODY_BYTES) throw bodyTooLarge()
      body = await readRawBody(event, false)
      if (body && body.length > MAX_BODY_BYTES) throw bodyTooLarge()
      if (body && body.length === 0) body = undefined
    }
    else if (method === 'PUT') {
      // Never buffered: a PUT is a volume archive or a small JSON body. The agent
      // wants the length to refuse an oversized upload before reading it.
      body = event.node.req
      const length = getRequestHeader(event, 'content-length')
      if (length) headers['content-length'] = length
    }

    // If the browser goes away (tab closed, log stream stopped), stop the upstream request too.
    const abort = new AbortController()
    const res = event.node.res
    res.once('close', () => abort.abort())

    const upstream = await agentRequest({ method, path, search, token, headers, body, headersTimeoutMs, signal: abort.signal })
    const status = upstream.statusCode ?? 502

    // The agent no longer accepts this token: the session is over.
    if (status === 401) deleteCookie(event, SESSION_COOKIE, sessionCookieOptions(event))

    res.statusCode = status
    for (const name of FORWARDED_RESPONSE_HEADERS) {
      const value = upstream.headers[name]
      if (value !== undefined) res.setHeader(name, value)
    }
    res.setHeader('cache-control', 'no-store')
    res.setHeader('x-content-type-options', 'nosniff')
    // Ask reverse proxies in front of the dashboard (nginx) not to buffer streams.
    res.setHeader('x-accel-buffering', 'no')

    // Send the headers now: a followed log stream may stay silent for a long time.
    res.flushHeaders()
    res.socket?.setNoDelay(true)

    await new Promise<void>((resolve) => {
      upstream.once('error', () => {
        res.destroy()
        resolve()
      })
      res.once('close', resolve)
      upstream.pipe(res)
    })
  }
  catch (error) {
    if (event.node.res.headersSent) {
      event.node.res.destroy()
      return
    }
    if (error instanceof Error && error.name === 'AbortError') return
    return sendProxyError(event, error)
  }
})

/** The path below /api/v1 as decoded segments, each checked; they are encoded again for the upstream URL. */
function safeSegments(raw: string): string[] {
  const segments = raw.split('/').filter(s => s !== '')
  if (segments.length === 0) throw new AgentProxyError(404, 'NOT_FOUND', 'no such endpoint')
  return segments.map((segment) => {
    let decoded: string
    try {
      decoded = decodeURIComponent(segment)
    }
    catch {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Malformed path')
    }
    if (!isSafeSegment(decoded)) {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Malformed path')
    }
    return decoded
  })
}

function bodyTooLarge(): AgentProxyError {
  return new AgentProxyError(413, 'INVALID_REQUEST', 'Request body too large')
}
