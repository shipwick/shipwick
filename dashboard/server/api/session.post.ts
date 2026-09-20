/**
 * POST /api/session {token} → verifies the token against the agent and, if it
 * is accepted, stores it in an httpOnly cookie. The token is never echoed back
 * and never logged.
 */
export default defineEventHandler(async (event) => {
  try {
    assertSameOriginRequest(event)

    const body = await readBody<{ token?: unknown } | null>(event).catch(() => null)
    const token = body && typeof body.token === 'string' ? body.token.trim() : ''
    if (!isPlausibleToken(token)) {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Enter the agent token')
    }

    const response = await agentRequest({
      method: 'GET',
      path: `${AGENT_API_PREFIX}server`,
      token,
      headers: { accept: 'application/json' },
    })
    // Drain the body so the socket is released; its content is not needed.
    response.resume()

    if (response.statusCode === 401) {
      throw new AgentProxyError(401, 'UNAUTHORIZED', 'The agent rejected this token')
    }
    if (response.statusCode !== 200) {
      const base = agentBaseUrl()
      throw new AgentProxyError(502, 'AGENT_UNREACHABLE', `${base} answered with status ${response.statusCode}. Is it a Shipwick agent?`, { agent_url: base })
    }

    setCookie(event, SESSION_COOKIE, token, { ...sessionCookieOptions(event), maxAge: SESSION_MAX_AGE_SECONDS })
    setResponseHeader(event, 'cache-control', 'no-store')
    return { authenticated: true }
  }
  catch (error) {
    return sendProxyError(event, error)
  }
})
