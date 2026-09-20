/**
 * Pure formatting helpers. No Vue, no Nuxt, no globals besides Date/Intl, so
 * they can be unit-tested in plain Node.
 */

export const EM_DASH = '—'

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const

/**
 * Formats a byte count with 1024-based units, labelled the way deploy.yaml
 * writes them (`512mb`, `1gb`): 1073741824 is "1 GB", not "1.07 GB".
 */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return EM_DASH
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  // Rounding can carry into the next unit: 1023.97 MB must read "1 GB".
  if (unit > 0 && unit < BYTE_UNITS.length - 1 && Number(value.toFixed(value < 10 ? 1 : 0)) >= 1024) {
    value /= 1024
    unit++
  }
  return `${trimNumber(value, unit === 0 || value >= 10 ? 0 : 1)} ${BYTE_UNITS[unit]}`
}

/** "412 MB / 1 GB", or just "412 MB" when there is no limit (limit = 0). */
export function formatMemoryUsage(used: number | null | undefined, limit: number | null | undefined): string {
  if (used === null || used === undefined) return EM_DASH
  if (!limit || limit <= 0) return formatBytes(used)
  return `${formatBytes(used)} / ${formatBytes(limit)}`
}

/**
 * Formats a percentage. Values under 10 keep one decimal so a mostly idle
 * process does not read as a flat "0%".
 */
export function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return EM_DASH
  const abs = Math.abs(value)
  return `${trimNumber(value, abs < 10 ? 1 : 0)}%`
}

export function formatCores(cores: number | null | undefined): string {
  if (!cores || cores <= 0) return 'unlimited'
  return `${trimNumber(cores, 2)} ${cores === 1 ? 'core' : 'cores'}`
}

/** Formats a span of milliseconds: "850ms", "6.1s", "1m 12s", "2h 5m", "3d 4h". */
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return EM_DASH
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 59_950) return `${trimNumber(ms / 1000, 1)}s`
  const totalSeconds = Math.round(ms / 1000)
  if (totalSeconds < 3600) {
    const m = Math.floor(totalSeconds / 60)
    const s = totalSeconds % 60
    return s === 0 ? `${m}m` : `${m}m ${s}s`
  }
  const totalMinutes = Math.round(totalSeconds / 60)
  if (totalMinutes < 1440) {
    const h = Math.floor(totalMinutes / 60)
    const m = totalMinutes % 60
    return m === 0 ? `${h}h` : `${h}h ${m}m`
  }
  const totalHours = Math.round(totalMinutes / 60)
  const d = Math.floor(totalHours / 24)
  const h = totalHours % 24
  return h === 0 ? `${d}d` : `${d}d ${h}h`
}

/** Milliseconds between two ISO timestamps; null when either is missing or invalid. */
export function durationBetween(start: string | null | undefined, end: string | null | undefined): number | null {
  if (!start || !end) return null
  const a = Date.parse(start)
  const b = Date.parse(end)
  if (Number.isNaN(a) || Number.isNaN(b)) return null
  return Math.max(0, b - a)
}

/**
 * "just now", "12s ago", "2m ago", "3h ago", "5d ago", "4mo ago", "2y ago".
 *
 * Timestamps slightly in the future are treated as "just now": the agent's
 * clock and the browser's are never perfectly aligned.
 */
export function formatRelativeTime(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return EM_DASH
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return EM_DASH
  const diff = now - then
  if (diff < 0) {
    if (diff > -60_000) return 'just now'
    return `in ${relativeUnit(-diff)}`
  }
  if (diff < 5_000) return 'just now'
  return `${relativeUnit(diff)} ago`
}

function relativeUnit(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  const d = Math.floor(h / 24)
  if (d < 30) return `${d}d`
  if (d < 365) return `${Math.floor(d / 30)}mo`
  return `${Math.floor(d / 365)}y`
}

/** "2026-03-01 10:00:00 UTC" */
export function formatAbsoluteUtc(iso: string | null | undefined): string {
  if (!iso) return EM_DASH
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return EM_DASH
  const d = new Date(t)
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())} UTC`
}

/** "10:00:00.120" (UTC), the timestamp column of the log viewer. */
export function formatLogTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const d = new Date(t)
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}.${pad(d.getUTCMilliseconds(), 3)}`
}

/** Splits "ghcr.io/org/app:1.4.2" into repository and tag; digests are kept with the repository. */
export function splitImage(image: string): { repository: string, tag: string } {
  const at = image.indexOf('@')
  const ref = at === -1 ? image : image.slice(0, at)
  const colon = ref.lastIndexOf(':')
  // A colon before the last slash belongs to a registry port, not a tag.
  if (colon === -1 || colon < ref.lastIndexOf('/')) return { repository: image, tag: '' }
  return { repository: ref.slice(0, colon), tag: ref.slice(colon + 1) }
}

export function pluralize(count: number, singular: string, plural: string = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`
}

function pad(n: number, width = 2): string {
  return String(n).padStart(width, '0')
}

/** Fixed decimals with trailing zeros removed: 1.0 → "1", 1.50 → "1.5". */
function trimNumber(value: number, decimals: number): string {
  const fixed = value.toFixed(decimals)
  return decimals > 0 ? fixed.replace(/\.?0+$/, '') : fixed
}
