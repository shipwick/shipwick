import { describe, expect, it } from 'vitest'
import { MAX_VALUE_BYTES, replaces, secretNameProblem, secretRemovalConsequence, secretValueProblem } from '../app/utils/secrets'

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
