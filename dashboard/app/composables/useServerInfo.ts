import type { InjectionKey } from 'vue'
import type { Server } from '~/types/api'

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
