/**
 * Where everything lives. Pure, so the grouping, the active entry and the
 * addresses that moved are unit-tested.
 *
 * Three groups, by what a person is thinking about: what runs (applications,
 * their deployments and logs), the machine it runs on, and what is set up once
 * and then left alone.
 */

export type NavIcon = 'overview' | 'applications' | 'deployments' | 'logs' | 'servers' | 'disk' | 'certificate' | 'lock' | 'registry' | 'key'

export interface NavItem {
  to: string
  label: string
  icon: NavIcon
  /** Paths that belong to this entry besides `to` and what lies below it. */
  also?: readonly string[]
  /** Only matches its own path, not what lies below it. */
  exact?: boolean
  /** Shown once the token is known to be an admin's: the page is refused to every other role. */
  admin?: boolean
}

export interface NavGroup {
  /** No label: the first group needs none. */
  label: string
  items: readonly NavItem[]
}

export const NAV_GROUPS: readonly NavGroup[] = [
  {
    label: '',
    items: [
      { to: '/', label: 'Overview', icon: 'overview', exact: true },
      // The page that deploys a pasted deploy.yaml creates an application, or changes one.
      { to: '/applications', label: 'Applications', icon: 'applications', also: ['/deploy'] },
      { to: '/deployments', label: 'Deployments', icon: 'deployments' },
      { to: '/logs', label: 'Logs', icon: 'logs' },
    ],
  },
  {
    label: 'Server',
    items: [
      { to: '/servers', label: 'Status', icon: 'servers' },
      { to: '/volumes', label: 'Volumes', icon: 'disk' },
      { to: '/certificates', label: 'Certificates', icon: 'certificate' },
    ],
  },
  {
    label: 'Settings',
    items: [
      { to: '/settings/secrets', label: 'Secrets', icon: 'lock' },
      { to: '/settings/registries', label: 'Registries', icon: 'registry' },
      { to: '/settings/access', label: 'Access', icon: 'key', admin: true },
    ],
  },
]

/** Whether `path` is the entry's page or one below it. */
export function isActive(item: Pick<NavItem, 'to' | 'also' | 'exact'>, path: string): boolean {
  const under = (base: string) => path === base || (!item.exact && path.startsWith(`${base}/`))
  return under(item.to) || (item.also ?? []).some(under)
}

/** The entry `path` belongs to, for the page title of a narrow screen's top bar; null on a page outside the navigation. */
export function activeItem(path: string, groups: readonly NavGroup[] = NAV_GROUPS): NavItem | null {
  for (const group of groups) {
    for (const item of group.items) {
      if (isActive(item, path)) return item
    }
  }
  return null
}

/**
 * Addresses from before the navigation was grouped. A bookmark, a link in a
 * chat or in a runbook keeps working: the path is rewritten and the query and
 * what follows the old prefix are kept.
 */
const MOVED: readonly (readonly [from: string, to: string])[] = [
  ['/tokens', '/settings/access'],
  ['/secrets', '/settings/secrets'],
  ['/registries', '/settings/registries'],
  ['/settings', '/settings/secrets'],
]

/** The new path for one that moved; null for a path that is where it always was. */
export function movedPath(path: string): string | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, '') : path
  for (const [from, to] of MOVED) {
    if (clean === from) return to
    // `/settings` is only the bare address: everything below it is a real page.
    if (from !== '/settings' && clean.startsWith(`${from}/`)) return to + clean.slice(from.length)
  }
  return null
}
