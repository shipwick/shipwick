import { describe, expect, it } from 'vitest'
import { RESUME_DELAYS_MS, STALE_AFTER_MS, boundaryDuplicates, endMeaning, isStale, latestTime, lineKey, resumeDelay, resumeQuery } from '../app/utils/logResume'
import type { LogRecord } from '../app/utils/ndjson'

const line = (time: string, message: string, container = 'shipwick_api_7_1'): LogRecord => ({
  kind: 'line',
  line: { replica: 1, container, stream: 'stdout', time, message },
})
const heartbeat = (time: string): LogRecord => ({ kind: 'heartbeat', time })

describe('resumeDelay', () => {
  it('waits longer each time and gives up after about half a minute', () => {
    expect(RESUME_DELAYS_MS.map((_, i) => resumeDelay(i))).toEqual([1000, 2000, 4000, 8000, 16000])
    expect(RESUME_DELAYS_MS.reduce((a, b) => a + b, 0)).toBe(31_000)
    expect(resumeDelay(5)).toBeNull()
  })
})

describe('isStale', () => {
  it('is stale after 45 seconds without a line or a heartbeat, not before', () => {
    expect(isStale(1_000_000, 1_000_000 + STALE_AFTER_MS - 1)).toBe(false)
    expect(isStale(1_000_000, 1_000_000 + STALE_AFTER_MS)).toBe(true)
  })
})

describe('latestTime', () => {
  it('is the latest of the lines and the heartbeats, so a quiet application is taken up from its last heartbeat', () => {
    expect(latestTime('', [line('2026-10-11T16:40:48Z', 'tick 60'), heartbeat('2026-10-11T16:41:08.5Z')])).toBe('2026-10-11T16:41:08.5Z')
    expect(latestTime('2026-10-11T16:41:08.5Z', [line('2026-10-11T16:40:48Z', 'late line')])).toBe('2026-10-11T16:41:08.5Z')
    expect(latestTime('2026-10-11T16:41:08.5Z', [line('2026-10-11T16:41:09.000000001Z', 'tick 61')])).toBe('2026-10-11T16:41:09.000000001Z')
  })

  it('ignores times that do not parse and an end line, and is empty when nothing was heard', () => {
    expect(latestTime('', [{ kind: 'end', reason: 'shutdown' }, line('', 'no time')])).toBe('')
    expect(latestTime('garbage', [line('2026-10-11T16:40:48Z', 'tick')])).toBe('2026-10-11T16:40:48Z')
  })
})

describe('resumeQuery', () => {
  it('asks for the time the agent last told, and without one for the tail as before', () => {
    expect(resumeQuery(100, '2026-10-11T16:41:08.5Z')).toEqual({ follow: true, since: '2026-10-11T16:41:08.5Z' })
    expect(resumeQuery(100, '')).toEqual({ follow: true, tail: 100 })
  })
})

describe('boundaryDuplicates', () => {
  const shown = [
    { container: 'c1', time: '2026-10-11T16:40:48Z', message: 'tick 59' },
    { container: 'c1', time: '2026-10-11T16:40:53Z', message: 'tick 60' },
    { container: 'c2', time: '2026-10-11T16:40:53Z', message: 'other replica, same instant' },
  ]

  it('names the lines on screen at the instant the stream is taken up from, which Docker sends again', () => {
    const keys = boundaryDuplicates(shown, '2026-10-11T16:40:53Z')
    expect(keys).toEqual(new Set([lineKey(shown[1]!), lineKey(shown[2]!)]))
    expect(keys.has(lineKey(shown[0]!))).toBe(false)
  })

  it('matches the instant however the agent spells it, and nothing for a time that is not one', () => {
    expect(boundaryDuplicates(shown, '2026-10-11T16:40:53.000Z').size).toBe(2)
    expect(boundaryDuplicates(shown, '').size).toBe(0)
  })
})

describe('endMeaning', () => {
  it('takes a stream up after a shutdown and ends it when the containers are gone, or when an older agent says nothing', () => {
    expect(endMeaning('shutdown')).toBe('resume')
    expect(endMeaning('containers')).toBe('ended')
    expect(endMeaning(null)).toBe('ended')
    expect(endMeaning('something-new')).toBe('ended')
  })
})
