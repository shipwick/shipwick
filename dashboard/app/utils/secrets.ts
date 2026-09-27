import type { Secret } from '~/types/api'

/**
 * The Secrets page's rules, mirrored from the agent (pkg/api/secrets.go) so a
 * bad name is refused before the request and the wording is unit-tested. The
 * agent checks everything again.
 */

/** An environment variable name: letters, digits and underscores, not starting with a digit. */
const NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/
const MAX_NAME_LENGTH = 64
/** The agent's limit on a value; the body it sits in may be a little larger. */
export const MAX_VALUE_BYTES = 64 * 1024

/** Why a name cannot be a secret's, or "" when it can. */
export function secretNameProblem(name: string): string {
  if (name === '') return ''
  if (name.length > MAX_NAME_LENGTH) return `At most ${MAX_NAME_LENGTH} characters.`
  if (!NAME_PATTERN.test(name)) return 'Letters, digits and underscores, not starting with a digit: the name of an environment variable, e.g. DATABASE_PASSWORD.'
  return ''
}

/** Why a value cannot be stored, or "" when it can. Never quotes the value. */
export function secretValueProblem(value: string): string {
  if (value === '') return ''
  if (value.includes('\0')) return 'The value must not contain a NUL byte.'
  if (new TextEncoder().encode(value).length > MAX_VALUE_BYTES) return 'The value must be at most 64 KB.'
  return ''
}

/** The stored secret a name would replace, if any: PUT creates and replaces alike, so the form must say which. */
export function replaces(name: string, stored: readonly Pick<Secret, 'name'>[]): boolean {
  return name !== '' && stored.some(s => s.name === name)
}

/** What removing a secret does, for the confirmation. */
export function secretRemovalConsequence(name: string): string {
  return `Deployments already made keep their value. The next deploy whose env refers to \${${name}} is refused until the secret is stored again.`
}
