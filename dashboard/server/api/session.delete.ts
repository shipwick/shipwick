/**
 * DELETE /api/session?server=<name> → sign out of one server; the sign-ins
 * for the others stay. Without `server`: out of the one server, or, with
 * several configured, out of all of them.
 */
export default defineEventHandler((event) => {
  try {
    assertSameOriginRequest(event)
    const name = getQuery(event).server
    const agents = typeof name === 'string' && name !== '' ? [agentFor(name)] : configuredAgents()
    for (const agent of agents) deleteCookie(event, sessionCookieName(agent), sessionCookieOptions(event))
  }
  catch (error) {
    return sendProxyError(event, error)
  }
  setResponseHeader(event, 'cache-control', 'no-store')
  return { authenticated: false }
})
