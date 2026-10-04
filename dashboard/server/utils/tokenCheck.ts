/**
 * What the agent's answer to `GET /server` says about a token somebody signs
 * in with. Pure, so it is unit-tested: the handler asks, this decides.
 */

/** The agent's error envelope, as far as it could be read. */
export interface Refusal {
  code: string
  message: string
  details: Record<string, unknown>
}

export type TokenVerdict
  = | { accepted: true }
    | { accepted: false, status: number, code: string, message: string, details: Record<string, unknown> }

export function tokenVerdict(status: number, refusal: Refusal | null, agentUrl: string): TokenVerdict {
  if (status === 200) return { accepted: true }
  if (status === 401) {
    // A token that has expired is told so, in the agent's words: it is not a wrong token.
    if (refusal?.code === 'TOKEN_EXPIRED') return { accepted: false, status: 401, code: 'TOKEN_EXPIRED', message: refusal.message, details: refusal.details }
    return { accepted: false, status: 401, code: 'UNAUTHORIZED', message: 'The agent rejected this token', details: {} }
  }
  // The agent did not look at the token: too many failures from this address (the dashboard server's, as the agent sees it).
  if (status === 429) return { accepted: false, status: 429, code: 'RATE_LIMITED', message: 'Too many failed attempts from this address; try again in a minute.', details: {} }
  // Nor here: it refuses the dashboard server for the network it calls from, whatever the token.
  if (status === 403 && refusal?.code === 'APPLICATION_CALLER') return { accepted: false, status: 403, code: 'APPLICATION_CALLER', message: refusal.message, details: {} }
  // The agent checks the token before it asks Docker for the facts of the
  // server, so this answer is one to a token it accepted: Docker does not
  // answer, or the agent is shutting down. Signed in, the pages say which.
  if (status === 503 && refusal?.code === 'RUNTIME_UNAVAILABLE') return { accepted: true }
  return { accepted: false, status: 502, code: 'AGENT_UNREACHABLE', message: `${agentUrl} answered with status ${status}. Is it a Shipwick agent?`, details: { agent_url: agentUrl } }
}

/** The envelope out of a body that holds one; null for anything else. */
export function parseRefusal(text: string): Refusal | null {
  try {
    const error = (JSON.parse(text) as { error?: { code?: unknown, message?: unknown, details?: unknown } } | null)?.error
    if (typeof error?.code !== 'string' || typeof error.message !== 'string') return null
    return { code: error.code, message: error.message, details: typeof error.details === 'object' && error.details !== null ? error.details as Record<string, unknown> : {} }
  }
  catch {
    return null
  }
}
