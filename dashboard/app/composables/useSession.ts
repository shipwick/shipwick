import { errorFromResponse, toAgentError } from '~/utils/agentError'

/** Sent with every request; the server rejects state-changing requests without it (CSRF). */
export const REQUEST_HEADERS: Readonly<Record<string, string>> = {
  'X-Shipwick-Request': '1',
  'Accept': 'application/json',
}

/**
 * The browser's view of the session. The token itself lives in an httpOnly
 * cookie that JavaScript cannot read; all this knows is whether one exists.
 */
export function useSession() {
  // null = not checked yet
  const authenticated = useState<boolean | null>('session:authenticated', () => null)
  const router = useRouter()

  async function check(): Promise<boolean> {
    if (authenticated.value !== null) return authenticated.value
    try {
      const response = await fetch('/api/session', { headers: REQUEST_HEADERS, credentials: 'same-origin' })
      const body = await response.json() as { authenticated?: unknown }
      authenticated.value = body.authenticated === true
    }
    catch {
      // The dashboard server itself is unreachable; the login page will say so on submit.
      authenticated.value = false
    }
    return authenticated.value
  }

  /** Throws an AgentError when the token is rejected or the agent cannot be reached. */
  async function login(token: string): Promise<void> {
    let response: Response
    try {
      response = await fetch('/api/session', {
        method: 'POST',
        headers: { ...REQUEST_HEADERS, 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ token }),
      })
    }
    catch (error) {
      throw toAgentError(error)
    }
    if (!response.ok) throw await errorFromResponse(response)
    authenticated.value = true
  }

  async function logout(): Promise<void> {
    try {
      await fetch('/api/session', { method: 'DELETE', headers: REQUEST_HEADERS, credentials: 'same-origin' })
    }
    catch {
      // Even if the request failed, leave the authenticated area.
    }
    authenticated.value = false
    await router.replace('/login')
  }

  /** The agent answered 401: the proxy has already cleared the cookie. */
  function expire(): void {
    if (authenticated.value === false && router.currentRoute.value.path === '/login') return
    authenticated.value = false
    const current = router.currentRoute.value
    const redirect = current.path === '/login' ? undefined : current.fullPath
    void router.replace({ path: '/login', query: { ...(redirect ? { redirect } : {}), expired: '1' } })
  }

  return { authenticated, check, login, logout, expire }
}
