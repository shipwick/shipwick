import { describe, expect, it } from 'vitest'
import { MAX_VALUE_BYTES, applicationsWithSecrets, missingSecrets, replaces, scopeLabel, scopesKnown, secretNameProblem, secretRemovalConsequence, secretScopeProblem, secretValueProblem, sortSecrets, urlCharacterHint, urlUnsafeCharacters } from '../app/utils/secrets'

describe('secretNameProblem', () => {
  it('accepts environment variable names', () => {
    expect(secretNameProblem('DATABASE_PASSWORD')).toBe('')
    expect(secretNameProblem('_private')).toBe('')
    expect(secretNameProblem('key2')).toBe('')
    expect(secretNameProblem('A'.repeat(64))).toBe('')
  })

  it('refuses what the agent refuses, and says what a name looks like', () => {
    expect(secretNameProblem('2FA_CODE')).toContain('not starting with a digit')
    expect(secretNameProblem('database-password')).toContain('Letters, digits and underscores')
    expect(secretNameProblem('WITH SPACE')).not.toBe('')
    expect(secretNameProblem('A'.repeat(65))).toBe('At most 64 characters.')
  })

  it('says nothing about an empty field: the form is simply not ready', () => {
    expect(secretNameProblem('')).toBe('')
  })
})

describe('secretValueProblem', () => {
  it('accepts any text up to 64 KB and never repeats it', () => {
    expect(secretValueProblem('hunter2')).toBe('')
    expect(secretValueProblem('a'.repeat(MAX_VALUE_BYTES))).toBe('')
    expect(secretValueProblem('')).toBe('')
  })

  it('refuses a NUL byte and a value over 64 KB, counted in bytes', () => {
    expect(secretValueProblem('a\0b')).toBe('The value must not contain a NUL byte.')
    expect(secretValueProblem('a'.repeat(MAX_VALUE_BYTES + 1))).toBe('The value must be at most 64 KB.')
    // Two bytes per character: half the characters are already the limit.
    expect(secretValueProblem('é'.repeat(MAX_VALUE_BYTES / 2 + 1))).toBe('The value must be at most 64 KB.')
  })
})

describe('replaces', () => {
  const stored = [{ name: 'DATABASE_PASSWORD' }, { name: 'STRIPE_KEY' }]

  it('is true only for a name that is already stored, exactly', () => {
    expect(replaces('STRIPE_KEY', stored)).toBe(true)
    expect(replaces('stripe_key', stored)).toBe(false)
    expect(replaces('SMTP_PASSWORD', stored)).toBe(false)
    expect(replaces('', stored)).toBe(false)
  })
})

describe('secretRemovalConsequence', () => {
  it('says that made deployments keep the value and the next deploy referring to it is refused', () => {
    const text = secretRemovalConsequence('DATABASE_PASSWORD')
    expect(text).toContain('Deployments already made keep their value')
    expect(text).toContain('${DATABASE_PASSWORD}')
    expect(text).toContain('refused')
  })
})

describe('missingSecrets', () => {
  it('names the secrets a refused deployment refers to, from the command the agent suggests', () => {
    expect(missingSecrets([
      { field: 'env.DATABASE_URL', expected: 'shipwick secret set POSTGRES_PASSWORD' },
      { field: 'env.STRIPE', expected: 'shipwick secret set STRIPE_KEY' },
    ])).toEqual(['POSTGRES_PASSWORD', 'STRIPE_KEY'])
  })

  it('covers a basic-auth password the same way', () => {
    expect(missingSecrets([{ field: 'proxy.basic_auth[0].password', expected: 'shipwick secret set ADMIN_PASSWORD' }])).toEqual(['ADMIN_PASSWORD'])
  })

  it('names a secret once however often it is referred to', () => {
    expect(missingSecrets([
      { field: 'env.A', expected: 'shipwick secret set TOKEN' },
      { field: 'env.B', expected: 'shipwick secret set TOKEN' },
    ])).toEqual(['TOKEN'])
  })

  it('offers nothing for any other problem', () => {
    expect(missingSecrets([
      { field: 'domain' },
      { field: 'resources.memory', expected: '128mb, 512mb, 1gb, ...' },
      { field: 'env.X', expected: 'shipwick secret set' },
      { field: 'env.Y', expected: 'shipwick secret set 1BAD' },
      { field: 'env.Z', expected: 'run shipwick secret set NAME first' },
    ])).toEqual([])
    expect(missingSecrets([])).toEqual([])
  })
})

