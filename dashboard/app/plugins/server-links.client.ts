import type { RouteLocationNormalizedLoaded, RouteLocationRaw } from 'vue-router'

/**
 * With several servers, every link on a page names the page's server in its
 * address, so that opening it in a new tab or copying it lands on the same
 * server. Links are written without one throughout the app (`/applications`);
 * this adds `?server=` where the router turns them into an address. With one
 * server it changes nothing.
 */
export default defineNuxtPlugin(() => {
  const router = useRouter()
  const session = useSession()
  const resolve = router.resolve.bind(router)

  router.resolve = ((to: RouteLocationRaw, current?: RouteLocationNormalizedLoaded) => {
    const resolved = resolve(to, current)
    const server = session.selected.value
    if (!session.multiple.value || !server || resolved.query.server !== undefined || resolved.path === '/login') return resolved
    return resolve({ path: resolved.path, query: { ...resolved.query, server }, hash: resolved.hash }, current)
  }) as typeof router.resolve
})
