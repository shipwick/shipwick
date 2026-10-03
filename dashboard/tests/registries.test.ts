import { describe, expect, it } from 'vitest'
import { AgentError } from '../app/utils/agentError'
import {
  MAX_PASSWORD_BYTES,
  deniedRegistry,
  loginFailedHint,
  normalizeRegistry,
  passwordProblem,
  registryProblem,
  registryRemovalConsequence,
  replacesRegistry,
  usernameProblem,
} from '../app/utils/registries'

describe('normalizeRegistry', () => {
  it('is the hostname, with its port, in lower case', () => {
    expect(normalizeRegistry('ghcr.io')).toBe('ghcr.io')
    expect(normalizeRegistry(' GHCR.io ')).toBe('ghcr.io')
    expect(normalizeRegistry('registry.example.com:5000')).toBe('registry.example.com:5000')
    expect(normalizeRegistry('localhost:5055')).toBe('localhost:5055')
  })

  it('knows Docker Hub by its three names', () => {
    expect(normalizeRegistry('index.docker.io')).toBe('docker.io')
    expect(normalizeRegistry('registry-1.docker.io')).toBe('docker.io')
    expect(normalizeRegistry('docker.io')).toBe('docker.io')
  })

  it('refuses a scheme, a path and a bad port, as the agent does', () => {
    expect(normalizeRegistry('https://ghcr.io')).toBeNull()
    expect(normalizeRegistry('ghcr.io/acme')).toBeNull()
    expect(normalizeRegistry('ghcr.io:0')).toBeNull()
    expect(normalizeRegistry('ghcr.io:65536')).toBeNull()
    expect(normalizeRegistry('ghcr.io:080')).toBeNull()
    expect(normalizeRegistry('ghcr.io:')).toBeNull()
    expect(normalizeRegistry('')).toBeNull()
    expect(normalizeRegistry(`${'a'.repeat(256)}.io`)).toBeNull()
  })
})

describe('the form', () => {
  it('says what a registry\'s name looks like', () => {
    expect(registryProblem('ghcr.io')).toBe('')
    expect(registryProblem('')).toBe('')
    expect(registryProblem('https://ghcr.io')).toContain('ghcr.io, registry.example.com:5000')
  })

  it('holds the username to the agent\'s rules', () => {
    expect(usernameProblem('octocat')).toBe('')
    expect(usernameProblem('_json_key')).toBe('')
    expect(usernameProblem('a:b')).toContain('colon')
    expect(usernameProblem('a\tb')).toContain('control')
    expect(usernameProblem('a'.repeat(256))).toContain('255')
  })

  it('holds the password to 16 KB and never quotes it', () => {
    expect(passwordProblem('ghp_abc')).toBe('')
    expect(passwordProblem('a\0b')).toContain('NUL')
    expect(passwordProblem('x'.repeat(MAX_PASSWORD_BYTES))).toBe('')
    const long = 'p'.repeat(MAX_PASSWORD_BYTES + 1)
    expect(passwordProblem(long)).toContain('16 KB')
    expect(passwordProblem(long)).not.toContain('ppp')
  })

  it('says when a login replaces a stored credential', () => {
    expect(replacesRegistry('ghcr.io', [{ registry: 'ghcr.io' }])).toBe(true)
    expect(replacesRegistry('quay.io', [{ registry: 'ghcr.io' }])).toBe(false)
    expect(replacesRegistry(null, [{ registry: 'ghcr.io' }])).toBe(false)
  })
})

describe('loginFailedHint', () => {
  it('tells a refusal from a registry that could not be asked', () => {
    const refused = new AgentError(400, 'REGISTRY_LOGIN_FAILED', 'ghcr.io refused the login: denied: denied', { registry: 'ghcr.io', refused: true })
    const unreachable = new AgentError(400, 'REGISTRY_LOGIN_FAILED', 'could not log in to ghcr.oi: no such host', { registry: 'ghcr.oi', refused: false })
    expect(loginFailedHint(refused)).toContain('Check the username and the token')
    expect(loginFailedHint(unreachable)).toContain('Check the registry\'s name')
    expect(loginFailedHint(refused)).toContain('Nothing was stored')
    expect(loginFailedHint(new AgentError(400, 'INVALID_REQUEST', 'x'))).toBe('')
  })
})

describe('deniedRegistry', () => {
  it('reads the registry out of the agent\'s pull-denied sentence', () => {
    expect(deniedRegistry('pull access denied for ghcr.io/company/api: run shipwick registry login ghcr.io (or check the image name)')).toBe('ghcr.io')
    expect(deniedRegistry('pull access denied for localhost:5055/sw05/web: the credential stored for localhost:5055 does not give access to it; replace it with shipwick registry login localhost:5055 (or check the image name)')).toBe('localhost:5055')
  })

  it('is null for every other failure', () => {
    expect(deniedRegistry('replica 1 exited with code 1 shortly after start')).toBeNull()
    expect(deniedRegistry('')).toBeNull()
  })
})

describe('registryRemovalConsequence', () => {
  it('names the registry and what stops working', () => {
    const text = registryRemovalConsequence('ghcr.io')
    expect(text).toContain('ghcr.io')
    expect(text).toContain('What is running keeps running')
  })
})
