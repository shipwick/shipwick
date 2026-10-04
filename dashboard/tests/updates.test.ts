import { describe, expect, it } from 'vitest'
import type { UpdateStatus } from '../app/types/api'
import { atLeast07, releaseUrl, updateLine, updateNotice } from '../app/utils/updates'

const now = Date.parse('2026-10-04T12:00:00Z')

const available: UpdateStatus = { enabled: true, latest_version: 'v0.7.1', checked_at: '2026-10-04T09:00:00Z', available: true }
const current: UpdateStatus = { enabled: true, latest_version: 'v0.7.0', checked_at: '2026-10-04T09:00:00Z', available: false }
const unanswered: UpdateStatus = { enabled: true, latest_version: '', checked_at: null, available: false }
const off: UpdateStatus = { enabled: false, latest_version: '', checked_at: null, available: false }

describe('updateNotice', () => {
  it('says which release exists and which one the server runs', () => {
    expect(updateNotice({ agent_version: 'v0.7.0', update: available })).toEqual({
      latest: 'v0.7.1',
      running: 'v0.7.0',
      sentence: 'Shipwick v0.7.1 is available. This server runs v0.7.0.',
      url: 'https://github.com/shipwick/shipwick/releases/tag/v0.7.1',
    })
  })

  it('is nothing while there is nothing to do', () => {
    for (const update of [current, unanswered, off]) expect(updateNotice({ agent_version: 'v0.7.0', update })).toBeNull()
  })

  it('is nothing for an agent that does not check, and before the server has answered', () => {
    expect(updateNotice({ agent_version: 'v0.6.0' })).toBeNull()
    expect(updateNotice(null)).toBeNull()
  })

  it('does not trust an answer that says available and names no release', () => {
    expect(updateNotice({ agent_version: 'v0.7.0', update: { ...available, latest_version: '' } })).toBeNull()
  })

  it('encodes the version into the address, whatever it holds', () => {
    expect(releaseUrl('v0.7.1')).toBe('https://github.com/shipwick/shipwick/releases/tag/v0.7.1')
    expect(releaseUrl('../../evil?x=1')).toBe('https://github.com/shipwick/shipwick/releases/tag/..%2F..%2Fevil%3Fx%3D1')
  })
})

describe('updateLine', () => {
  it('says quietly that the server is up to date, and when that was checked', () => {
    expect(updateLine(current, now)).toBe('Up to date, checked 3h ago')
  })

  it('says that the check is off', () => {
    expect(updateLine(off, now)).toBe('The check for newer releases is off (SHIPWICK_UPDATE_CHECK)')
  })

  it('says nothing while the agent has never been answered', () => {
    expect(updateLine(unanswered, now)).toBe('')
  })

  it('leaves a newer release to the notice, and an agent before 0.7 alone', () => {
    expect(updateLine(available, now)).toBe('')
    expect(updateLine(undefined, now)).toBe('')
  })
})

describe('atLeast07', () => {
  it('is known by what GET /server carries', () => {
    expect(atLeast07({ update: off })).toBe(true)
    expect(atLeast07({ log_archive: { enabled: true, entries: 0, bytes: 0, max_bytes: 1, retention_days: 14 } })).toBe(true)
    expect(atLeast07({})).toBe(false)
    expect(atLeast07(null)).toBe(false)
  })
})
