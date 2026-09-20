import { describe, expect, it } from 'vitest'
import { safeRedirect } from '../app/utils/redirect'

describe('safeRedirect', () => {
  it('accepts same-site paths', () => {
    expect(safeRedirect('/applications/my-api')).toBe('/applications/my-api')
    expect(safeRedirect('/logs?application=web')).toBe('/logs?application=web')
  })

  it('rejects anything that could leave the site', () => {
    expect(safeRedirect('https://evil.example')).toBe('/')
    expect(safeRedirect('//evil.example')).toBe('/')
    expect(safeRedirect('/\\evil.example')).toBe('/')
    expect(safeRedirect('javascript:alert(1)')).toBe('/')
  })

  it('never redirects back to the login page, and tolerates junk', () => {
    expect(safeRedirect('/login')).toBe('/')
    expect(safeRedirect('/login?redirect=/x')).toBe('/')
    expect(safeRedirect(undefined)).toBe('/')
    expect(safeRedirect(['/a', '/b'])).toBe('/')
  })
})
