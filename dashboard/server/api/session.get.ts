/**
 * GET /api/session → whether this browser holds a session cookie.
 *
 * Deliberately cheap: it does not call the agent. A cookie holding a token the
 * agent no longer accepts is discovered by the first proxied request (401),
 * which clears it.
 */
export default defineEventHandler((event) => {
  setResponseHeader(event, 'cache-control', 'no-store')
  return { authenticated: Boolean(getCookie(event, SESSION_COOKIE)) }
})
