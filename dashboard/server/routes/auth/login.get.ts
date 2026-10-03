import type { ConfiguredAgent } from '../../utils/agents'
import type { PendingSignIn } from '../../utils/signin'

/**
 * GET /auth/login?server=<name> → begins a sign-in through the agent's
 * OpenID Connect provider: three random values are kept in a cookie for the
 * callback, and the browser is sent to the provider with what identifies this
 * sign-in. A plain navigation: it changes nothing, so it needs no CSRF header.
 */
export default defineEventHandler(async (event) => {
  let agent: ConfiguredAgent | null = null
  try {
    const name = getQuery(event).server
    agent = agentFor(typeof name === 'string' ? name : null)
    const { config } = await fetchSignInConfig(agent)
    // Nothing to sign in with: the page offers the token field alone.
    if (!config) return sendRedirect(event, loginPath(agent), 302)

    const pending: PendingSignIn = { state: randomValue(), nonce: randomValue(), verifier: randomValue(), server: agent.name }
    setCookie(event, SIGNIN_COOKIE, JSON.stringify(pending), {
      httpOnly: true,
      secure: sessionCookieOptions(event).secure,
      // The way back is a navigation that starts at the provider: a Strict cookie would not come with it.
      sameSite: 'lax',
      path: '/auth',
      maxAge: SIGNIN_MAX_AGE_SECONDS,
    })
    setResponseHeader(event, 'cache-control', 'no-store')
    return sendRedirect(event, authorizationUrl(config, pending), 302)
  }
  catch {
    return sendRedirect(event, loginPath(agent), 302)
  }
})
