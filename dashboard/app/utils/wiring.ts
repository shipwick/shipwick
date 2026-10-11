import type { ProxyStatus, Server } from '~/types/api'

/**
 * What the server page says about the server's networking that no single
 * request shows: a proxy that is not on all of its networks, and a kernel
 * whose table of neighbours the containers are filling. The words are
 * `shipwick doctor`'s (cli/internal/commands/wiring.go). Pure, so they are
 * unit-tested. An agent before 0.9 says nothing of either, and then nothing
 * is said here.
 */

/** The command that puts the proxy back on every network of the installation. */
export const RECREATE_PROXY_COMMAND = 'cd /opt/shipwick && docker compose up -d --force-recreate caddy'

/** The red line under the proxy when it is not on all of its networks; "" when it is, or the agent did not say. */
export function proxyNetworksProblem(proxy: Pick<ProxyStatus, 'networks_missing'> | null | undefined): string {
  const missing = proxy?.networks_missing ?? []
  if (missing.length === 0) return ''
  return `The proxy is not on the ${missing.join(' and ')} ${missing.length === 1 ? 'network' : 'networks'}: it serves some applications and not others, or cannot reach the agent. On the server: ${RECREATE_PROXY_COMMAND}`
}

export interface NeighbourTableDescription {
  /** "1,024 entries": the table's size. */
  size: string
  /** Why it matters and what to do, when the containers approach it; "" while there is room. */
  warning: string
}

/**
 * The kernel's table of neighbours next to the container count, when the
 * agent could read it. Every container's addresses are in it, and so are the
 * entries each container keeps of its peers; when it is full, containers stop
 * reaching each other. The warning comes from a quarter of the table on, as
 * doctor's does, counting the proxy as one more container. Not a fraction of
 * containers over entries: each container takes several.
 */
export function describeNeighbourTable(server: Pick<Server, 'containers' | 'kernel_limits'>): NeighbourTableDescription | null {
  const table = server.kernel_limits?.neighbour_table
  if (table === undefined || table <= 0) return null
  const containers = server.containers + 1
  const warning = containers * 4 >= table
    ? `The server runs ${containers} containers; the kernel's table of neighbours holds ${table.toLocaleString('en-US')} entries and each container takes several: raise net.ipv4.neigh.default.gc_thresh3 (and gc_thresh1/2) in /etc/sysctl.d before it fills.`
    : ''
  return { size: `${table.toLocaleString('en-US')} entries`, warning }
}
