import { describe, expect, it } from 'vitest'
import { AgentError } from '../app/utils/agentError'
import { cloudflareDisplay, cloudflareSummary, verifyRefusal, zoneDisplay } from '../app/utils/cloudflare'

const zones = [{ name: 'example.com', dns_edit: true }, { name: 'shipwick.com', dns_edit: false }]

describe('cloudflareDisplay', () => {
  it('is green when the token can do its job and red when it cannot', () => {
    expect(cloudflareDisplay({ ok: true })).toEqual({ tone: 'ok', label: 'Token works' })
    expect(cloudflareDisplay({ ok: false })).toEqual({ tone: 'danger', label: 'Token cannot do its job' })
  })
})

describe('cloudflareSummary', () => {
  it('names the zones the token can and cannot edit', () => {
    expect(cloudflareSummary({ ok: true, zones: [zones[0]!] })).toBe('The token can edit the DNS records of example.com.')
    expect(cloudflareSummary({ ok: false, zones })).toBe('The token can edit the DNS records of example.com, and not of shipwick.com.')
    expect(cloudflareSummary({ ok: false, zones: [zones[1]!] })).toBe('The token sees shipwick.com and can edit the DNS records of none of them.')
    expect(cloudflareSummary({ ok: false, zones: [] })).toBe('The token sees no zone.')
  })

  it('marks each zone by whether a record could be written in it', () => {
    expect(zoneDisplay(zones[0]!)).toEqual({ tone: 'ok', label: 'DNS edit' })
    expect(zoneDisplay(zones[1]!)).toEqual({ tone: 'danger', label: 'No DNS edit' })
  })
})

describe('verifyRefusal', () => {
  it('hides the button when the agent has no token or is older than the endpoint', () => {
    expect(verifyRefusal(new AgentError(409, 'CLOUDFLARE_NOT_CONFIGURED', 'no token'))).toMatchObject({ hide: true, message: 'The agent has no Cloudflare token to check.' })
    const older = verifyRefusal(new AgentError(404, 'ENDPOINT_NOT_FOUND', 'no such endpoint'))
    expect(older.hide).toBe(true)
    expect(older.message).toContain('older than 0.9')
  })

  it('says how long to wait after a recent check, from Retry-After or the details, and keeps the last result', () => {
    const withHeader = verifyRefusal(new AgentError(429, 'CLOUDFLARE_CHECKED_RECENTLY', 'checked recently', { checked_at: '2026-10-11T09:00:00Z', retry_after_seconds: 42 }, 42))
    expect(withHeader).toMatchObject({ hide: false, retryAfterSeconds: 42 })
    expect(withHeader.message).toContain('Try again in 42 seconds')
    expect(withHeader.message).toContain('result shown')
    expect(verifyRefusal(new AgentError(429, 'CLOUDFLARE_CHECKED_RECENTLY', 'checked recently', { retry_after_seconds: 1 })).message).toContain('in 1 second')
    expect(verifyRefusal(new AgentError(429, 'CLOUDFLARE_CHECKED_RECENTLY', 'checked recently')).retryAfterSeconds).toBe(60)
  })

  it('leaves any other failure to speak for itself', () => {
    expect(verifyRefusal(new AgentError(503, 'RUNTIME_UNAVAILABLE', 'shutting down'))).toEqual({ hide: false, message: '', retryAfterSeconds: 0 })
  })
})