describe('a secret of one application', () => {
  const stored = [
    { name: 'DATABASE_PASSWORD', application: '' },
    { name: 'DATABASE_PASSWORD', application: 'api' },
    { name: 'STRIPE_KEY', application: 'shop' },
  ]

  it('replaces by scope and name, not by name alone', () => {
    expect(replaces('DATABASE_PASSWORD', stored)).toBe(true)
    expect(replaces('DATABASE_PASSWORD', stored, 'api')).toBe(true)
    expect(replaces('DATABASE_PASSWORD', stored, 'web')).toBe(false)
    expect(replaces('STRIPE_KEY', stored)).toBe(false)
    expect(replaces('STRIPE_KEY', stored, 'shop')).toBe(true)
  })

  it('reads a row without application, from an agent before 0.9, as the server\'s', () => {
    expect(replaces('TOKEN', [{ name: 'TOKEN' }])).toBe(true)
    expect(replaces('TOKEN', [{ name: 'TOKEN' }], 'api')).toBe(false)
  })

  it('validates the scope as an application name, since the application may not exist yet', () => {
    expect(secretScopeProblem('')).toBe('')
    expect(secretScopeProblem('my-api')).toBe('')
    expect(secretScopeProblem('My_API')).toContain('lowercase letters, digits and dashes')
    expect(secretScopeProblem('-api')).not.toBe('')
    expect(secretScopeProblem('a'.repeat(64))).not.toBe('')
  })

  it('labels the server\'s scope and names an application\'s', () => {
    expect(scopeLabel('')).toBe('Server')
    expect(scopeLabel(undefined)).toBe('Server')
    expect(scopeLabel('api')).toBe('api')
  })

  it('sorts by name with the server\'s before the applications\' of the same name', () => {
    expect(sortSecrets([
      { name: 'STRIPE_KEY', application: 'shop' },
      { name: 'DATABASE_PASSWORD', application: 'api' },
      { name: 'DATABASE_PASSWORD', application: '' },
    ]).map(s => `${s.application}/${s.name}`)).toEqual(['/DATABASE_PASSWORD', 'api/DATABASE_PASSWORD', 'shop/STRIPE_KEY'])
  })

  it('knows the scopes only when every row says one, and nothing of an empty list', () => {
    expect(scopesKnown(stored)).toBe(true)
    expect(scopesKnown([{ name: 'A' }, { name: 'B' }])).toBe(false)
    expect(scopesKnown([])).toBeNull()
  })

  it('offers the applications that have secrets of their own, once each and sorted', () => {
    expect(applicationsWithSecrets([...stored, { name: 'X', application: 'api' }, { name: 'Y' }])).toEqual(['api', 'shop'])
  })
})

describe('urlCharacterHint', () => {
  it('names the characters a URL cannot carry, never the value', () => {
    expect(urlUnsafeCharacters('p@ss:word/with?odd#chars%20 and space')).toEqual(['@', ':', '/', '?', '#', '%', 'whitespace'])
    const hint = urlCharacterHint('secret@2026')
    expect(hint).toContain('@')
    expect(hint).toContain('connection string')
    expect(hint).toContain('stored all the same')
    expect(hint).not.toContain('secret@2026')
    expect(hint).not.toContain('2026')
  })

  it('says nothing of a value that is safe anywhere in a URL', () => {
    expect(urlUnsafeCharacters('a-b_c.d~E9')).toEqual([])
    expect(urlCharacterHint('a-b_c.d~E9')).toBe('')
    expect(urlCharacterHint('')).toBe('')
  })
})

describe('secretRemovalConsequence, by scope', () => {
  const stored = [
    { name: 'DATABASE_PASSWORD', application: '' },
    { name: 'DATABASE_PASSWORD', application: 'api' },
    { name: 'DATABASE_PASSWORD', application: 'web' },
    { name: 'ONLY_OWN', application: 'api' },
  ]

  it('removing the server\'s says which applications keep their own', () => {
    const text = secretRemovalConsequence('DATABASE_PASSWORD', '', stored)
    expect(text).toContain('api, web keep their own DATABASE_PASSWORD')
    expect(text).toContain('every other application')
    expect(secretRemovalConsequence('DATABASE_PASSWORD', '', stored.filter(s => s.application !== 'web'))).toContain('api keeps its own')
  })

  it('removing an application\'s says the server-wide one answers from the next deployment on, when there is one', () => {
    const text = secretRemovalConsequence('DATABASE_PASSWORD', 'api', stored)
    expect(text).toContain('${DATABASE_PASSWORD} in api\'s deploy.yaml resolves to the server-wide DATABASE_PASSWORD')
    expect(text).not.toContain('refused')
  })

  it('removing an application\'s only one says its next deploy is refused', () => {
    const text = secretRemovalConsequence('ONLY_OWN', 'api', stored)
    expect(text).toContain('next deploy of api')
    expect(text).toContain('refused')
  })
})
