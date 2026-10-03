import type { ConfiguredAgent } from '../../utils/agents'

/**
 * GET /auth/callback?code=…&state=… → where the provider sends the browser
 * back. The code is handed to the agent together with what the cookie kept;
 * the agent redeems it, verifies who signed in and answers with a session,
 * which is stored exactly as a token is: in the server's httpOnly cookie.
 *
 * Neither request's body is ever logged, and the reason for a failure reaches
 * the sign-in page through a cookie, not through the address.
 */
export default defineEventHandler(async (event) => {
  const pending = parsePending(getCookie(event, SIGNIN_COOKIE))
  // Used once, whatever happens next.
  deleteCookie(event, SIGNIN_COOKIE, { httpOnly: true, secure: sessionCookieOptions(event).secure, sameSite: 'lax', path: '/auth' })
  setResponseHeader(event, 'cache-control', 'no-store')

  let agent: ConfiguredAgent | null = null
  try {
    const query = getQuery(event)
    const state = typeof query.state === 'string' ? query.state : ''
    if (!pending || !sameValue(state, pending.state)) return failSignIn(event, null, FAILURE_NOT_STARTED_HERE)

    agent = agentFor(pending.server)
    if (typeof query.error === 'string' && query.error !== '') return failSignIn(event, agent, providerRefusal(query.error))
    const code = typeof query.code === 'string' ? query.code : ''
    if (code === '') return failSignIn(event, agent, FAILURE_NOT_STARTED_HERE)

    const { config, problem } = await fetchSignInConfig(agent)
    if (!config) {
      return failSignIn(event, agent, problem ? describeFailure(502, 'SIGN_IN_UNAVAILABLE', problem) : describeFailure(409, 'SIGN_IN_NOT_CONFIGURED', ''))
    }

    const headers: Record<string, string> = { 'accept': 'application/json', 'content-type': 'application/json' }
    // The audit trail's entry for the sign-in names the browser, not the dashboard.
    const client = clientAddress(getRequestHeader(event, 'x-forwarded-for'), event.node.req.socket.remoteAddress)
    if (client) headers['x-forwarded-for'] = client

    const response = await agentRequest({
      agent,
      method: 'POST',
      path: `${AGENT_API_PREFIX}auth/exchange`,
      headers,
      body: Buffer.from(JSON.stringify({ code, code_verifier: pending.verifier, nonce: pending.nonce, redirect_uri: config.redirect_uri })),
    })
    const answer = await readAgentJson(response) as {
      data?: { session?: unknown, identity?: { expires_at?: unknown } }
      error?: { code?: unknown, message?: unknown, details?: unknown }
    } | null

    if (response.statusCode !== 200) {
      const error = answer?.error
      return failSignIn(event, agent, describeFailure(
        response.statusCode ?? 502,
        typeof error?.code === 'string' ? error.code : '',
        typeof error?.message === 'string' ? error.message : '',
        typeof error?.details === 'object' && error.details !== null ? error.details as Record<string, unknown> : {},
      ))
    }
    const session = answer?.data?.session
    if (!isPlausibleToken(session)) return failSignIn(event, agent, describeFailure(502, '', ''))

    setCookie(event, sessionCookieName(agent), session, {
      ...sessionCookieOptions(event),
      maxAge: sessionMaxAge(answer?.data?.identity?.expires_at, SESSION_MAX_AGE_SECONDS),
    })
    return sendRedirect(event, configuredAgents().length > 1 ? `/?server=${encodeURIComponent(agent.name)}` : '/', 302)
  }
  catch (error) {
    return failSignIn(event, agent, describeFailure(502, error instanceof AgentProxyError ? error.code : '', ''))
  }
})
