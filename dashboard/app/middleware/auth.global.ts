/**
 * Everything except /login needs a session. This is a convenience for the
 * user, not the security boundary: the boundary is the server-side proxy,
 * which answers 401 without a valid session cookie whatever the client does.
 */
export default defineNuxtRouteMiddleware(async (to) => {
  const session = useSession()
  const authenticated = await session.check()

  if (to.path === '/login') {
    if (authenticated) return navigateTo(safeRedirect(to.query.redirect), { replace: true })
    return
  }
  if (!authenticated) {
    return navigateTo({ path: '/login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }, { replace: true })
  }
})
