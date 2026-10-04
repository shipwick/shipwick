import type { IncomingMessage } from 'node:http'
import type { Refusal } from '../utils/tokenCheck'

/**
 * POST /api/session {token, server?} → verifies the token against the agent
 * and, if it is accepted, stores it in an httpOnly cookie of that server's.
 * `server` may be left out when one server is configured. The token is never
 * echoed back and never logged.
 */
export default defineEventHandler(async (event) => {
  try {
    assertSameOriginRequest(event)

    const body = await readBody<{ token?: unknown, server?: unknown } | null>(event).catch(() => null)
    const agent = agentFor(body && typeof body.server === 'string' ? body.server : null)
    const token = body && typeof body.token === 'string' ? body.token.trim() : ''
    if (!isPlausibleToken(token)) {
      throw new AgentProxyError(400, 'INVALID_REQUEST', 'Enter the agent token')
    }

    const response = await agentRequest({
      agent,
      method: 'GET',
      path: `${AGENT_API_PREFIX}server`,
      token,
      headers: { accept: 'application/json' },
    })

    // What the agent says when it does not answer with the server's facts decides; the facts themselves are not needed.
    let refusal: Refusal | null = null
    if (response.statusCode === 200) response.resume()
    else refusal = await readRefusal(response)

    const verdict = tokenVerdict(response.statusCode ?? 0, refusal, agent.url)
    if (!verdict.accepted) {
      if (verdict.status === 429) {
        const retryAfter = Number(response.headers['retry-after'])
        setResponseHeader(event, 'retry-after', Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : 60)
      }
      throw new AgentProxyError(verdict.status, verdict.code, verdict.message, verdict.details)
    }

    setCookie(event, sessionCookieName(agent), token, { ...sessionCookieOptions(event), maxAge: SESSION_MAX_AGE_SECONDS })
    setResponseHeader(event, 'cache-control', 'no-store')
    return { authenticated: true, server: agent.name }
  }
  catch (error) {
    return sendProxyError(event, error)
  }
})

/** The agent's error envelope out of an answer that is not the server's facts, or null. At most 8 KB are read. */
async function readRefusal(response: IncomingMessage): Promise<Refusal | null> {
  const chunks: Buffer[] = []
  let size = 0
  try {
    for await (const chunk of response) {
      size += (chunk as Buffer).length
      if (size > 8 * 1024) {
        response.destroy()
        return null
      }
      chunks.push(chunk as Buffer)
    }
    return parseRefusal(Buffer.concat(chunks).toString('utf8'))
  }
  catch {
    return null
  }
}
