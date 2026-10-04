import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'

/**
 * Signing in through an OpenID Connect provider, the dashboard server's half:
 * it starts the sign-in, keeps `state`, `nonce` and the PKCE verifier in a
 * cookie for the ten minutes the sign-in may take, and hands the provider's
 * code to the agent. The agent holds the client secret, redeems the code and
 * verifies the ID token; the dashboard never sees either. Pure, so every rule
 * here is unit-tested; the routes are server/routes/auth/.
 */

/** Holds what the callback must match; sent only to /auth, and on the navigation back from the provider (SameSite=Lax). */
export const SIGNIN_COOKIE = 'shipwick_signin'
export const SIGNIN_MAX_AGE_SECONDS = 600

/** Why a sign-in did not complete, for the sign-in page to read once: never in an address, where anyone could write it. */
export const SIGNIN_RESULT_COOKIE = 'shipwick_signin_result'

/** What the agent's GET /auth says when a provider is configured. */
export interface SignInConfig {
  issuer: string
  authorization_endpoint: string
  client_id: string
  scopes: string[]
  redirect_uri: string
}

export interface PendingSignIn {
  state: string
  nonce: string
  verifier: string
  /** The server the sign-in is for: the provider's redirect cannot carry it. */
  server: string
}

/** 32 random bytes as base64url without padding: 43 characters, what the agent expects of a nonce and a verifier. */
export function randomValue(): string {
  return randomBytes(32).toString('base64url')
}

/** The S256 challenge of a PKCE verifier. */
export function codeChallenge(verifier: string): string {
  return createHash('sha256').update(verifier).digest('base64url')
}

/** The agent's answer as a configuration to sign in with; null for "not configured" and for anything that is not one. */
export function parseSignInConfig(data: unknown): SignInConfig | null {
  if (typeof data !== 'object' || data === null) return null
  const d = data as Record<string, unknown>
  if (d.configured !== true) return null
  const strings = ['issuer', 'authorization_endpoint', 'client_id', 'redirect_uri'] as const
  if (strings.some(key => typeof d[key] !== 'string' || d[key] === '')) return null
  if (!Array.isArray(d.scopes) || d.scopes.some(s => typeof s !== 'string')) return null
  // The browser is sent there: it must be a web address, whatever the agent was told.
  if (!isWebUrl(d.authorization_endpoint as string)) return null
  return {
    issuer: d.issuer as string,
    authorization_endpoint: d.authorization_endpoint as string,
    client_id: d.client_id as string,
    scopes: d.scopes as string[],
    redirect_uri: d.redirect_uri as string,
  }
}

function isWebUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || url.protocol === 'http:'
  }
  catch {
    return false
  }
}

/** Where the browser is sent to sign in. The endpoint may carry a query of its own, which is kept. */
export function authorizationUrl(config: SignInConfig, pending: Pick<PendingSignIn, 'state' | 'nonce' | 'verifier'>): string {
  const url = new URL(config.authorization_endpoint)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('client_id', config.client_id)
  url.searchParams.set('redirect_uri', config.redirect_uri)
  url.searchParams.set('scope', config.scopes.join(' '))
  url.searchParams.set('state', pending.state)
  url.searchParams.set('nonce', pending.nonce)
  url.searchParams.set('code_challenge', codeChallenge(pending.verifier))
  url.searchParams.set('code_challenge_method', 'S256')
  return url.toString()
}

const VALUE = /^[A-Za-z0-9_-]{43}$/
const SERVER = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

/** The cookie's content, if it is what this server wrote; null for a missing or altered one. */
export function parsePending(raw: string | undefined | null): PendingSignIn | null {
  if (!raw) return null
  try {
    const value = JSON.parse(raw) as Record<string, unknown>
    const { state, nonce, verifier, server } = value
    if (typeof state !== 'string' || typeof nonce !== 'string' || typeof verifier !== 'string' || typeof server !== 'string') return null
    if (!VALUE.test(state) || !VALUE.test(nonce) || !VALUE.test(verifier) || !SERVER.test(server)) return null
    return { state, nonce, verifier, server }
  }
  catch {
    return null
  }
}

