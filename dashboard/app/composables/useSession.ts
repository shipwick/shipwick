import type { AgentError } from '~/utils/agentError'
import { errorFromResponse, toAgentError } from '~/utils/agentError'
import { sessionEndedMessage } from '~/utils/access'
import type { KnownServer } from '~/utils/servers'
import { agentBase, withServer } from '~/utils/servers'

/** Sent with every request; the server rejects state-changing requests without it (CSRF). */
export const REQUEST_HEADERS: Readonly<Record<string, string>> = {
  'X-Shipwick-Request': '1',
  'Accept': 'application/json',
}

const REMEMBERED_KEY = 'shipwick-server'

/**
 * What the browser knows of its sign-ins. The app is client-rendered, so this
 * lives for as long as the page: one list of servers and, per tab, the one it
 * is on. The tokens are in httpOnly cookies that JavaScript cannot read.
 */
const state = reactive({
  /** null = not asked yet. */
  servers: null as KnownServer[] | null,
  selected: null as string | null,
  /** Why the dashboard's own server could not say which servers it has. */
  problem: '',
  /** Why the last session ended, in the agent's words, for the sign-in page. */
  ended: '',
})

/** The proxy path for the server this tab is on. Plain function: download links and uploads build addresses outside a component. */
export function currentAgentBase(): string {
  return agentBase(state.selected, (state.servers?.length ?? 0) > 1)
}

/**
 * The browser's view of the session: which servers the dashboard has, which of
 * them this browser is signed in to, and which one this tab shows.
 */
export function useSession() {
  const router = useRouter()

  const servers = computed<KnownServer[]>(() => state.servers ?? [])
  /** Several servers are configured: addresses name theirs and the sidebar offers the others. */
  const multiple = computed(() => servers.value.length > 1)
  const selected = computed(() => state.selected)
  const authenticated = computed<boolean | null>(() => {
    if (state.servers === null) return null
    return servers.value.find(s => s.name === state.selected)?.authenticated ?? false
  })

  async function check(): Promise<KnownServer[]> {
    if (state.servers !== null) return state.servers
    try {
      const response = await fetch('/api/session', { headers: REQUEST_HEADERS, credentials: 'same-origin' })
      if (!response.ok) throw await errorFromResponse(response)
      const body = await response.json() as { servers?: unknown }
      state.servers = Array.isArray(body.servers)
        ? body.servers.filter((s): s is KnownServer => typeof s === 'object' && s !== null && typeof (s as KnownServer).name === 'string')
            .map(s => ({ name: s.name, authenticated: s.authenticated === true }))
        : []
      state.problem = ''
    }
    catch (cause) {
      // The dashboard server is unreachable or misconfigured; the sign-in page says which.
      state.servers = []
      state.problem = toAgentError(cause).message
    }
    if (state.servers.length === 1) state.selected = state.servers[0]!.name
    return state.servers
  }

  function isAuthenticated(server: string | null): boolean {
    return servers.value.find(s => s.name === server)?.authenticated ?? false
  }

  function mark(server: string | null, value: boolean) {
    state.servers = servers.value.map(s => (s.name === server ? { ...s, authenticated: value } : s))
  }

  /** Makes `server` the one this tab is on, and the one an address without a server opens next time. */
  function select(server: string | null) {
    state.selected = server
    if (!server || !multiple.value) return
    try {
      localStorage.setItem(REMEMBERED_KEY, server)
    }
    catch {
      // Storage is unavailable: an address without a server asks instead.
    }
  }

  function remembered(): string | null {
    try {
      return localStorage.getItem(REMEMBERED_KEY)
    }
    catch {
      return null
    }
  }

  /** Throws an AgentError when the token is rejected or the agent cannot be reached. */
  async function login(token: string, server: string | null = state.selected): Promise<void> {
    let response: Response
    try {
      response = await fetch('/api/session', {
        method: 'POST',
        headers: { ...REQUEST_HEADERS, 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(multiple.value ? { token, server } : { token }),
      })
    }
    catch (error) {
      throw toAgentError(error)
    }
    if (!response.ok) throw await errorFromResponse(response)
    state.ended = ''
    mark(server, true)
  }

  /** Signs out of one server; the sign-ins for the others stay. */
  async function signOut(server: string | null): Promise<void> {
    try {
      await fetch(multiple.value && server ? `/api/session?server=${encodeURIComponent(server)}` : '/api/session', { method: 'DELETE', headers: REQUEST_HEADERS, credentials: 'same-origin' })
    }
    catch {
      // Even if the request failed, leave the authenticated area.
    }
    mark(server, false)
  }

  /**
   * Signs out of the server this tab is on and leaves it: to the sign-in page,
   * or to the list when there are other servers. A person's session is ended
   * on the agent first; a token has nothing there to end.
   */
  async function logout(endSession = false): Promise<void> {
    if (endSession) {
      try {
        await fetch(`${currentAgentBase()}/auth/session`, { method: 'DELETE', headers: REQUEST_HEADERS, credentials: 'same-origin' })
      }
      catch {
        // The cookie is cleared whatever the agent answered; the session ends by itself within hours.
      }
    }
    await signOut(state.selected)
    // A fresh load: nothing of the server that was just left stays on screen.
    if (multiple.value) window.location.assign('/servers')
    else await router.replace('/login')
  }

  /**
   * The agent answered 401: the proxy has already cleared the cookie. A token
   * that expired, or a session that ended, is told why on the sign-in page.
   */
  function expire(error?: AgentError): void {
    const current = router.currentRoute.value
    if (!isAuthenticated(state.selected) && current.path === '/login') return
    mark(state.selected, false)
    state.ended = sessionEndedMessage(error)
    const redirect = current.path === '/login' ? undefined : current.fullPath
    void router.replace({
      path: '/login',
      query: { ...(multiple.value && state.selected ? { server: state.selected } : {}), ...(redirect ? { redirect } : {}), expired: '1' },
    })
  }

  return {
    servers,
    multiple,
    selected,
    authenticated,
    problem: computed(() => state.problem),
    ended: computed(() => state.ended),
    check,
    isAuthenticated,
    select,
    remembered,
    login,
    signOut,
    logout,
    expire,
    /** A path on the server this tab is on: for a plain link that must survive a new tab. */
    here: (path: string) => (multiple.value ? withServer(path, state.selected) : path),
  }
}
