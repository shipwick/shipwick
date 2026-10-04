import type { DNSRecord, Import, ImportedApplication, PromotedApplication, Promotion, Standby, StandbyPull } from '~/types/api'
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

/** What is typed to confirm an import that replaces what exists. */
export const OVERWRITE_WORD = 'overwrite'

/** The shortest passphrase the agent writes an export with: it is all that protects every secret of the server. */
export const MIN_PASSPHRASE_LENGTH = 12

/**
 * Why the passphrase of a new export cannot be used yet; "" when it can. It
 * is typed twice because a typo makes a file nobody can open.
 */
export function passphraseProblem(passphrase: string, again: string): string {
  if (passphrase === '') return ''
  if (passphrase.length < MIN_PASSPHRASE_LENGTH) return `At least ${MIN_PASSPHRASE_LENGTH} characters: it is all that protects every secret of the server.`
  if (again !== '' && again !== passphrase) return 'The two passphrases differ.'
  return ''
}

/** Typed twice, the same, and long enough. */
export function passphraseReady(passphrase: string, again: string): boolean {
  return passphrase.length >= MIN_PASSPHRASE_LENGTH && passphrase === again
}

/** A passphrase as the X-Shipwick-Passphrase header of an import carries it: its UTF-8 bytes, base64-encoded. */
export function encodePassphrase(passphrase: string): string {
  let binary = ''
  for (const byte of new TextEncoder().encode(passphrase)) binary += String.fromCharCode(byte)
  return btoa(binary)
}

/** Every file `shipwick export` and the agent write starts with these bytes. */
const EXPORT_MAGIC = 'SWBACKUP'

/** Whether the first bytes of a file are those of an export; anything else is refused before it is uploaded. */
export function looksLikeExport(head: Uint8Array): boolean {
  if (head.length < EXPORT_MAGIC.length) return false
  for (let i = 0; i < EXPORT_MAGIC.length; i++) if (head[i] !== EXPORT_MAGIC.charCodeAt(i)) return false
  return true
}

/** Where the server-side routes that hand an export to the browser live, next to the proxy to the same agent: `/api/export`, `/api/servers/<name>/export`. */
export function exportBase(agentBase: string): string {
  return agentBase.replace(/\/agent$/, '/export')
}

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
  pending: { tone: 'muted', label: 'Waiting' },
  starting: { tone: 'warn', label: 'Starting' },
  running: { tone: 'ok', label: 'Running' },
  // Started, and not ready within its startup budget: it may still come up.
  started: { tone: 'warn', label: 'Started, not ready yet' },
  failed: { tone: 'danger', label: 'Could not be started' },
}

export function promotedDisplay(app: Pick<PromotedApplication, 'status'>): StatusDisplay {
  return PROMOTED[app.status] ?? { tone: 'muted', label: app.status }
}

/**
 * A promotion that has begun and not ended: the agent sets `completed_at`
 * when it is completely done with it. An answer without the field at all is
 * the held request of an agent before 0.6, which only answers at the end.
 */
export function promotionRunning(promotion: Pick<Promotion, 'completed_at'> | null | undefined): boolean {
  return promotion !== null && promotion !== undefined && promotion.completed_at === null
}

/** "Promoting: starting my-api (2 of 3)"; before the first application and after the last, what is going on instead. */
export function promotionProgress(promotion: Pick<Promotion, 'applications'>): string {
  const total = promotion.applications.length
  const index = promotion.applications.findIndex(a => a.status === 'starting')
  if (index !== -1) return `Promoting: starting ${promotion.applications[index]!.name} (${index + 1} of ${total})`
  const waiting = promotion.applications.filter(a => a.status === 'pending').length
  return waiting === total ? 'Promoting: about to start the applications' : waiting === 0 ? 'Promoting: finishing' : `Promoting (${total - waiting} of ${total} started)`
}

/** "3 running", "2 running, 1 not ready yet, 1 could not be started": how a promotion that is over went. */
export function promotionOutcome(promotion: Pick<Promotion, 'applications'>): string {
  const count = (status: string) => promotion.applications.filter(a => a.status === status).length
  const parts = [
    [count('running'), 'running'],
    [count('started'), 'not ready yet'],
    [count('failed'), 'could not be started'],
  ].filter(([n]) => (n as number) > 0).map(([n, words]) => `${n} ${words}`)
  return parts.length > 0 ? parts.join(', ') : 'nothing to start'
}

/**
 * A poll that failed because the agent was away for a moment — restarting, or
 * the proxy in front of it answering for it — is asked again and not shown as
 * a failure: the promotion goes on, and an agent that starts next resumes it.
 */
export function pollRetryable(error: { status: number, code: string }): boolean {
  return error.status === 502 || error.status === 503 || error.status === 504 || error.code === 'NETWORK' || error.code === 'AGENT_UNREACHABLE' || error.code === 'AGENT_TIMEOUT'
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
