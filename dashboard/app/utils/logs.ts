import type { Application } from '~/types/api'

/**
 * Which applications the log viewer offers. A static application is a folder
 * the proxy serves: it has no containers, so there is no log to read and the
 * agent answers 409 STATIC_APPLICATION. It is not offered.
 */

type Listed = Pick<Application, 'name' | 'static'>

/**
 * The names to choose from, sorted. An application named in the URL that the
 * list does not know (not loaded yet, or deleted since) is kept, so the
 * viewer can say what the agent says about it; a static one never is.
 */
export function logApplications(apps: readonly Listed[], selected: string = ''): string[] {
  const names = apps.filter(a => !a.static).map(a => a.name)
  if (selected !== '' && !apps.some(a => a.name === selected)) names.push(selected)
  return names.sort()
}

/** The application named in the URL is a static one: there is nothing to tail, and the page says so instead of asking. */
export function hasNoLogs(apps: readonly Listed[], selected: string): boolean {
  return apps.some(a => a.name === selected && a.static)
}