/** Compares in constant time; false for values of different length. */
export function sameValue(a: string, b: string): boolean {
  const left = Buffer.from(a)
  const right = Buffer.from(b)
  return left.length === right.length && timingSafeEqual(left, right)
}

/** What the sign-in page shows when a sign-in did not complete. */
export interface SignInFailure {
  title: string
  /** The agent's own sentence where it says more than the title; "" otherwise. */
  message: string
  /** The message is written to be forwarded to an admin: shown so that it is easy to copy. */
  copy: boolean
}

export const FAILURE_NOT_STARTED_HERE: SignInFailure = {
  title: 'This sign-in did not start in this browser, or took longer than ten minutes. Sign in again.',
  message: '',
  copy: false,
}

/** The provider sent the browser back with an error instead of a code. Only the error's name is shown, as text. */
export function providerRefusal(error: string): SignInFailure {
  const name = /^[\w.-]{1,64}$/.test(error) ? error : 'error'
  return { title: `The provider did not sign you in (${name}).`, message: '', copy: false }
}

/** The sentence for an answer of the agent's POST /auth/exchange that is not a session. */
export function describeFailure(status: number, code: string, message: string, details: Record<string, unknown> = {}): SignInFailure {
  const said = message.slice(0, 1000)
  if (code === 'ACCESS_NOT_GRANTED') return { title: 'You signed in, and this server has no role for you yet.', message: said, copy: true }
  if (code === 'SIGN_IN_FAILED') {
    const reason = details.reason
    if (reason === 'invalid_id_token') return { title: 'The agent did not accept the provider\'s answer.', message: said, copy: false }
    if (reason === 'email_missing' || reason === 'email_not_verified') return { title: 'The provider\'s account cannot be used here.', message: said, copy: false }
    // The account has no name the agent can know it by: something for whoever runs the agent, so easy to pass on.
    if (reason === 'name_missing') return { title: 'The provider did not say who you are in a way this server can use.', message: said, copy: true }
    if (reason === 'tenant_not_allowed') return { title: 'Your account belongs to an organization that may not sign in to this server.', message: said, copy: true }
    return { title: 'That sign-in could not be completed. Sign in again.', message: '', copy: false }
  }
  if (code === 'SIGN_IN_NOT_CONFIGURED') return { title: 'This server takes API tokens only.', message: '', copy: false }
  if (code === 'SIGN_IN_UNAVAILABLE') return { title: 'The sign-in provider cannot be used right now.', message: said, copy: false }
  if (status === 429 || code === 'RATE_LIMITED') return { title: 'Too many failed attempts from this address; try again in a minute.', message: '', copy: false }
  if (code === 'INVALID_REQUEST') return { title: 'The dashboard and the agent disagree about where this dashboard lives.', message: said, copy: false }
  if (code === 'AGENT_UNREACHABLE' || code === 'AGENT_TIMEOUT') return { title: 'The agent could not be reached to complete the sign-in.', message: '', copy: false }
  return { title: 'The sign-in could not be completed.', message: said, copy: false }
}

/** A failure as the result cookie carries it, and back; null for anything else. */
export function parseFailure(raw: string | undefined | null): SignInFailure | null {
  if (!raw) return null
  try {
    const value = JSON.parse(raw) as Record<string, unknown>
    if (typeof value.title !== 'string' || typeof value.message !== 'string') return null
    return { title: value.title.slice(0, 300), message: value.message.slice(0, 1000), copy: value.copy === true }
  }
  catch {
    return null
  }
}

/**
 * How long the session cookie lives: until the agent says the session ends,
 * and never longer than `longest` (what a token's cookie gets). At least a
 * minute, so that a clock a little ahead does not drop the cookie at once.
 */
export function sessionMaxAge(expiresAt: unknown, longest: number, now: number = Date.now()): number {
  const end = typeof expiresAt === 'string' ? Date.parse(expiresAt) : Number.NaN
  if (Number.isNaN(end)) return longest
  return Math.min(longest, Math.max(60, Math.floor((end - now) / 1000)))
}
