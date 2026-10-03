import { safeRedirect } from '~/utils/redirect'
import { decideServer, wantedServer, withServer } from '~/utils/servers'

/**
 * Everything except /login needs a session, and with several servers every
 * address is about one of them. This is a convenience for the user, not the
 * security boundary: the boundary is the server-side proxy, which answers 401
 * without a valid session cookie whatever the client does.
 */
export default defineNuxtRouteMiddleware(async (to) => {
  const session = useSession()
  const servers = await session.check()

  const decision = decideServer({
    path: to.path,
    wanted: wantedServer(to.query.server),
    names: servers.map(s => s.name),
    selected: session.selected.value,
    remembered: session.remembered(),
  })

  switch (decision.action) {
    case 'list':
      return
    case 'add':
      return navigateTo({ path: to.path, query: { ...to.query, server: decision.server }, hash: to.hash })
    case 'choose':
      return navigateTo('/servers')
    case 'unknown':
      // Loaded afresh: the list is a page of its own, outside any one server.
      return navigateTo(`/servers?unknown=${encodeURIComponent(decision.server)}`, { external: true })
    case 'reload':
      return navigateTo(to.fullPath, { external: true })
  }

  const server = decision.server
  const multiple = session.multiple.value

  if (to.path === '/login') {
    if (server !== null && session.isAuthenticated(server)) {
      const target = safeRedirect(to.query.redirect)
      return navigateTo(multiple ? withServer(target, server) : target, { replace: true })
    }
    return
  }

  session.select(server)
  if (!session.isAuthenticated(server)) {
    return navigateTo({
      path: '/login',
      query: { ...(multiple && server ? { server } : {}), ...(to.fullPath === '/' ? {} : { redirect: to.fullPath }) },
    }, { replace: true })
  }
})
