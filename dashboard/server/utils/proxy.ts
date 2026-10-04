/**
 * /api/agent/** and /api/servers/<name>/agent/** → <the agent>/api/v1/**
 *
 * The browser never talks to the agent and never sees the token: this handler
 * takes it from the httpOnly session cookie and adds the Authorization header.
 *
 * - Status code and body pass through untouched (the JSON envelope is the agent's).
 * - The response body is piped, never buffered: `logs?follow=true` arrives line
 *   by line and a volume archive is downloaded as it streams from the agent.
 * - Only an allowlist of request headers is forwarded. Cookies never are.
 *   X-Forwarded-For is set here, to the address the request came from, so
 *   that the agent's audit trail names the browser and not the dashboard.
 * - POST/PUT/DELETE require the X-Shipwick-Request header (CSRF).
 * - A PUT body is streamed to the agent as it arrives, without a size limit of
 *   its own: a volume archive is gigabytes and the agent enforces 10 GB; a
 *   secret's value, a registry credential and a certificate are small JSON
 *   bodies and the agent enforces its limits on those too. So is the body of
 *   POST /import, an export of the whole server; its passphrase travels in a
 *   header that is passed on for that request alone and never logged.
 *   The dashboard uploads neither static folders nor images; that is the CLI's.
 * - How long the agent gets to answer depends on what was asked: stopping or
 *   deleting an application waits for its replicas to exit, which
 *   deploy.stop_timeout allows to take minutes (server/utils/timeouts.ts).
 * - A stream the agent ends with its error trailer is cut, not ended: the
 *   browser then reports a download that failed instead of saving a file
 *   that looks whole and is not (relayResponse).
 */

import type { IncomingMessage } from 'node:http'
import type { Readable } from 'node:stream'
import type { H3Event } from 'h3'

const ALLOWED_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'DELETE'])
const FORWARDED_REQUEST_HEADERS = ['accept', 'content-type'] as const
// retry-after: a 429 says when the agent will look at tokens from this address again.
const FORWARDED_RESPONSE_HEADERS = ['content-type', 'content-length', 'content-disposition', 'retry-after'] as const

/** The agent refuses JSON bodies over 64 KB; leave it a little room to say so itself. */
const MAX_BODY_BYTES = 128 * 1024

/** Carries the passphrase of an uploaded export, base64-encoded: the body is the file itself. */
const PASSPHRASE_HEADER = 'x-shipwick-passphrase'

/**
 * Set by the agent when an export, of the server or of the audit trail, broke
 * off after its first byte: the status was 200 long before.
 */
const EXPORT_ERROR_TRAILER = 'x-shipwick-export-error'

/**
 * Passes one request on to an agent: `server` names it, or is null where the
 * address does not (/api/agent/**, which is the one server's).
 */
