import type { DNSRecord, Import, ImportedApplication, PromotedApplication, Standby, StandbyPull } from '~/types/api'
import { describeSchedule } from '~/utils/backups'
import { formatRelativeTime, pluralize } from '~/utils/format'
import type { StatusDisplay } from '~/utils/status'

/**
 * Display rules for moving a server: exports, the import that reads one, and
 * the standby that holds applications stopped until it is promoted. Pure, so
 * the wording is unit-tested.
 */

/** What is typed to confirm a promotion, as `shipwick standby promote` asks for it. */
export const PROMOTE_WORD = 'promote'

/** The server is, or has been made, a standby: it holds stopped applications, or fetches exports on a schedule. */
export function isStandby(standby: Standby | null | undefined): boolean {
  return Boolean(standby) && (standby!.applications.length > 0 || standby!.pull !== null)
}

/** "An import is running: my-api (2 of 4)"; without an application in work yet, what it is reading. */
export function importProgress(run: Pick<Import, 'applications'>): string {
  const total = run.applications.length
  if (total === 0) return 'An import is running: reading the export'
  const index = run.applications.findIndex(a => a.status === 'importing')
  if (index !== -1) return `An import is running: ${run.applications[index]!.name} (${index + 1} of ${total})`
  const done = run.applications.filter(a => a.status !== 'pending').length
  return done >= total ? 'An import is running: finishing' : `An import is running (${done} of ${total} done)`
}

/** "3 imported, 1 skipped, 1 failed": how an import that is over went, by application. */
export function importOutcome(run: Pick<Import, 'applications'>): string {
  const count = (status: string) => run.applications.filter(a => a.status === status).length
  const parts = [
    [count('imported'), 'imported'],
    [count('skipped'), 'skipped'],
    [count('failed'), 'failed'],
  ].filter(([n]) => (n as number) > 0).map(([n, word]) => `${n} ${word}`)
  return parts.length > 0 ? parts.join(', ') : 'no applications'
}

/** "2 secrets, 1 registry credential": what an import stored besides applications; "" when nothing. */
export function importStored(run: Pick<Import, 'secrets' | 'registries' | 'certificates'>): string {
  const parts: string[] = []
  if (run.secrets > 0) parts.push(pluralize(run.secrets, 'secret'))
  if (run.registries > 0) parts.push(pluralize(run.registries, 'registry credential'))
  if (run.certificates > 0) parts.push(pluralize(run.certificates, 'certificate'))
  return parts.join(', ')
}

const IMPORTED: Record<string, StatusDisplay> = {
  pending: { tone: 'muted', label: 'Waiting' },
  importing: { tone: 'warn', label: 'Importing' },
  imported: { tone: 'ok', label: 'Imported' },
  // Left as it is on purpose: it exists here, or runs here. Not a failure.
  skipped: { tone: 'muted', label: 'Skipped' },
  failed: { tone: 'danger', label: 'Failed' },
}

export function importedDisplay(app: Pick<ImportedApplication, 'status'>): StatusDisplay {
  return IMPORTED[app.status] ?? { tone: 'muted', label: app.status }
}

const PROMOTED: Record<string, StatusDisplay> = {
  running: { tone: 'ok', label: 'Running' },
  // Started, and not ready within its startup budget: it may still come up.
  started: { tone: 'warn', label: 'Started, not ready yet' },
  failed: { tone: 'danger', label: 'Could not be started' },
}

export function promotedDisplay(app: Pick<PromotedApplication, 'status'>): StatusDisplay {
  return PROMOTED[app.status] ?? { tone: 'muted', label: app.status }
}

/** "hourly at :15, last 12m ago (export #42)"; says so when nothing has been fetched yet. */
export function describePull(pull: StandbyPull, now: number = Date.now()): string {
  const schedule = describeSchedule(pull.schedule)
  if (!pull.last_at) return `${schedule}, nothing fetched yet`
  const which = pull.last_export > 0 ? ` (export #${pull.last_export})` : ''
  return `${schedule}, last ${formatRelativeTime(pull.last_at, now)}${which}`
}

/** What a record's value column shows: the address, or that the agent does not know its own. */
export function recordValue(record: Pick<DNSRecord, 'value'>): string {
  return record.value || 'this server\'s address'
}
