import type { ApplicationStatus, Container, LogArchiveEntry, LogMatch, Server } from '~/types/api'
import { formatBytes, formatRelativeTime, pluralize } from '~/utils/format'
import type { Tone } from '~/utils/status'

/**
 * The output of containers that ended, which the agent keeps from 0.7 on, and
 * the search over it and the running replicas. Pure, so the wording and the
 * choice of what the Logs tab opens on are unit-tested.
 */

/** The four things the Logs tab shows; each has an address (`?view=`). */
export type LogView = 'live' | 'previous' | 'archive' | 'search'

export const LOG_VIEWS: readonly { view: LogView, label: string, about: string }[] = [
  { view: 'live', label: 'Live', about: 'What the replicas write now, as they write it.' },
  { view: 'previous', label: 'Previous', about: 'The last output of the container that ended most recently: where a crash says why.' },
  { view: 'archive', label: 'Archive', about: 'The kept output of every container that ended: crashed, restarted, stopped or replaced, and every run.' },
  { view: 'search', label: 'Search', about: 'Looks for a text in the running replicas and in everything that is kept.' },
]

/** `?view=` of the address; null for none, or one this page does not know. */
export function logViewOf(value: unknown): LogView | null {
  const first = Array.isArray(value) ? value[0] : value
  return LOG_VIEWS.some(v => v.view === first) ? first as LogView : null
}

/** A positive whole number from the address (`?entry=364`); null for anything else. */
export function positiveInt(value: unknown): number | null {
  const first = Array.isArray(value) ? value[0] : value
  return typeof first === 'string' && /^[1-9]\d{0,15}$/.test(first) ? Number(first) : null
}

/** The agent keeps an archive: it says so on GET /server from 0.7 on. An agent before that has only what its containers still hold. */
export function archiveSupported(server: Pick<Server, 'log_archive'> | null | undefined): boolean {
  return server?.log_archive !== undefined
}

type Ended = Pick<LogArchiveEntry, 'kind' | 'reason' | 'exit_code' | 'oom_killed'>

/** The container died, or was ended because something was wrong with it: what somebody who asks "why did it die?" is looking for. */
export function endedBadly(entry: Ended): boolean {
  if (entry.kind === 'run') return entry.reason === 'failed' || entry.reason === 'timed_out'
  if (entry.reason === 'crashed' || entry.reason === 'oom_killed' || entry.reason === 'unhealthy') return true
  // A replica of a failed deployment that was still running when it was removed did nothing wrong itself.
  return entry.reason === 'deployment_failed' && (entry.oom_killed || (entry.exit_code !== null && entry.exit_code !== 0))
}

const exit = (code: number | null) => (code === null ? '' : ` (exit ${code})`)

/**
 * How a container ended, as the rest of a sentence about it: "ran out of
 * memory (exit 137)", "was replaced by a newer deployment".
 */
export function endedBecause(entry: Ended): string {
  if (entry.kind === 'run') {
    switch (entry.reason) {
      case 'succeeded': return 'succeeded'
      case 'failed': return `failed${exit(entry.exit_code)}`
      case 'timed_out': return 'timed out and was stopped'
      case 'interrupted': return 'was interrupted by a restart of the agent'
      default: return entry.reason
    }
  }
  if (entry.oom_killed && entry.reason !== 'deployment_failed') return `ran out of memory${exit(entry.exit_code)}`
  switch (entry.reason) {
    case 'crashed': return `crashed${exit(entry.exit_code)}`
    case 'exited': return `exited by itself${exit(entry.exit_code)}`
    case 'oom_killed': return `ran out of memory${exit(entry.exit_code)}`
    case 'unhealthy': return 'was restarted for failing its health check'
    case 'stopped': return 'was stopped with the application'
    case 'replaced': return 'was replaced by a newer deployment'
    case 'deployment_failed':
      if (entry.oom_killed) return `ran out of memory${exit(entry.exit_code)} and failed its deployment`
      return entry.exit_code !== null && entry.exit_code !== 0 ? `exited${exit(entry.exit_code)} and failed its deployment` : 'was removed when its deployment failed'
    case 'removed': return 'was removed as a leftover'
    case 'restarted': return 'was restarted; how it ended was not seen'
    default: return entry.reason
  }
}

export function endedTone(entry: Ended): Tone {
  return endedBadly(entry) ? 'danger' : 'muted'
}

type Source = Pick<LogArchiveEntry, 'kind' | 'deployment' | 'replica' | 'job' | 'run_id'>

