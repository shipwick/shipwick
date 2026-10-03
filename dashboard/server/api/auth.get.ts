/**
 * GET /api/auth?server=<name> → what the sign-in page needs to know before
 * anyone is signed in: whether this server's agent lets people sign in
 * through a provider, the provider's address to name on the button, and why
 * the last sign-in of this browser did not complete (said once).
 *
 * Nothing else of the agent's answer is passed on: the rest is for the
 * redirect this server builds itself (server/routes/auth/login.get.ts).
 */
export default defineEventHandler(async (event) => {
  try {
    const name = getQuery(event).server
    const agent = agentFor(typeof name === 'string' ? name : null)

    const failure = parseFailure(getCookie(event, SIGNIN_RESULT_COOKIE))
    if (failure) deleteCookie(event, SIGNIN_RESULT_COOKIE, { ...sessionCookieOptions(event), path: '/api/auth' })

    const { config, problem } = await fetchSignInConfig(agent)
    setResponseHeader(event, 'cache-control', 'no-store')
    return { configured: config !== null || problem !== '', issuer: config?.issuer ?? '', problem, failure }
  }
  catch (error) {
    return sendProxyError(event, error)
  }
})
