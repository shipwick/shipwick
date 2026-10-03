import { movedPath } from '~/utils/navigation'

/**
 * Pages that have a new address keep answering at the old one: a bookmark or
 * a link in a runbook lands on the page it meant, with its query intact.
 */
export default defineNuxtRouteMiddleware((to) => {
  const path = movedPath(to.path)
  if (path) return navigateTo({ path, query: to.query, hash: to.hash }, { replace: true })
})
