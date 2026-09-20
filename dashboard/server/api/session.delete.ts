/** DELETE /api/session → sign out. */
export default defineEventHandler((event) => {
  try {
    assertSameOriginRequest(event)
  }
  catch (error) {
    return sendProxyError(event, error)
  }
  deleteCookie(event, SESSION_COOKIE, sessionCookieOptions(event))
  setResponseHeader(event, 'cache-control', 'no-store')
  return { authenticated: false }
})
