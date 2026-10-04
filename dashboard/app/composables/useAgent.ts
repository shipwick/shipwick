import type { ApiEnvelope } from '~/types/api'
import { AgentError, errorFromResponse, isAbortError } from '~/utils/agentError'
import { REQUEST_HEADERS, currentAgentBase } from '~/composables/useSession'

type QueryValue = string | number | boolean | null | undefined

export interface AgentRequestOptions {
  query?: Record<string, QueryValue>
  body?: unknown
  /** A body sent as it is, with its own content type: a deploy.yaml is YAML, not JSON. */
  text?: { content: string, type: string }
  signal?: AbortSignal
}

/**
 * An address below the server-side proxy, where the agent's /api/v1 is mounted:
 * /api/agent, or /api/servers/<name>/agent when the dashboard has several servers.
 */
export function agentUrl(path: string, query?: Record<string, QueryValue>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null && value !== '') params.set(key, String(value))
  }
  const search = params.toString()
  return `${currentAgentBase()}${path}${search ? `?${search}` : ''}`
}

/**
 * The only way the app talks to the agent: through the dashboard's own
 * /api/agent proxy, always with the CSRF header, never with a token.
 *
 * Must be called during component setup (it captures the session and router).
 */
export function useAgent() {
  const session = useSession()

  async function send(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, options: AgentRequestOptions = {}): Promise<Response> {
    const headers: Record<string, string> = { ...REQUEST_HEADERS }
    let body: string | undefined
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(options.body)
    }
    else if (options.text !== undefined) {
      headers['Content-Type'] = options.text.type
      body = options.text.content
    }

    let response: Response
    try {
      response = await fetch(agentUrl(path, options.query), {
        method,
        headers,
        body,
        signal: options.signal,
        credentials: 'same-origin',
        cache: 'no-store',
      })
    }
    catch (error) {
      if (isAbortError(error)) throw error
      throw new AgentError(0, 'NETWORK', 'Cannot reach the dashboard server')
    }

    if (response.status === 401) {
      const error = await errorFromResponse(response)
      session.expire(error)
      throw error
    }
    if (!response.ok) throw await errorFromResponse(response)
    return response
  }

  /** The answer's envelope: `data`, and whatever the agent says next to it. */
  async function envelope<E extends ApiEnvelope<unknown>>(response: Response): Promise<E> {
    try {
      const body = await response.json() as E
      if (typeof body !== 'object' || body === null || !('data' in body)) {
        throw new AgentError(response.status, 'BAD_RESPONSE', 'The agent sent a response without a data envelope')
      }
      return body
    }
    catch (error) {
      if (isAbortError(error)) throw error
      if (error instanceof AgentError) throw error
      throw new AgentError(response.status, 'BAD_RESPONSE', 'The agent sent a response that is not valid JSON')
    }
  }

  async function request<T>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, options?: AgentRequestOptions): Promise<T> {
    const response = await send(method, path, options)
    if (response.status === 204) return undefined as T
    return (await envelope<ApiEnvelope<T>>(response)).data
  }

  return {
    get: <T>(path: string, options?: AgentRequestOptions) => request<T>('GET', path, options),
    /** The whole answer, for the one that says something next to `data`: GET /audit and its `more`. */
    getEnvelope: async <E extends ApiEnvelope<unknown>>(path: string, options?: AgentRequestOptions) => envelope<E>(await send('GET', path, options)),
    post: <T>(path: string, options?: AgentRequestOptions) => request<T>('POST', path, options),
    /**
     * For a JSON body that creates or replaces: a secret or a registry
     * credential (204), a certificate (answered with what was stored).
     * Archives are not uploaded from the browser.
     */
    put: <T = void>(path: string, options?: AgentRequestOptions) => request<T>('PUT', path, options),
    del: (path: string, options?: AgentRequestOptions) => request<void>('DELETE', path, options),
    /** For NDJSON: resolves with the raw response once headers arrive; errors are thrown as for any request. */
    stream: (path: string, options?: AgentRequestOptions) => send('GET', path, options),
  }
}

export type Agent = ReturnType<typeof useAgent>
