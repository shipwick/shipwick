import type { MetricsHistory, MetricsPoint, MetricsRange } from '~/types/api'

/**
 * Turns the agent's metrics history into something a chart can draw. Pure:
 * the component only maps the numbers here onto pixels.
 *
 * The series are sparse on purpose: a bucket in which a replica had no sample
 * (it was not running, the agent was down) has no point. A gap is drawn as a
 * gap, so a series is split into segments of consecutive buckets and nothing
 * is interpolated across the hole.
 */

export const METRICS_RANGES: readonly MetricsRange[] = ['1h', '24h', '7d']

const MINUTE = 60_000
const HOUR = 60 * MINUTE

/** The window each range covers, and the bucket width the agent uses for it. */
export const RANGE_WINDOWS: Record<MetricsRange, { windowMs: number, stepMs: number }> = {
  '1h': { windowMs: HOUR, stepMs: 30_000 },
  '24h': { windowMs: 24 * HOUR, stepMs: 5 * MINUTE },
  '7d': { windowMs: 7 * 24 * HOUR, stepMs: HOUR },
}

export function isMetricsRange(value: unknown): value is MetricsRange {
  return typeof value === 'string' && (METRICS_RANGES as readonly string[]).includes(value)
}

/** Parses the agent's step ("30s", "5m", "1h") into milliseconds; falls back to the range's own. */
export function parseStep(step: string, range: MetricsRange): number {
  const match = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(step.trim())
  if (!match) return RANGE_WINDOWS[range].stepMs
  const n = Number(match[1])
  const unit = match[2] === 'ms' ? 1 : match[2] === 's' ? 1000 : match[2] === 'm' ? MINUTE : HOUR
  const ms = n * unit
  return ms > 0 ? ms : RANGE_WINDOWS[range].stepMs
}

export type HistoryMetric = 'cpu_percent' | 'memory_bytes'

export interface ChartPoint {
  /** Bucket start, epoch milliseconds. */
  t: number
  value: number
}

export interface ChartSeries {
  replica: number
  /** Runs of consecutive buckets; a new segment starts after every gap. */
  segments: ChartPoint[][]
  /** The newest point, for the legend readout. */
  latest: ChartPoint | null
}

export interface ChartData {
  /** The x axis: the window as the agent reported it, to the end. */
  start: number
  end: number
  stepMs: number
  series: ChartSeries[]
  /** The y axis spans 0..max; the limit when there is one and it is not exceeded, else the peak with headroom. */
  max: number
  /** The per-replica limit for this metric, in the metric's unit; null when unlimited. */
  limit: number | null
}

/**
 * Splits ordered points into segments. Two points belong to the same segment
 * when they are one step apart; a small tolerance absorbs clock jitter in the
 * bucket boundaries.
 */
export function segmentPoints(points: readonly MetricsPoint[], metric: HistoryMetric, stepMs: number): ChartPoint[][] {
  const segments: ChartPoint[][] = []
  let current: ChartPoint[] = []
  let previous: number | null = null
  for (const p of points) {
    const t = Date.parse(p.at)
    if (Number.isNaN(t)) continue
    if (previous !== null && t - previous > stepMs * 1.5) {
      if (current.length > 0) segments.push(current)
      current = []
    }
    current.push({ t, value: p[metric] })
    previous = t
  }
  if (current.length > 0) segments.push(current)
  return segments
}

/** The limit line of a metric: cores become percent of one core; 0 means unlimited. */
export function historyLimit(history: Pick<MetricsHistory, 'limits'>, metric: HistoryMetric): number | null {
  const limits = history.limits
  if (!limits) return null
  const value = metric === 'cpu_percent' ? limits.cpu * 100 : limits.memory_bytes
  return value > 0 ? value : null
}

/**
 * Chart data for one metric. `end` defaults to now; the window is the agent's
 * `since` up to it, so a freshly started replica sits at the right edge and
 * the space before it stays empty rather than the chart stretching.
 */
export function toChartData(history: MetricsHistory, metric: HistoryMetric, range: MetricsRange, end: number = Date.now()): ChartData {
  const stepMs = parseStep(history.step, range)
  const since = Date.parse(history.since)
  const start = Number.isNaN(since) ? end - RANGE_WINDOWS[range].windowMs : since
  const limit = historyLimit(history, metric)

  let peak = 0
  const series = history.series.map<ChartSeries>((s) => {
    const segments = segmentPoints(s.points, metric, stepMs)
    for (const segment of segments) for (const p of segment) peak = Math.max(peak, p.value)
    const last = segments[segments.length - 1]
    return { replica: s.replica, segments, latest: last ? last[last.length - 1] ?? null : null }
  })

  // The limit is the natural ceiling. A replica above it (CPU can burst past a
  // soft limit for a moment) still has to be on the chart.
  let max: number
  if (limit !== null && peak <= limit) max = limit
  else max = peak <= 0 ? 1 : peak * 1.15

  return { start: Math.min(start, end), end, stepMs, series, max, limit }
}

/** Tick positions along the x axis: whole hours for a day, whole days for a week, every 15 minutes for an hour. */
export function timeTicks(start: number, end: number, range: MetricsRange): number[] {
  const every = range === '1h' ? 15 * MINUTE : range === '24h' ? 4 * HOUR : 24 * HOUR
  const ticks: number[] = []
  for (let t = Math.ceil(start / every) * every; t <= end; t += every) ticks.push(t)
  return ticks
}

export interface PointFigures {
  samples: number
  min: number | null
  avg: number | null
  max: number | null
  latest: number | null
}

/** What a line of a chart says in numbers: the row of its table, the plot's alternative in text. */
export function pointFigures(segments: readonly (readonly ChartPoint[])[]): PointFigures {
  const values = segments.flat().map(p => p.value)
  const n = values.length
  return {
    samples: n,
    min: n ? Math.min(...values) : null,
    avg: n ? values.reduce((a, b) => a + b, 0) / n : null,
    max: n ? Math.max(...values) : null,
    latest: n ? values[n - 1]! : null,
  }
}
