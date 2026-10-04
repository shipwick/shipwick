import type { ApiErrorCode } from '~/types/api'

/** Codes produced on the browser side, when no envelope could be obtained at all. */
export type ClientErrorCode = 'NETWORK' | 'BAD_RESPONSE'

/**
 * What a 429 means to the person in front of the screen. The agent counts
 * failed authentications per address, so this is about the address, not the
 * token that was just entered: it is never reported as a wrong token.
 */
export const RATE_LIMITED_MESSAGE = 'Too many failed attempts from this address; try again in a minute.'

/**
 * What a 403 APPLICATION_CALLER means here. The agent refuses whoever calls
 * from a network applications are on, without looking at the token, and its
 * own sentence is written for a container. The caller in this case is the
 * dashboard server, so this says what is wrong with where it runs: it is
 * never reported as a wrong token or a missing role.
 */
export const APPLICATION_CALLER_MESSAGE = 'The agent refuses this dashboard: it calls from a network applications are on. On the server, run the compose command again (handbook, Installation from a package).'

/** The agent counts failures over a minute, so that is the longest a refusal lasts. */
const RATE_LIMIT_WINDOW_MS = 60_000

/** Retry-After as whole seconds; null when the header is absent or not a number of seconds. */
export function parseRetryAfter(value: string | null | undefined): number | null {
  const text = value?.trim() ?? ''
  return /^\d+$/.test(text) ? Number(text) : null
}

/**
 * Every failure of a call to the agent, normalized: an error envelope from the
 * agent, one from the dashboard's proxy, or a failure to reach the dashboard.
 */
export class AgentError extends Error {
  readonly status: number
  readonly code: ApiErrorCode | ClientErrorCode | string
  readonly details: Record<string, unknown>
  /** Seconds from the response's Retry-After header; null when it carried none. */
  readonly retryAfter: number | null

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}, retryAfter: number | null = null) {
    super(message)
    this.name = 'AgentError'
    this.status = status
    this.code = code
    this.details = details
    this.retryAfter = retryAfter
  }

  /**
   * How long to leave the agent alone before asking again, in milliseconds:
   * what Retry-After said, or a minute for a rate limit that came without one.
   * Zero for every other failure, which is retried at the usual pace.
   */
  get retryAfterMs(): number {
    if (this.retryAfter !== null) return this.retryAfter * 1000
    return this.rateLimited ? RATE_LIMIT_WINDOW_MS : 0
  }

  /** The dashboard could not reach the agent at all (as opposed to the agent refusing something). */
  get unreachable(): boolean {
    return this.code === 'AGENT_UNREACHABLE' || this.code === 'AGENT_TIMEOUT'
  }

  get notFound(): boolean {
    return this.status === 404
  }

  /** The agent refused to look at the token: too many failures from this address within a minute. */
  get rateLimited(): boolean {
    return this.status === 429 || this.code === 'RATE_LIMITED'
  }

  /** The agent refused the dashboard server for the network it calls from; the token was not looked at. */
  get applicationCaller(): boolean {
    return this.code === 'APPLICATION_CALLER'
  }

  /**
   * The Docker daemon does not answer the agent. RUNTIME_UNAVAILABLE is also
   * what an agent that is shutting down answers, so the agent's sentence
   * decides: every request that needs Docker then fails with the same one.
   */
  get dockerSilent(): boolean {
    return this.code === 'RUNTIME_UNAVAILABLE' && this.message.startsWith('Docker does not answer')
  }

  /**
   * The message to show: the agent's, which for a silent Docker and a full
   * disk already says what to do. A rate limit and a refusal of the dashboard
   * itself are worded for the person instead of the client.
   */
  get displayMessage(): string {
    if (this.rateLimited) return RATE_LIMITED_MESSAGE
    return this.applicationCaller ? APPLICATION_CALLER_MESSAGE : this.message
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

/**
 * The heading over a failure that left a page or a panel with nothing to
 * show. `subject` is what failed to load, e.g. "applications"; it is named
 * only where the failure is about it, and not where everything that asks the
 * agent fails for the same reason.
 */
export function failureTitle(error: AgentError, subject: string): string {
  if (error.unreachable) return 'Agent unreachable'
  if (error.code === 'NETWORK') return 'Dashboard server unreachable'
  if (error.applicationCaller) return 'Refused by the agent'
  if (error.dockerSilent) return 'Docker does not answer'
  if (error.code === 'DISK_FULL') return 'The server\'s disk is full'
  if (error.notFound) return 'Not found'
  return `Could not load ${subject}`
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
  const retryAfter = parseRetryAfter(response.headers.get('retry-after'))
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
        retryAfter,
      )
    }
  }
  catch {
    // not JSON
  }
  return new AgentError(response.status, 'BAD_RESPONSE', `Request failed with status ${response.status}`, {}, retryAfter)
}

/**
 * When a poll may next be sent: after the usual interval, and not before the
 * time a refusal asked to be left alone until.
 */
export function nextPollDelay(intervalMs: number, notBefore: number, now: number = Date.now()): number {
  return Math.max(intervalMs, notBefore - now)
}
