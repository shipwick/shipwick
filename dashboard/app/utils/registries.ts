import type { Registry } from '~/types/api'
import type { AgentError } from '~/utils/agentError'

/**
 * The Registries page's rules, mirrored from the agent (pkg/api/registries.go)
 * so a bad name is refused before the request and the wording is unit-tested.
 * The agent checks everything again, and it is the registry that judges the
 * credential.
 */

const HOST_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/
const MAX_REGISTRY_LENGTH = 255
const MAX_USERNAME_LENGTH = 255
/** The agent's limit on a password: a token, or a JSON service-account key of a few kilobytes. */
export const MAX_PASSWORD_BYTES = 16 * 1024

/**
 * The name a credential is stored under, which is the one image references
 * use: a hostname with an optional port, in lower case. Docker Hub goes by
 * three names and they are one registry. Null when it is not a registry's name.
 */
export function normalizeRegistry(input: string): string | null {
  const name = input.trim().toLowerCase()
  const colon = name.indexOf(':')
  const host = colon === -1 ? name : name.slice(0, colon)
  if (name.length > MAX_REGISTRY_LENGTH || !HOST_PATTERN.test(host)) return null
  if (colon !== -1) {
    const port = name.slice(colon + 1)
    if (!/^[1-9]\d{0,4}$/.test(port) || Number(port) > 65535) return null
  }
  if (name === 'index.docker.io' || name === 'registry-1.docker.io') return 'docker.io'
  return name
}

/** Why this is not a registry's name, or "" when it is. */
export function registryProblem(input: string): string {
  if (input.trim() === '') return ''
  if (normalizeRegistry(input) === null) return 'The registry\'s hostname, with a port if it has one and nothing else: ghcr.io, registry.example.com:5000.'
  return ''
}

/** Why a username cannot be stored, or "" when it can. */
export function usernameProblem(username: string): string {
  if (username === '') return ''
  if (username.length > MAX_USERNAME_LENGTH) return `At most ${MAX_USERNAME_LENGTH} characters.`
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1F\x7F:]/.test(username)) return 'The username must not contain a colon or control characters.'
  return ''
}

/** Why a password cannot be stored, or "" when it can. Never quotes the password. */
export function passwordProblem(password: string): string {
  if (password === '') return ''
  if (password.includes('\0')) return 'The password must not contain a NUL byte.'
  if (new TextEncoder().encode(password).length > MAX_PASSWORD_BYTES) return 'The password must be at most 16 KB.'
  return ''
}

/** The stored credential a login would replace, if any: PUT creates and replaces alike, so the form says which. */
export function replacesRegistry(registry: string | null, stored: readonly Pick<Registry, 'registry'>[]): boolean {
  return registry !== null && stored.some(r => r.registry === registry)
}

/**
 * What to check after a refused login. The registry said no (`refused`), or it
 * could not be asked at all: those call for different things.
 */
export function loginFailedHint(error: Pick<AgentError, 'code' | 'details'>): string {
  if (error.code !== 'REGISTRY_LOGIN_FAILED') return ''
  return error.details.refused === true
    ? 'Nothing was stored. Check the username and the token, and that the token may read images.'
    : 'Nothing was stored. Check the registry\'s name, and that the server can reach it.'
}

/** What logging out of a registry does, for the confirmation. */
export function registryRemovalConsequence(registry: string): string {
  return `Images from ${registry} are pulled without a credential from then on: a private image fails to pull at the next deployment, rollback or job, and when a replica's image has to be fetched again. What is running keeps running.`
}

/** The registry a failed pull names, when a deployment's error is the agent's "pull access denied … run shipwick registry login <registry>"; null otherwise. */
export function deniedRegistry(error: string): string | null {
  if (!error.startsWith('pull access denied for ')) return null
  const match = /shipwick registry login (\S+?)[\s)]/.exec(`${error} `)
  return match ? match[1]! : null
}
