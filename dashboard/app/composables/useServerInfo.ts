import type { InjectionKey } from 'vue'
import type { Role, Server, TokenIdentity } from '~/types/api'
import { roleCovers } from '~/utils/roles'

type ServerPolling = ReturnType<typeof usePolling<Server>>

const KEY: InjectionKey<ServerPolling> = Symbol('shipwick:server')

/**
 * GET /server is needed by the sidebar on every page and by two pages
 * themselves. The layout polls it once and shares it, so there is never more
 * than one request for it in flight.
 */
export function provideServerInfo(): ServerPolling {
  const agent = useAgent()
  const polling = usePolling<Server>(signal => agent.get<Server>('/server', { signal }), { interval: 15_000 })
  provide(KEY, polling)
  return polling
}

export function useServerInfo(): ServerPolling {
  const polling = inject(KEY, null)
  if (!polling) throw new Error('useServerInfo() must be used inside the default layout')
  return polling
}

/**
 * What the signed-in token may do, from `GET /server`'s `token`. This only
 * decides which controls to show and how to explain a refusal; the agent
 * enforces the roles on every request whatever the page does.
 *
 * Before the server has answered nothing is gated. An agent from before tokens
 * had roles omits `token` and knows a single token that may do everything.
 */
export function useAccess() {
  const server = useServerInfo()
  const token = computed<TokenIdentity | null>(() => {
    const s = server.data.value
    if (!s) return null
    return s.token ?? { name: 'root', role: 'admin' }
  })
  const role = computed<Role | null>(() => token.value?.role ?? null)

  function can(required: Role): boolean {
    return role.value === null || roleCovers(role.value, required)
  }

  /** Strictly known and sufficient: for controls that should not flash for a read-only token, such as the Tokens page. */
  function knownTo(required: Role): boolean {
    return role.value !== null && roleCovers(role.value, required)
  }

  return { token, role, can, knownTo }
}
