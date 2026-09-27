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
 * - A PUT body (a volume archive, gigabytes) is streamed to the agent as it
 *   arrives, without a size limit of its own: the agent enforces 10 GB.
 */

import type { Readable } from 'node:stream'

const ALLOWED_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'DELETE'])
const FORWARDED_REQUEST_HEADERS = ['accept', 'content-type'] as const
const FORWARDED_RESPONSE_HEADERS = ['content-type', 'content-length', 'content-disposition'] as const

/** The agent refuses JSON bodies over 64 KB; leave it a little room to say so itself. */
const MAX_BODY_BYTES = 128 * 1024

/**
 * How long the agent may take to answer a streamed upload once the last byte
 * has been sent: it extracts the archive into the volume before it answers.
 */
const UPLOAD_ANSWER_TIMEOUT_MS = 10 * 60_000

/** Path segments the agent uses are names, numbers and fixed words: nothing else gets through. */
const SAFE_SEGMENT = /^[\w.~-]+$/

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

    const path = AGENT_API_PREFIX + safePath(getRouterParam(event, 'path') ?? '')
    const queryStart = event.path.indexOf('?')
    const search = queryStart === -1 ? '' : event.path.slice(queryStart)

    const headers: Record<string, string> = {}
    for (const name of FORWARDED_REQUEST_HEADERS) {
      const value = getRequestHeader(event, name)
      if (value) headers[name] = value
    }

    let body: Buffer | Readable | undefined
    let headersTimeoutMs: number | undefined
    if (method === 'POST') {
      if (Number(getRequestHeader(event, 'content-length') ?? 0) > MAX_BODY_BYTES) throw bodyTooLarge()
      body = await readRawBody(event, false)
      if (body && body.length > MAX_BODY_BYTES) throw bodyTooLarge()
      if (body && body.length === 0) body = undefined
    }
    else if (method === 'PUT') {
      // Never buffered: the only PUT is a volume archive. The agent wants the
      // length to refuse an oversized upload before reading it.
      body = event.node.req
      const length = getRequestHeader(event, 'content-length')
      if (length) headers['content-length'] = length
      headersTimeoutMs = UPLOAD_ANSWER_TIMEOUT_MS
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

function safePath(raw: string): string {
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
    if (decoded === '.' || decoded === '..' || !SAFE_SEGMENT.test(decoded)) {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Malformed path')
    }
    return encodeURIComponent(decoded)
  }).join('/')
}

function bodyTooLarge(): AgentProxyError {
  return new AgentProxyError(413, 'INVALID_REQUEST', 'Request body too large')
}
