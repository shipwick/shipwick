/**
 * The sections of the application page and of the server page. Each has an
 * address of its own, so "the backups of my-api" is a link. Pure, so which
 * tabs an application has is unit-tested.
 */
export interface Tab {
  key: string
  label: string
  to: string
  /** A count or a short mark next to the label. */
  badge?: string | number | null
  badgeTone?: 'warn' | 'danger' | 'muted'
}

export interface TabSubject {
  name: string
  /** A folder served by the proxy: no containers, so no logs and nothing to run. */
  static: boolean
  /** The spec lists volumes: there is something to back up. */
  volumes: boolean
}

export type ApplicationTabKey = 'overview' | 'metrics' | 'logs' | 'deployments' | 'jobs' | 'backups' | 'configuration'

export function applicationPath(name: string, tab: ApplicationTabKey = 'overview'): string {
  const base = `/applications/${encodeURIComponent(name)}`
  return tab === 'overview' ? base : `${base}/${tab}`
}

/** The tabs an application has; one that would only say "not for this application" is left out. */
export function applicationTabs(subject: TabSubject): Tab[] {
  const tab = (key: ApplicationTabKey, label: string): Tab => ({ key, label, to: applicationPath(subject.name, key) })
  const tabs = [tab('overview', 'Overview'), tab('metrics', subject.static ? 'Traffic' : 'Metrics')]
  if (!subject.static) tabs.push(tab('logs', 'Logs'))
  tabs.push(tab('deployments', 'Deployments'))
  if (!subject.static) tabs.push(tab('jobs', 'Jobs'))
  if (subject.volumes) tabs.push(tab('backups', 'Backups'))
  tabs.push(tab('configuration', 'Configuration'))
  return tabs
}

export interface ServerTabState {
  /** Applications that wait, stopped, to be promoted; 0 on a server that is not a standby. */
  waiting: number
}

export function serverTabs(state: ServerTabState = { waiting: 0 }): Tab[] {
  return [
    { key: 'status', label: 'Status', to: '/servers' },
    { key: 'backups', label: 'Backups', to: '/servers/backups' },
    { key: 'transfer', label: 'Export and standby', to: '/servers/transfer', badge: state.waiting > 0 ? state.waiting : null, badgeTone: 'warn' },
    { key: 'encryption', label: 'Encryption key', to: '/servers/encryption' },
  ]
}

/** The sections of Access: tokens for programs, rules for people, and the trail of what either did. */
export function accessTabs(): Tab[] {
  return [
    { key: 'tokens', label: 'API tokens', to: '/settings/access' },
    { key: 'sign-in', label: 'Sign-in', to: '/settings/access/sign-in' },
    { key: 'audit', label: 'Audit trail', to: '/settings/access/audit' },
  ]
}

/** The tab whose address is `path`: the longest match, so the first tab does not claim its siblings. */
export function activeTab(tabs: readonly Tab[], path: string): Tab | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, '') : path
  let best: Tab | null = null
  for (const tab of tabs) {
    if ((clean === tab.to || clean.startsWith(`${tab.to}/`)) && (!best || tab.to.length > best.to.length)) best = tab
  }
  return best
}

/**
 * The title of a page with tabs: the first tab is the page itself, another is
 * named before it ('Metrics · my-api'), so that the tabs of one page differ
 * in the browser's tab, in its history, and in what a screen reader says when
 * the page changes.
 */
export function tabTitle(tabs: readonly Tab[], path: string, page: string): string {
  const tab = activeTab(tabs, path)
  return !tab || tab === tabs[0] ? page : tab.label + ' · ' + page
}
