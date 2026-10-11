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
/** An application's name, as the agent validates it: the scope of a secret that is not the server's. */
const APPLICATION_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** Why a name cannot be a secret's, or "" when it can. */
export function secretNameProblem(name: string): string {
  if (name === '') return ''
  if (name.length > MAX_NAME_LENGTH) return `At most ${MAX_NAME_LENGTH} characters.`
  if (!NAME_PATTERN.test(name)) return 'Letters, digits and underscores, not starting with a digit: the name of an environment variable, e.g. DATABASE_PASSWORD.'
  return ''
}

/**
 * Why a name cannot be the application a secret belongs to, or "" when it
 * can. The application need not exist: its secrets may be stored before its
 * first deployment, so only the shape of the name is checked.
 */
export function secretScopeProblem(application: string): string {
  if (application === '') return ''
  if (!APPLICATION_NAME.test(application)) return 'An application\'s name: lowercase letters, digits and dashes, e.g. my-api.'
  return ''
}

/** Why a value cannot be stored, or "" when it can. Never quotes the value. */
export function secretValueProblem(value: string): string {
  if (value === '') return ''
  if (value.includes('\0')) return 'The value must not contain a NUL byte.'
  if (new TextEncoder().encode(value).length > MAX_VALUE_BYTES) return 'The value must be at most 64 KB.'
  return ''
}

/**
 * The characters of a value that a URL cannot carry unescaped, named and
 * never quoted: a secret that goes into a connection string is read as part
 * of a URL, where `@`, `:`, `/`, `?`, `#`, `%` and whitespace mean something
 * and are not the password any more. The CLI warns the same way.
 */
export function urlUnsafeCharacters(value: string): string[] {
  const found: string[] = []
  for (const c of ['@', ':', '/', '?', '#', '%']) if (value.includes(c)) found.push(c)
  if (/\s/.test(value)) found.push('whitespace')
  return found
}

/** The hint under the value field for a value with such characters; "" for one without. */
export function urlCharacterHint(value: string): string {
  const found = urlUnsafeCharacters(value)
  if (found.length === 0) return ''
  return `The value has ${found.join(' ')}, which a URL cannot carry unescaped. If it goes into a connection string such as postgres://user:password@host, choose another; it is stored all the same.`
}

/** How a scope reads: "Server" for the server's, else the application's name. */
export function scopeLabel(application: string | undefined): string {
  return application ? application : 'Server'
}

/**
 * The stored secret a (scope, name) would replace, if any: PUT creates and
 * replaces alike, so the form must say which. A stored secret without
 * `application` is from an agent before 0.9, which keeps one list for the
 * whole server: it is the server's.
 */
export function replaces(name: string, stored: readonly Pick<Secret, 'name' | 'application'>[], application: string = ''): boolean {
  return name !== '' && stored.some(s => s.name === name && (s.application ?? '') === application)
}

/**
 * By name, the server's before the applications' of the same name, so a
 * server-wide secret and the applications' own sit together. The agent lists
 * them so already; a list from an older agent is sorted the same way.
 */
export function sortSecrets<T extends Pick<Secret, 'name' | 'application'>>(secrets: readonly T[]): T[] {
  return [...secrets].sort((a, b) => a.name.localeCompare(b.name) || (a.application ?? '').localeCompare(b.application ?? ''))
}

/**
 * Whether the list says what scope each secret has. An agent before 0.9 leaves
 * `application` out of every row: it keeps one list for the whole server, and
 * the page then offers no scope. An empty list says nothing either way.
 */
export function scopesKnown(secrets: readonly Pick<Secret, 'application'>[]): boolean | null {
  if (secrets.length === 0) return null
  return secrets.every(s => s.application !== undefined)
}

/** The applications that have secrets of their own, for the names the scope choice offers. */
export function applicationsWithSecrets(secrets: readonly Pick<Secret, 'application'>[]): string[] {
  return [...new Set(secrets.map(s => s.application ?? '').filter(a => a !== ''))].sort()
}

const SECRET_COMMAND = /^shipwick secret set ([A-Za-z_][A-Za-z0-9_]*)$/

/**
 * The secrets a refused deployment is missing, by name and without repeats.
 * The agent answers INVALID_CONFIG with one entry per reference — field
 * `env.<VARIABLE>` or `proxy.basic_auth[i].password` — whose `expected` is
 * the command that stores the secret; the name is read from there, so a
 * stored secret that is merely unusable (too short a password) is offered
 * for replacement the same way.
 */
export function missingSecrets(fields: readonly { field: string, expected?: string }[]): string[] {
  const names: string[] = []
  for (const f of fields) {
    const name = SECRET_COMMAND.exec(f.expected ?? '')?.[1]
    if (name && !names.includes(name)) names.push(name)
  }
  return names
}

/**
 * What removing a secret does, for the confirmation. The server's goes while
 * the applications' own of the same name stay, and are named; an
 * application's own goes, and a server-wide one of the same name answers from
 * the next deployment on, if there is one.
 */
export function secretRemovalConsequence(name: string, application: string = '', stored: readonly Pick<Secret, 'name' | 'application'>[] = []): string {
  const others = stored.filter(s => s.name === name && (s.application ?? '') !== application)
  const base = `Deployments already made keep their value. The next deploy whose env refers to \${${name}} is refused until the secret is stored again.`
  if (application === '') {
    const own = others.map(s => s.application!).sort()
    if (own.length === 0) return base
    return `Deployments already made keep their value. ${own.join(', ')} ${own.length === 1 ? 'keeps its' : 'keep their'} own ${name}; every other application's next deploy that refers to \${${name}} is refused until the secret is stored again.`
  }
  if (others.some(s => (s.application ?? '') === '')) {
    return `Deployments already made keep their value. From the next deploy on, \${${name}} in ${application}'s deploy.yaml resolves to the server-wide ${name}.`
  }
  return `Deployments already made keep their value. The next deploy of ${application} whose env refers to \${${name}} is refused until the secret is stored again.`
}