/** Whose output it is: "#3 replica 2", "run 12 of nightly-report", "run 4, a command", "run 9, the pre-deploy command". */
export function outputOf(entry: Source): string {
  if (entry.kind === 'run') {
    const run = entry.run_id === null ? 'a run' : `run ${entry.run_id}`
    if (entry.job === 'run') return `${run}, a command`
    if (entry.job === 'pre-deploy') return `${run}, the pre-deploy command`
    return entry.job ? `${run} of ${entry.job}` : run
  }
  return entry.deployment > 0 ? `#${entry.deployment} replica ${entry.replica}` : `replica ${entry.replica}`
}

/** "153 lines", "1 line", "last 2,000 lines", "no output". */
export function linesKept(entry: Pick<LogArchiveEntry, 'lines' | 'truncated'>): string {
  if (entry.lines === 0) return 'no output'
  const count = `${entry.lines.toLocaleString('en-US')} ${entry.lines === 1 ? 'line' : 'lines'}`
  return entry.truncated ? `last ${count}` : count
}

/** The subject of an entry as a sentence starts with it: "Replica 2 of #3 (1.4.2)", "Run 12 of nightly-report". */
export function entrySubject(entry: Source & Pick<LogArchiveEntry, 'version'>): string {
  if (entry.kind === 'run') {
    const text = outputOf(entry)
    return text.charAt(0).toUpperCase() + text.slice(1)
  }
  if (entry.deployment === 0) return `Replica ${entry.replica}`
  return `Replica ${entry.replica} of #${entry.deployment}${entry.version ? ` (${entry.version})` : ''}`
}

/** "Replica 2 of #3 (1.4.2) · crashed (exit 1) · 5m ago · 153 lines": an entry in one line. */
export function entryHeadline(entry: LogArchiveEntry, now: number = Date.now()): string {
  return [entrySubject(entry), endedBecause(entry), formatRelativeTime(entry.ended_at, now), linesKept(entry)].join(' · ')
}

/** "197 KB kept" with what it takes on the server's disk when that differs. */
export function entrySize(entry: Pick<LogArchiveEntry, 'bytes' | 'lines'>): string {
  return entry.lines === 0 ? '' : formatBytes(entry.bytes)
}

const UNHEALTHY: readonly ApplicationStatus[] = ['DEGRADED', 'DOWN', 'CRASH_LOOP', 'FAILED']

/** How recent a crash has to be to be mentioned above the live output of an application that is healthy again. */
export const RECENT_CRASH_MS = 24 * 60 * 60 * 1000

export interface LogViewChoice {
  /** What the tab opens on when the address names no view. */
  view: 'live' | 'previous'
  /** Why it opened on Previous, or what is worth knowing above Live; "" when nothing is. */
  note: string
}

/**
 * What somebody who opens the Logs tab most likely came for. An application
 * that is not healthy and whose last container to end died of something opens
 * on that container's output: the crash is on screen without a mode having
 * been chosen. One that has no containers opens on what its last one wrote.
 * Otherwise it is the live output, with a line above it when a replica died
 * within the last day.
 */
export function chooseLogView(
  app: { name: string, status: ApplicationStatus | string, containers: number },
  newest: LogArchiveEntry | null,
  now: number = Date.now(),
): LogViewChoice {
  if (!newest) return { view: 'live', note: '' }
  const what = `${entrySubject(newest).replace(/^Replica/, 'replica')} ${endedBecause(newest)} ${formatRelativeTime(newest.ended_at, now)}`
  if (endedBadly(newest) && UNHEALTHY.includes(app.status as ApplicationStatus)) {
    return { view: 'previous', note: `${app.name} is not healthy, and ${what}. This is what it wrote last.` }
  }
  if (app.containers === 0) {
    return { view: 'previous', note: `${app.name} has no containers now. This is what the last one wrote before it ended.` }
  }
  const age = now - Date.parse(newest.ended_at)
  if (endedBadly(newest) && age >= 0 && age <= RECENT_CRASH_MS) {
    return { view: 'live', note: `${what.charAt(0).toUpperCase()}${what.slice(1)}.` }
  }
  return { view: 'live', note: '' }
}

/** A replica that restarted, keeps crashing or lies dead: the row of the replicas table that gets a "last output" link. */
export function hasDied(container: Pick<Container, 'restarts' | 'crash_loop' | 'oom_killed' | 'exit_code' | 'state' | 'stopping'>): boolean {
  if (container.stopping) return false
  return container.restarts > 0 || container.crash_loop || container.oom_killed || (container.state !== 'running' && container.exit_code !== 0)
}

/** The newest kept output of a container's own crash, restart for its health, or death by memory; null when nothing of it is kept. */
export function lastOutputEntry(entries: readonly LogArchiveEntry[], container: string): LogArchiveEntry | null {
  let newest: LogArchiveEntry | null = null
  for (const entry of entries) {
    if (entry.container !== container || !['crashed', 'oom_killed', 'unhealthy'].includes(entry.reason)) continue
    if (!newest || entry.id > newest.id) newest = entry
  }
  return newest
}

