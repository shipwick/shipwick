import type { Role } from '~/types/api'
import type { AgentError } from '~/utils/agentError'

/** Ascending: each role includes the ones before it. The agent's own order (pkg/api/tokens.go). */
export const ROLE_ORDER: readonly Role[] = ['read', 'deploy', 'admin']

export function isRole(value: unknown): value is Role {
  return typeof value === 'string' && (ROLE_ORDER as readonly string[]).includes(value)
}

/** Whether a token with `have` may use an endpoint that requires `required`. */
export function roleCovers(have: Role, required: Role): boolean {
  return ROLE_ORDER.indexOf(have) >= ROLE_ORDER.indexOf(required)
}

/** What each role adds, for the token form and the tooltips. */
export const ROLE_DESCRIPTIONS: Record<Role, string> = {
  read: 'See everything: applications, deployments, logs, events, metrics',
  deploy: 'And change what runs: deploy, redeploy, roll back, stop, start',
  admin: 'And everything else: delete applications, manage tokens, back up and restore volumes',
}

/**
 * The sentence for a 403, in the agent's own words: "This token has the read
 * role; deploying needs deploy or admin". Built from `details` so it is right
 * even when the agent's message changes; falls back to the message.
 */
export function forbiddenExplanation(error: Pick<AgentError, 'code' | 'message' | 'details'>): string {
  if (error.code !== 'FORBIDDEN') return ''
  const have = error.details.role
  const required = error.details.required
  if (!isRole(have) || !isRole(required)) return capitalize(error.message)
  return `This token has the ${have} role; ${needs(required)}`
}

/** Tooltip for an action the current token may not take: "Deploying needs the deploy or admin role". */
export function roleHint(required: Role): string {
  return capitalize(needs(required)).replace(/ needs (\S+)( or (\S+))?$/, (_m, a: string, _b, c?: string) =>
    ` needs the ${a}${c ? ` or ${c}` : ''} role`)
}

function needs(required: Role): string {
  if (required === 'deploy') return 'deploying needs deploy or admin'
  return `this needs ${required}`
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}
