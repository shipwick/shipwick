import { describe, expect, it } from 'vitest'
import type { Traffic, TrafficPoint } from '../app/types/api'
import { errorShare, formatCount, formatLatency, hasTraffic, newestFirst, statusTone, toTrafficChart } from '../app/utils/traffic'

const MINUTE = 60_000
const since = Date.parse('2026-03-01T09:00:00Z')

function point(minute: number, requests: number, status5xx = 0, p95 = 40): TrafficPoint {
  return {
    t: new Date(since + minute * MINUTE).toISOString(),
    requests,
    status_2xx: requests - status5xx,
    status_3xx: 0,
    status_4xx: 0,
    status_5xx: status5xx,
    bytes: requests * 1000,
    p50_ms: p95 / 4,
    p95_ms: p95,
    p99_ms: p95 * 3,
  }
}

function traffic(points: TrafficPoint[]): Traffic {
  const requests = points.reduce((sum, p) => sum + p.requests, 0)
  const errors = points.reduce((sum, p) => sum + p.status_5xx, 0)
  return {
    application: 'my-api',
    since: new Date(since).toISOString(),
    step_seconds: 60,
    totals: { requests, status_2xx: requests - errors, status_3xx: 0, status_4xx: 0, status_5xx: errors, bytes: requests * 1000, p50_ms: 10, p95_ms: 40, p99_ms: 120 },
    points,
  }
}

describe('toTrafficChart', () => {
  it('fills the steps without a point with zeros: no request is a zero, not a gap', () => {
    const chart = toTrafficChart(traffic([point(0, 10), point(3, 20, 2)]), '1h', since + 5 * MINUTE)
    expect(chart.requests.map(p => p.value)).toEqual([10, 0, 0, 20, 0, 0])
    expect(chart.errors.map(p => p.value)).toEqual([0, 0, 0, 2, 0, 0])
    expect(chart.requests.map(p => p.t)).toEqual([0, 1, 2, 3, 4, 5].map(m => since + m * MINUTE))
  })

  it('spans the window the agent reported, in its steps, to the end', () => {
    const chart = toTrafficChart(traffic([point(1, 5)]), '1h', since + 60 * MINUTE)
    expect(chart.start).toBe(since)
    expect(chart.end).toBe(since + 60 * MINUTE)
    expect(chart.stepMs).toBe(MINUTE)
    expect(chart.requests).toHaveLength(61)
  })

  it('draws the latency only where there were requests, split at the steps without any', () => {
    const chart = toTrafficChart(traffic([point(0, 10, 0, 30), point(1, 10, 0, 50), point(4, 3, 0, 900)]), '1h', since + 5 * MINUTE)
    expect(chart.p95.map(segment => segment.map(p => p.value))).toEqual([[30, 50], [900]])
    expect(chart.p95[1]![0]!.t).toBe(since + 4 * MINUTE)
  })

  it('leaves headroom above the peak, and keeps a scale for an empty window', () => {
    const chart = toTrafficChart(traffic([point(0, 100, 0, 200)]), '1h', since + MINUTE)
    expect(chart.maxRequests).toBeCloseTo(115)
    expect(chart.maxP95).toBeCloseTo(230)
    const empty = toTrafficChart(traffic([]), '1h', since + 60 * MINUTE)
    expect(empty.requests.every(p => p.value === 0)).toBe(true)
    expect(empty.p95).toEqual([])
    expect(empty.maxRequests).toBe(1)
    expect(empty.maxP95).toBe(1)
  })

  it('drops a point that is not on the step grid rather than drawing it in the wrong place', () => {
    const off = { ...point(0, 7), t: new Date(since + 20_000).toISOString() }
    const chart = toTrafficChart(traffic([off, point(1, 5)]), '1h', since + 2 * MINUTE)
    expect(chart.requests.map(p => p.value)).toEqual([0, 5, 0])
  })

  it('falls back to the range\'s own window and step when the answer says neither', () => {
    const broken = { ...traffic([]), since: 'not a date', step_seconds: 0 }
    const end = since + 24 * 60 * MINUTE
    const chart = toTrafficChart(broken, '24h', end)
    expect(chart.stepMs).toBe(5 * MINUTE)
    expect(chart.start).toBe(since)
    expect(chart.requests).toHaveLength(289)
  })
})

describe('totals', () => {
  it('formats counts for a person', () => {
    expect(formatCount(12480)).toBe('12,480')
    expect(formatCount(0)).toBe('0')
    expect(formatCount(undefined)).toBe('—')
  })

  it('formats durations in the unit they are read in', () => {
    expect(formatLatency(0.8)).toBe('0.8 ms')
    expect(formatLatency(12.4)).toBe('12 ms')
    expect(formatLatency(480)).toBe('480 ms')
    expect(formatLatency(1234)).toBe('1.2 s')
    expect(formatLatency(30000)).toBe('30 s')
    expect(formatLatency(null)).toBe('—')
  })

  it('gives the share of 5xx answers, and none when there were no requests', () => {
    expect(errorShare({ requests: 12480, status_5xx: 20 })).toBe('0.16%')
    expect(errorShare({ requests: 100, status_5xx: 12 })).toBe('12%')
    expect(errorShare({ requests: 100, status_5xx: 0 })).toBe('0%')
    expect(errorShare({ requests: 10_000_000, status_5xx: 1 })).toBe('<0.01%')
    expect(errorShare({ requests: 0, status_5xx: 0 })).toBe('—')
  })

  it('knows a window without traffic', () => {
    expect(hasTraffic(traffic([]))).toBe(false)
    expect(hasTraffic(traffic([point(0, 1)]))).toBe(true)
    expect(hasTraffic(null)).toBe(false)
  })
})

describe('recent requests', () => {
  it('colors a status by its class', () => {
    expect(statusTone(200)).toBe('ok')
    expect(statusTone(204)).toBe('ok')
    expect(statusTone(308)).toBe('muted')
    expect(statusTone(404)).toBe('warn')
    expect(statusTone(503)).toBe('danger')
    expect(statusTone(0)).toBe('muted')
  })

  it('reads newest first without touching what the agent sent', () => {
    const sent = [
      { time: '2026-03-01T10:00:00.1Z', method: 'GET', path: '/a', status: 200, duration_ms: 1, bytes: 1, client: 'x' },
      { time: '2026-03-01T10:00:00.2Z', method: 'GET', path: '/b', status: 200, duration_ms: 1, bytes: 1, client: 'x' },
    ]
    expect(newestFirst(sent).map(r => r.path)).toEqual(['/b', '/a'])
    expect(sent.map(r => r.path)).toEqual(['/a', '/b'])
  })
})
