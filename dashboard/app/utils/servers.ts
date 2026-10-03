/**
 * Several servers in one dashboard: which one an address is about. Pure, so
 * the rules are unit-tested.
 *
 * With one server configured none of this shows: addresses carry no server
 * and requests go to /api/agent. With several, every address names its server
 * as `?server=<name>`, so a link someone shares opens on the server it was
 * copied from, and every request to an agent names it in its path, so two
 * tabs on two servers never act on each other's.
 */

export interface KnownServer {
  /** As configured on the dashboard's server; the agent's own hostname is what GET /server says. */
  name: string
  /** This browser holds a sign-in for it. */
  authenticated: boolean
}

/** Where the dashboard's proxy to an agent is mounted: the one server's, or the named one's. */
export function agentBase(server: string | null, multiple: boolean): string {
  return multiple && server ? `/api/servers/${encodeURIComponent(server)}/agent` : '/api/agent'
}

export type ServerDecision =
  /** Go on; `server` is the one the page is about (null on the sign-in page before one is chosen). */
  | { action: 'proceed', server: string | null }
  /** `/servers` opened without a server: the list of them. */
  | { action: 'list' }
  /** The address names no server: the same address, on this one. */
  | { action: 'add', server: string }
  /** No server is known to mean: to the list. */
  | { action: 'choose' }
  /** The address names a server this dashboard does not have. */
  | { action: 'unknown', server: string }
  /** The address names another server than the page holds: load it afresh, so nothing of the first is left on screen. */
  | { action: 'reload' }

export interface ServerQuestion {
  path: string
  /** `?server=` of the address. */
  wanted: string | null
  /** The configured servers, in order. */
  names: readonly string[]
  /** The server this tab is on; null before the first page. */
  selected: string | null
  /** The server last opened in this browser, for an address typed without one. */
  remembered: string | null
}

export function decideServer(q: ServerQuestion): ServerDecision {
  if (q.names.length <= 1) return { action: 'proceed', server: q.names[0] ?? null }

  const known = (name: string | null): name is string => name !== null && q.names.includes(name)
  if (q.path === '/login') return { action: 'proceed', server: known(q.wanted) ? q.wanted : null }

  if (q.wanted === null || q.wanted === '') {
    // A link inside the page stays on the page's server; only a fresh visit to /servers is the list.
    if (known(q.selected)) return { action: 'add', server: q.selected }
    if (q.path === '/servers') return { action: 'list' }
    return known(q.remembered) ? { action: 'add', server: q.remembered } : { action: 'choose' }
  }
  if (!known(q.wanted)) return { action: 'unknown', server: q.wanted }
  if (q.selected !== null && q.selected !== q.wanted) return { action: 'reload' }
  return { action: 'proceed', server: q.wanted }
}

/** `?server=` as a single name; a repeated parameter counts by its first value. */
export function wantedServer(value: unknown): string | null {
  const first = Array.isArray(value) ? value[0] : value
  return typeof first === 'string' && first !== '' ? first : null
}

/** A same-site path with `server` in its query, unless it names one already. */
export function withServer(path: string, server: string | null): string {
  if (!server) return path
  const hashAt = path.indexOf('#')
  const hash = hashAt === -1 ? '' : path.slice(hashAt)
  const rest = hashAt === -1 ? path : path.slice(0, hashAt)
  const queryAt = rest.indexOf('?')
  const params = new URLSearchParams(queryAt === -1 ? '' : rest.slice(queryAt + 1))
  if (params.has('server')) return path
  params.set('server', server)
  return `${queryAt === -1 ? rest : rest.slice(0, queryAt)}?${params.toString()}${hash}`
}
