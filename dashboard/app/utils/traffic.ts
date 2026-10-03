import type { RequestLine, Traffic, TrafficCounts, TrafficPoint, TrafficRange } from '~/types/api'
import type { ChartPoint } from '~/utils/metricsHistory'
import { RANGE_WINDOWS } from '~/utils/metricsHistory'
import type { Tone } from '~/utils/status'

/**
 * Turns the agent's traffic series into something a chart can draw. Pure: the
 * component only maps the numbers here onto pixels.
 *
 * The series is sparse the other way round from the metrics history: a step
 * without a point is a step without a request, which is a zero and is drawn as
 * one. Only the latency has gaps, since no request means no duration to
 * report, not a duration of zero.
 */

/** The step the agent uses for each range, for when a response does not say. */
const STEP_SECONDS: Record<TrafficRange, number> = { '1h': 60, '24h': 300, '7d': 3600 }

export interface TrafficChartData {
  /** The x axis: the window as the agent reported it, to the end. */
  start: number
  end: number
  stepMs: number
  /** One point per step of the window, zeros included. */
  requests: ChartPoint[]
  /** The 5xx answers among them, same steps. */
  errors: ChartPoint[]
  /** p95 of the steps that had requests, split where a step had none. */
  p95: ChartPoint[][]
  /** The y axes span 0..max; never 0, so an empty window still has a scale. */
  maxRequests: number
  maxP95: number
}

/**
 * Chart data for the window `since` → `end` (now by default), in the agent's
 * steps. Points outside the window, or off the step grid, are dropped rather
 * than drawn in the wrong place.
 */
export function toTrafficChart(traffic: Traffic, range: TrafficRange, end: number = Date.now()): TrafficChartData {
  const stepMs = (traffic.step_seconds > 0 ? traffic.step_seconds : STEP_SECONDS[range]) * 1000
  const since = Date.parse(traffic.since)
  const start = Number.isNaN(since) ? Math.floor((end - RANGE_WINDOWS[range].windowMs) / stepMs) * stepMs : since

  const byStep = new Map<number, TrafficPoint>()
  for (const p of traffic.points ?? []) {
    const t = Date.parse(p.t)
    if (!Number.isNaN(t)) byStep.set(t, p)
  }

  const requests: ChartPoint[] = []
  const errors: ChartPoint[] = []
  const p95: ChartPoint[][] = []
  let segment: ChartPoint[] = []
  let maxRequests = 0
  let maxP95 = 0
  for (let t = start; t <= end; t += stepMs) {
    const p = byStep.get(t)
    const count = p?.requests ?? 0
    requests.push({ t, value: count })
    errors.push({ t, value: p?.status_5xx ?? 0 })
    maxRequests = Math.max(maxRequests, count)
    if (p && count > 0) {
      segment.push({ t, value: p.p95_ms })
      maxP95 = Math.max(maxP95, p.p95_ms)
    }
    else if (segment.length > 0) {
      p95.push(segment)
      segment = []
    }
  }
  if (segment.length > 0) p95.push(segment)

  return {
    start: Math.min(start, end),
    end,
    stepMs,
    requests,
    errors,
    p95,
    maxRequests: headroom(maxRequests),
    maxP95: headroom(maxP95),
  }
}

/** A ceiling a little above the peak, so the highest step is not drawn on the frame; 1 for an empty series. */
function headroom(peak: number): number {
  return peak <= 0 ? 1 : peak * 1.15
}

/** "12,480": a count as a person reads it. */
export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return '—'
  return Math.round(n).toLocaleString('en-US')
}

/** "0.8 ms", "12 ms", "480 ms", "1.2 s", "30 s": a duration the proxy measured, given in milliseconds. */
export function formatLatency(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 10) return `${trim(ms, 1)} ms`
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${trim(ms / 1000, ms < 10_000 ? 1 : 0)} s`
}

function trim(value: number, decimals: number): string {
  const fixed = value.toFixed(decimals)
  return decimals > 0 ? fixed.replace(/\.?0+$/, '') : fixed
}

/** The share of requests answered 5xx: "0%", "0.16%", "12%"; "—" when there were no requests. */
export function errorShare(counts: Pick<TrafficCounts, 'requests' | 'status_5xx'>): string {
  if (counts.requests <= 0) return '—'
  if (counts.status_5xx <= 0) return '0%'
  const share = (counts.status_5xx / counts.requests) * 100
  if (share < 0.01) return '<0.01%'
  return `${trim(share, share < 10 ? 2 : 0)}%`
}

/** Whether a window had any traffic at all. */
export function hasTraffic(traffic: Pick<Traffic, 'totals'> | null | undefined): boolean {
  return (traffic?.totals.requests ?? 0) > 0
}

/** The color of a status code by its class: 5xx red, 4xx amber, 2xx green, the rest gray. */
export function statusTone(status: number): Tone {
  if (status >= 500 && status <= 599) return 'danger'
  if (status >= 400 && status <= 499) return 'warn'
  if (status >= 200 && status <= 299) return 'ok'
  return 'muted'
}

/** The agent answers oldest first; a table of recent requests reads newest first. */
export function newestFirst(requests: readonly RequestLine[]): RequestLine[] {
  return [...requests].reverse()
}
