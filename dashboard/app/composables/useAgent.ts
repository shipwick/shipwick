import type { ApiEnvelope } from '~/types/api'
import { AgentError, errorFromResponse, isAbortError } from '~/utils/agentError'
import { REQUEST_HEADERS } from '~/composables/useSession'

type QueryValue = string | number | boolean | null | undefined

export interface AgentRequestOptions {
  query?: Record<string, QueryValue>
  body?: unknown
  signal?: AbortSignal
}

/** Base path of the server-side proxy; the agent's /api/v1 is mounted below it. */
const PROXY_BASE = '/api/agent'

export function agentUrl(path: string, query?: Record<string, QueryValue>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null && value !== '') params.set(key, String(value))
  }
  const search = params.toString()
  return `${PROXY_BASE}${path}${search ? `?${search}` : ''}`
}

/**
 * The only way the app talks to the agent: through the dashboard's own
 * /api/agent proxy, always with the CSRF header, never with a token.
 *
 * Must be called during component setup (it captures the session and router).
 */
export function useAgent() {
  const session = useSession()

  async function send(method: 'GET' | 'POST' | 'DELETE', path: string, options: AgentRequestOptions = {}): Promise<Response> {
    const headers: Record<string, string> = { ...REQUEST_HEADERS }
    let body: string | undefined
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(options.body)
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
      session.expire()
      throw await errorFromResponse(response)
    }
    if (!response.ok) throw await errorFromResponse(response)
    return response
  }

  async function request<T>(method: 'GET' | 'POST' | 'DELETE', path: string, options?: AgentRequestOptions): Promise<T> {
    const response = await send(method, path, options)
    if (response.status === 204) return undefined as T
    try {
      const envelope = await response.json() as ApiEnvelope<T>
      if (typeof envelope !== 'object' || envelope === null || !('data' in envelope)) {
        throw new AgentError(response.status, 'BAD_RESPONSE', 'The agent sent a response without a data envelope')
      }
      return envelope.data
    }
    catch (error) {
      if (isAbortError(error)) throw error
      if (error instanceof AgentError) throw error
      throw new AgentError(response.status, 'BAD_RESPONSE', 'The agent sent a response that is not valid JSON')
    }
  }

  return {
    get: <T>(path: string, options?: AgentRequestOptions) => request<T>('GET', path, options),
    post: <T>(path: string, options?: AgentRequestOptions) => request<T>('POST', path, options),
    del: (path: string, options?: AgentRequestOptions) => request<void>('DELETE', path, options),
    /** For NDJSON: resolves with the raw response once headers arrive; errors are thrown as for any request. */
    stream: (path: string, options?: AgentRequestOptions) => send('GET', path, options),
  }
}

export type Agent = ReturnType<typeof useAgent>