/** The lines one container wrote, among what a search found: read from a container that exists, or from one kept entry. */
export interface LogGroup {
  key: string
  container: string
  archiveId: number | null
  deployment: number
  replica: number
  job: string
  runId: number | null
  /** In reading order: oldest first. */
  lines: LogMatch[]
}

/**
 * A search answers source by source, newest line first, and a source may go
 * on in the next page. Grouped here by source in the order they were first
 * seen, each group's lines turned into reading order.
 */
export function groupMatches(lines: readonly LogMatch[]): LogGroup[] {
  const groups = new Map<string, LogGroup>()
  for (const line of lines) {
    const key = line.archive_id === null ? `c:${line.container}` : `a:${line.archive_id}`
    let group = groups.get(key)
    if (!group) {
      group = { key, container: line.container, archiveId: line.archive_id, deployment: line.deployment, replica: line.replica, job: line.job, runId: line.run_id, lines: [] }
      groups.set(key, group)
    }
    group.lines.push(line)
  }
  return [...groups.values()].map(group => ({ ...group, lines: [...group.lines].reverse() }))
}

/** What a group of matches is the output of: "replica 3 of #2", "run 12 of nightly-report". */
export function groupSource(group: Pick<LogGroup, 'deployment' | 'replica' | 'job' | 'runId'>): string {
  if (group.runId !== null || group.job !== '') return outputOf({ kind: 'run', deployment: group.deployment, replica: 0, job: group.job, run_id: group.runId })
  return group.deployment > 0 ? `replica ${group.replica} of #${group.deployment}` : `replica ${group.replica}`
}

export type SearchWindow = '' | '1h' | '24h' | '7d' | 'custom'

export const SEARCH_WINDOWS: readonly { value: SearchWindow, label: string }[] = [
  { value: '', label: 'Any time' },
  { value: '1h', label: 'Last hour' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: 'custom', label: 'Between…' },
]

const WINDOW_MS: Record<string, number> = { '1h': 60 * 60 * 1000, '24h': 24 * 60 * 60 * 1000, '7d': 7 * 24 * 60 * 60 * 1000 }

/**
 * `since` and `until` of a search, RFC 3339, from the chosen window. A custom
 * window is two values of a datetime-local field, read in the browser's time
 * zone; `problem` says why they cannot be used.
 */
export function searchBounds(window: SearchWindow, from: string, to: string, now: number = Date.now()): { since?: string, until?: string, problem: string } {
  if (window === '') return { problem: '' }
  if (window !== 'custom') return { since: new Date(now - WINDOW_MS[window]!).toISOString(), problem: '' }
  const start = from === '' ? null : Date.parse(from)
  const end = to === '' ? null : Date.parse(to)
  if ((start !== null && Number.isNaN(start)) || (end !== null && Number.isNaN(end))) return { problem: 'Not a time: choose a day and a time.' }
  if (start === null && end === null) return { problem: 'Choose where the search starts, where it ends, or both.' }
  if (start !== null && end !== null && end < start) return { problem: 'The end is before the start.' }
  return { ...(start === null ? {} : { since: new Date(start).toISOString() }), ...(end === null ? {} : { until: new Date(end).toISOString() }), problem: '' }
}

/** A text the agent searches for: one line of at most 256 bytes. "" when it can be sent. */
export function searchTextProblem(text: string): string {
  if (/[\r\n]/.test(text)) return 'One line only: a search looks inside lines.'
  if (new TextEncoder().encode(text).length > 256) return 'At most 256 bytes.'
  return ''
}

/** "42 lines in 3 containers", "No line matches": what a search found, said once when it stops. */
export function searchSummary(lines: number, groups: number, more: boolean): string {
  if (lines === 0) return more ? 'Nothing found yet' : 'No line matches'
  return `${pluralize(lines, 'line')} in ${pluralize(groups, 'container')}${more ? ', and there is more to search' : ''}`
}

/** "64 MB of 1 GB, 278 entries, kept 14 days": the archive on the server's Status tab; that it is off, when it is. */
export function describeLogArchive(status: NonNullable<Server['log_archive']>): string {
  if (!status.enabled) return 'Off: nothing is kept of a container that ended'
  return `${formatBytes(status.bytes)} of ${formatBytes(status.max_bytes)}, ${pluralize(status.entries, 'entry', 'entries')}, kept ${pluralize(status.retention_days, 'day')}`
}

/** The address of a view of an application's Logs tab. */
export function logsPath(application: string, query: Record<string, string | number | null | undefined> = {}): { path: string, query: Record<string, string> } {
  const clean: Record<string, string> = {}
  for (const [key, value] of Object.entries(query)) if (value !== null && value !== undefined && value !== '') clean[key] = String(value)
  return { path: `/applications/${encodeURIComponent(application)}/logs`, query: clean }
}
