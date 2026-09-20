import type { ApiErrorCode } from '~/types/api'

/** Codes produced on the browser side, when no envelope could be obtained at all. */
export type ClientErrorCode = 'NETWORK' | 'BAD_RESPONSE'

/**
 * Every failure of a call to the agent, normalized: an error envelope from the
 * agent, one from the dashboard's proxy, or a failure to reach the dashboard.
 */
export class AgentError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | ClientErrorCode | string
  readonly details: Record<string, unknown>

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
    super(message)
    this.name = 'AgentError'
    this.status = status
    this.code = code
    this.details = details
  }

  /** The dashboard could not reach the agent at all (as opposed to the agent refusing something). */
  get unreachable(): boolean {
    return this.code === 'AGENT_UNREACHABLE' || this.code === 'AGENT_TIMEOUT'
  }

  get notFound(): boolean {
    return this.status === 404
  }

  /** The URL the proxy tried, when it told us. */
  get agentUrl(): string | null {
    const url = this.details.agent_url
    return typeof url === 'string' ? url : null
  }

  /** Field-level problems of an INVALID_CONFIG response. */
  get fields(): { field: string, message: string, expected?: string }[] {
    const fields = this.details.fields
    if (!Array.isArray(fields)) return []
    return fields.filter((f): f is { field: string, message: string, expected?: string } =>
      typeof f === 'object' && f !== null && typeof (f as { field?: unknown }).field === 'string')
  }
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === 'AbortError'
    : typeof error === 'object' && error !== null && (error as { name?: unknown }).name === 'AbortError'
}

export function toAgentError(error: unknown): AgentError {
  if (error instanceof AgentError) return error
  const message = error instanceof Error ? error.message : String(error)
  return new AgentError(0, 'NETWORK', message || 'Request failed')
}

/** Reads an error envelope out of a non-2xx response, tolerating non-JSON bodies. */
export async function errorFromResponse(response: Response): Promise<AgentError> {
  let text = ''
  try {
    text = await response.text()
  }
  catch {
    // fall through with an empty body
  }
  try {
    const body = JSON.parse(text) as { error?: { code?: unknown, message?: unknown, details?: unknown } }
    if (body && typeof body === 'object' && body.error && typeof body.error === 'object') {
      const { code, message, details } = body.error
      return new AgentError(
        response.status,
        typeof code === 'string' ? code : 'BAD_RESPONSE',
        typeof message === 'string' && message !== '' ? message : `Request failed with status ${response.status}`,
        typeof details === 'object' && details !== null ? details as Record<string, unknown> : {},
      )
    }
  }
  catch {
    // not JSON
  }
  return new AgentError(response.status, 'BAD_RESPONSE', `Request failed with status ${response.status}`)
}
