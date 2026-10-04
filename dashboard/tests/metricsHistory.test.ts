import { describe, expect, it } from 'vitest'
import type { MetricsHistory } from '../app/types/api'
import { historyLimit, isMetricsRange, parseStep, pointFigures, segmentPoints, timeTicks, toChartData } from '../app/utils/metricsHistory'

const T0 = Date.parse('2026-03-01T09:00:00Z')
const at = (offsetMs: number) => new Date(T0 + offsetMs).toISOString()

const history: MetricsHistory = {
  application: 'my-api',
  since: at(0),
  step: '30s',
  series: [
    {
      replica: 1,
      points: [
        { at: at(0), cpu_percent: 12.5, memory_bytes: 200 },
        { at: at(30_000), cpu_percent: 14, memory_bytes: 210 },
        // two buckets missing: the replica was not running
        { at: at(120_000), cpu_percent: 9, memory_bytes: 190 },
        { at: at(150_000), cpu_percent: 11, memory_bytes: 205 },
      ],
    },
    { replica: 2, points: [{ at: at(150_000), cpu_percent: 40, memory_bytes: 300 }] },
  ],
  limits: { cpu: 1, memory_bytes: 1024 },
}

describe('parseStep', () => {
  it('reads the agent\'s step strings', () => {
    expect(parseStep('30s', '1h')).toBe(30_000)
    expect(parseStep('5m', '24h')).toBe(300_000)
    expect(parseStep('1h', '7d')).toBe(3_600_000)
  })
  it('falls back to the range\'s own step for anything unreadable', () => {
    expect(parseStep('soon', '24h')).toBe(300_000)
    expect(parseStep('0s', '1h')).toBe(30_000)
  })
  it('knows the accepted ranges', () => {
    expect(isMetricsRange('24h')).toBe(true)
    expect(isMetricsRange('2h')).toBe(false)
  })
})

describe('segmentPoints', () => {
  it('splits a series at every gap instead of drawing across it', () => {
    const segments = segmentPoints(history.series[0]!.points, 'cpu_percent', 30_000)
    expect(segments.map(s => s.map(p => p.value))).toEqual([[12.5, 14], [9, 11]])
  })

  it('tolerates a little jitter in the bucket boundaries', () => {
    const points = [{ at: at(0), cpu_percent: 1, memory_bytes: 1 }, { at: at(31_000), cpu_percent: 2, memory_bytes: 1 }]
    expect(segmentPoints(points, 'cpu_percent', 30_000)).toHaveLength(1)
  })

  it('skips points with an unreadable time and handles an empty series', () => {
    expect(segmentPoints([{ at: 'never', cpu_percent: 1, memory_bytes: 1 }], 'memory_bytes', 30_000)).toEqual([])
    expect(segmentPoints([], 'memory_bytes', 30_000)).toEqual([])
  })
})

describe('historyLimit', () => {
  it('turns cores into percent of one core and passes bytes through', () => {
    expect(historyLimit(history, 'cpu_percent')).toBe(100)
    expect(historyLimit(history, 'memory_bytes')).toBe(1024)
  })
  it('is null when unlimited', () => {
    expect(historyLimit({ limits: { cpu: 0, memory_bytes: 0 } }, 'cpu_percent')).toBeNull()
    expect(historyLimit({ limits: { cpu: 0, memory_bytes: 0 } }, 'memory_bytes')).toBeNull()
  })
})

describe('toChartData', () => {
  const end = T0 + 3_600_000

  it('keeps one series per replica with its segments and newest point', () => {
    const chart = toChartData(history, 'cpu_percent', '1h', end)
    expect(chart.series.map(s => s.replica)).toEqual([1, 2])
    expect(chart.series[0]?.segments).toHaveLength(2)
    expect(chart.series[0]?.latest).toEqual({ t: T0 + 150_000, value: 11 })
    expect(chart.series[1]?.latest?.value).toBe(40)
  })

  it('spans the agent\'s window, from since to now', () => {
    const chart = toChartData(history, 'cpu_percent', '1h', end)
    expect(chart.start).toBe(T0)
    expect(chart.end).toBe(end)
    expect(chart.stepMs).toBe(30_000)
  })

  it('uses the limit as the ceiling while nothing exceeds it', () => {
    expect(toChartData(history, 'cpu_percent', '1h', end).max).toBe(100)
    expect(toChartData(history, 'memory_bytes', '1h', end).limit).toBe(1024)
  })

  it('grows past a limit a replica has burst through, and gives an unlimited metric headroom', () => {
    const bursting = { ...history, series: [{ replica: 1, points: [{ at: at(0), cpu_percent: 130, memory_bytes: 50 }] }] }
    expect(toChartData(bursting, 'cpu_percent', '1h', end).max).toBeCloseTo(130 * 1.15)
    const unlimited = { ...bursting, limits: { cpu: 0, memory_bytes: 0 } }
    expect(toChartData(unlimited, 'memory_bytes', '1h', end)).toMatchObject({ limit: null, max: 50 * 1.15 })
  })

  it('never divides by zero on an empty history', () => {
    const empty = { ...history, series: [], limits: { cpu: 0, memory_bytes: 0 } }
    expect(toChartData(empty, 'cpu_percent', '1h', end).max).toBe(1)
  })
})

describe('timeTicks', () => {
  it('places ticks on round times inside the window', () => {
    expect(timeTicks(T0 + 1, T0 + 3_600_000, '1h').map(t => new Date(t).toISOString().slice(11, 16))).toEqual(['09:15', '09:30', '09:45', '10:00'])
    expect(timeTicks(T0, T0 + 24 * 3_600_000, '24h')).toHaveLength(6)
    expect(timeTicks(T0, T0 + 7 * 24 * 3_600_000, '7d')).toHaveLength(7)
  })
})

describe('a line of a chart in numbers', () => {
  it('counts the points across the gaps and gives the least, the mean, the peak and the last', () => {
    const segments = [[{ t: 1, value: 10 }, { t: 2, value: 30 }], [{ t: 5, value: 20 }]]
    expect(pointFigures(segments)).toEqual({ samples: 3, min: 10, avg: 20, max: 30, latest: 20 })
  })

  it('has no figures for a line without points', () => {
    expect(pointFigures([])).toEqual({ samples: 0, min: null, avg: null, max: null, latest: null })
  })
})
