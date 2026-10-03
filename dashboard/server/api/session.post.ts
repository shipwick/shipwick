import type { IncomingMessage } from 'node:http'

/**
 * POST /api/session {token, server?} → verifies the token against the agent
 * and, if it is accepted, stores it in an httpOnly cookie of that server's.
 * `server` may be left out when one server is configured. The token is never
 * echoed back and never logged.
 */
export default defineEventHandler(async (event) => {
  try {
    assertSameOriginRequest(event)

    const body = await readBody<{ token?: unknown, server?: unknown } | null>(event).catch(() => null)
    const agent = agentFor(body && typeof body.server === 'string' ? body.server : null)
    const token = body && typeof body.token === 'string' ? body.token.trim() : ''
    if (!isPlausibleToken(token)) {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Enter the agent token')
    }

    const response = await agentRequest({
      agent,
      method: 'GET',
      path: `${AGENT_API_PREFIX}server`,
      token,
      headers: { accept: 'application/json' },
    })

    if (response.statusCode === 401) {
      // A token that has expired is told so, in the agent's words: it is not a wrong token.
      const refusal = await readRefusal(response)
      if (refusal) throw new AgentProxyError(401, 'TOKEN_EXPIRED', refusal.message, refusal.details)
      throw new AgentProxyError(401, 'UNAUTHORIZED', 'The agent rejected this token')
    }
    // Drain the body so the socket is released; its content is not needed.
    response.resume()

    // The agent did not look at the token: too many failures from this address (the dashboard server's, as the agent sees it).
    if (response.statusCode === 429) {
      const retryAfter = Number(response.headers['retry-after'])
      setResponseHeader(event, 'retry-after', Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : 60)
      throw new AgentProxyError(429, 'RATE_LIMITED', 'Too many failed attempts from this address; try again in a minute.')
    }
    if (response.statusCode !== 200) {
      throw new AgentProxyError(502, 'AGENT_UNREACHABLE', `${agent.url} answered with status ${response.statusCode}. Is it a Shipwick agent?`, { agent_url: agent.url })
    }

    setCookie(event, sessionCookieName(agent), token, { ...sessionCookieOptions(event), maxAge: SESSION_MAX_AGE_SECONDS })
    setResponseHeader(event, 'cache-control', 'no-store')
    return { authenticated: true, server: agent.name }
  }
  catch (error) {
    return sendProxyError(event, error)
  }
})

/** The agent's TOKEN_EXPIRED envelope out of a 401, or null for any other refusal. At most 8 KB are read. */
async function readRefusal(response: IncomingMessage): Promise<{ message: string, details: Record<string, unknown> } | null> {
  const chunks: Buffer[] = []
  let size = 0
  try {
    for await (const chunk of response) {
      size += (chunk as Buffer).length
      if (size > 8 * 1024) {
        response.destroy()
        return null
      }
      chunks.push(chunk as Buffer)
    }
    const error = (JSON.parse(Buffer.concat(chunks).toString('utf8')) as { error?: { code?: unknown, message?: unknown, details?: unknown } }).error
    if (error?.code !== 'TOKEN_EXPIRED' || typeof error.message !== 'string') return null
    return { message: error.message, details: typeof error.details === 'object' && error.details !== null ? error.details as Record<string, unknown> : {} }
  }
  catch {
    return null
  }
}
