import { describe, expect, it } from 'vitest'
import { hasNoLogs, logApplications } from '../app/utils/logs'

const apps = [
  { name: 'web', static: false },
  { name: 'landing', static: true },
  { name: 'api', static: false },
]

describe('logApplications', () => {
  it('offers the applications that have containers, sorted, and not the static ones', () => {
    expect(logApplications(apps)).toEqual(['api', 'web'])
  })

  it('keeps an application named in the URL that the list does not know', () => {
    expect(logApplications(apps, 'gone')).toEqual(['api', 'gone', 'web'])
    expect(logApplications([], 'my-api')).toEqual(['my-api'])
  })

  it('never offers a static application, named in the URL or not', () => {
    expect(logApplications(apps, 'landing')).toEqual(['api', 'web'])
    expect(logApplications([{ name: 'landing', static: true }], 'landing')).toEqual([])
  })
})

describe('hasNoLogs', () => {
  it('is true only for a static application the list knows', () => {
    expect(hasNoLogs(apps, 'landing')).toBe(true)
    expect(hasNoLogs(apps, 'web')).toBe(false)
    expect(hasNoLogs(apps, 'gone')).toBe(false)
    expect(hasNoLogs(apps, '')).toBe(false)
  })
})
