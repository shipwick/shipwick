import type { IncomingMessage } from 'node:http'
import type { H3Event } from 'h3'
import type { ConfiguredAgent } from './agents'
import type { SignInConfig, SignInFailure } from './signin'
import { SIGNIN_RESULT_COOKIE, parseSignInConfig } from './signin'

/** An answer of the agent read whole: these are small JSON documents, and more than 64 KB is not one. */
export async function readAgentJson(response: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  let size = 0
  for await (const chunk of response) {
    size += (chunk as Buffer).length
    if (size > 64 * 1024) {
      response.destroy()
      return null
    }
    chunks.push(chunk as Buffer)
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString('utf8'))
  }
  catch {
    return null
  }
}

/**
 * Whether people can sign in at this agent, and how: its GET /auth, which
 * takes no token. `problem` is the agent's sentence when a provider is
 * configured and cannot be used. An agent before 0.6, or one that does not
 * answer, has no sign-in to offer: tokens work as they always did.
 */
export async function fetchSignInConfig(agent: ConfiguredAgent): Promise<{ config: SignInConfig | null, problem: string }> {
  let response: IncomingMessage
  try {
    response = await agentRequest({ agent, method: 'GET', path: `${AGENT_API_PREFIX}auth`, headers: { accept: 'application/json' }, headersTimeoutMs: 15_000 })
  }
  catch {
    return { config: null, problem: '' }
  }
  const body = await readAgentJson(response) as { data?: unknown, error?: { code?: unknown, message?: unknown } } | null
  if (response.statusCode === 200) return { config: parseSignInConfig(body?.data), problem: '' }
  if (response.statusCode === 502 && body?.error?.code === 'SIGN_IN_UNAVAILABLE' && typeof body.error.message === 'string') {
    return { config: null, problem: body.error.message.slice(0, 1000) }
  }
  return { config: null, problem: '' }
}

/** The sign-in page of a server: with several, the address names it. */
export function loginPath(agent: ConfiguredAgent | null): string {
  return agent && configuredAgents().length > 1 ? `/login?server=${encodeURIComponent(agent.name)}` : '/login'
}

/** Leaves the reason for the sign-in page to read once, and sends the browser there. */
export function failSignIn(event: H3Event, agent: ConfiguredAgent | null, failure: SignInFailure) {
  setCookie(event, SIGNIN_RESULT_COOKIE, JSON.stringify(failure), {
    ...sessionCookieOptions(event),
    path: '/api/auth',
    maxAge: 120,
  })
  return sendRedirect(event, loginPath(agent), 302)
}
