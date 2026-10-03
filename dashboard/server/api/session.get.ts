/**
 * GET /api/session → the servers this dashboard knows, by name, and whether
 * this browser holds a sign-in for each. `authenticated` is the answer for a
 * dashboard with one server.
 *
 * Deliberately cheap: it does not call an agent. A cookie holding a token the
 * agent no longer accepts is discovered by the first proxied request (401),
 * which clears it.
 */
export default defineEventHandler((event) => {
  try {
    const agents = configuredAgents()
    const servers = agents.map(agent => ({ name: agent.name, authenticated: Boolean(getCookie(event, sessionCookieName(agent))) }))
    setResponseHeader(event, 'cache-control', 'no-store')
    return { authenticated: servers.length === 1 && servers[0]!.authenticated, servers }
  }
  catch (error) {
    return sendProxyError(event, error)
  }
})