export async function proxyToAgent(event: H3Event, server: string | null): Promise<unknown> {
  let release = () => {}
  try {
    const agent = agentFor(server)
    const cookie = sessionCookieName(agent)
    const method = event.method.toUpperCase()
    if (!ALLOWED_METHODS.has(method)) {
      throw new AgentProxyError(405, 'METHOD_NOT_ALLOWED', `${method} is not supported`)
    }
    if (method !== 'GET' && method !== 'HEAD') assertSameOriginRequest(event)

    const token = getCookie(event, cookie)
    if (!token || !isPlausibleToken(token)) {
      throw new AgentProxyError(401, 'UNAUTHORIZED', 'Not signed in')
    }

    const segments = safeSegments(getRouterParam(event, 'path') ?? '')
    const path = AGENT_API_PREFIX + segments.map(encodeURIComponent).join('/')
    const queryStart = event.path.indexOf('?')
    const search = queryStart === -1 ? '' : event.path.slice(queryStart)

    const headers = forwardedHeaders(event)
    let body: Buffer | Readable | undefined
    const headersTimeoutMs = answerTimeoutMs(method, segments)
    if (streamsBody(method, segments)) {
      // Never buffered: an archive, an export, or a small JSON body. The agent
      // wants the length to refuse an oversized upload before reading it.
      body = event.node.req
      const length = getRequestHeader(event, 'content-length')
      if (length) headers['content-length'] = length
      if (method === 'POST') {
        const passphrase = getRequestHeader(event, PASSPHRASE_HEADER)
        if (passphrase) headers[PASSPHRASE_HEADER] = passphrase
      }
      // A file of gigabytes takes longer to arrive than a request is given by default.
      release = holdRequestTimeout(event)
      event.node.res.once('close', release)
    }
    else if (method === 'POST') {
      body = await readSmallBody(event)
    }

    // If the browser goes away (tab closed, log stream stopped), stop the upstream request too.
    const abort = new AbortController()
    event.node.res.once('close', () => abort.abort())

    const upstream = await agentRequest({ agent, method, path, search, token, headers, body, headersTimeoutMs, signal: abort.signal })

    // The agent no longer accepts this token: the session is over.
    if (upstream.statusCode === 401) deleteCookie(event, cookie, sessionCookieOptions(event))

    await relayResponse(event, upstream)
  }
  catch (error) {
    release()
    if (event.node.res.headersSent) {
      event.node.res.destroy()
      return
    }
    if (error instanceof Error && error.name === 'AbortError') return
    return sendProxyError(event, error)
  }
}

/**
 * The request headers the agent is sent: an allowlist, and the address the
 * request came from. The agent's audit trail records who asked: without
 * X-Forwarded-For it only ever sees the dashboard's own address.
 */
export function forwardedHeaders(event: H3Event): Record<string, string> {
  const headers: Record<string, string> = {}
  for (const name of FORWARDED_REQUEST_HEADERS) {
    const value = getRequestHeader(event, name)
    if (value) headers[name] = value
  }
  const client = clientAddress(getRequestHeader(event, 'x-forwarded-for'), event.node.req.socket.remoteAddress)
  if (client) headers['x-forwarded-for'] = client
  return headers
}

/** A JSON body of a POST, read whole: undefined when there is none, refused when it is larger than any the agent takes. */
export async function readSmallBody(event: H3Event): Promise<Buffer | undefined> {
  if (Number(getRequestHeader(event, 'content-length') ?? 0) > MAX_BODY_BYTES) throw bodyTooLarge()
  const body = await readRawBody(event, false)
  if (body && body.length > MAX_BODY_BYTES) throw bodyTooLarge()
  return body && body.length > 0 ? body : undefined
}

/**
 * Hands the agent's answer to the browser as it arrives: status, the headers
 * that describe the body, then the body piped and never buffered, so that a
 * followed log arrives line by line and a download of gigabytes takes no
 * memory here. Resolves when the response has ended or the browser has gone.
 *
 * An answer that announces the export trailer is ended only if the trailer
 * stayed empty. With it set, or when the agent's connection breaks, the
 * connection to the browser is cut: a download that stops short is reported
 * as failed by the browser, where one that ends normally is kept as a file.
 */
export function relayResponse(event: H3Event, upstream: IncomingMessage): Promise<void> {
  const res = event.node.res
  res.statusCode = upstream.statusCode ?? 502
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

  const trailers = String(upstream.headers.trailer ?? '').toLowerCase().split(',').map(name => name.trim())
  const mayBreakOff = trailers.includes(EXPORT_ERROR_TRAILER)

  return new Promise<void>((resolve) => {
    const cut = () => {
      res.destroy()
      resolve()
    }
    upstream.once('error', cut)
    // The agent's connection ended before its answer did.
    upstream.once('close', () => {
      if (!upstream.complete) cut()
    })
    res.once('close', resolve)
    if (!mayBreakOff) {
      upstream.pipe(res)
      return
    }
    upstream.pipe(res, { end: false })
    upstream.once('end', () => {
      if (upstream.trailers[EXPORT_ERROR_TRAILER]) res.destroy()
      else res.end()
    })
  })
}

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
