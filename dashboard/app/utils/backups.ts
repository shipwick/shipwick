import type { BackupRun, BackupStatus, SpecBackups } from '~/types/api'
import { formatBytes, formatRelativeTime } from '~/utils/format'
import { formatArgv } from '~/utils/spec'
import type { StatusDisplay, Tone } from '~/utils/status'

/**
 * Display rules for the backups the agent takes: of an application's volumes,
 * and of its own state. Pure, so the wording is unit-tested; where the CLI
 * prints the same thing (`shipwick status`, `shipwick doctor`) the wording is
 * the CLI's.
 */

/** "daily at 03:00 UTC", "hourly at :15", or the expression itself for anything else. */
export function describeSchedule(expression: string): string {
  const fields = expression.trim().split(/\s+/)
  if (fields.length === 5 && fields[2] === '*' && fields[3] === '*' && fields[4] === '*') {
    const minute = /^\d+$/.test(fields[0]!) ? Number(fields[0]) : null
    const hour = /^\d+$/.test(fields[1]!) ? Number(fields[1]) : null
    if (minute !== null && hour !== null) return `daily at ${pad(hour)}:${pad(minute)} UTC`
    if (minute !== null && fields[1] === '*') return `hourly at :${pad(minute)}`
  }
  return `on ${expression} (UTC)`
}

/** "daily at 03:00 UTC, 7 kept, after pg_dump -f /data/dump.sql, with the application stopped": what the `backups` block has the server do. */
export function describeBackupPlan(backups: SpecBackups): string {
  let plan = `${describeSchedule(backups.schedule)}, ${backups.keep} kept`
  if (backups.before?.length) plan += `, after ${formatArgv(backups.before)}`
  if (backups.stop) plan += ', with the application stopped'
  return plan
}

/** The archives of a backup added up; they are measured before encryption. */
export function backupSize(run: Pick<BackupRun, 'volumes'>): number {
  return (run.volumes ?? []).reduce((sum, v) => sum + v.size_bytes, 0)
}

/** "local + s3", "local"; "—" for a backup that kept nothing. */
export function describeDestinations(run: Pick<BackupRun, 'destinations'>): string {
  return run.destinations?.length ? run.destinations.join(' + ') : '—'
}

/** Being taken, verified or restored: the agent answers 409 BACKUP_BUSY to anything else asked of it. */
export function backupBusy(run: Pick<BackupRun, 'status' | 'activity'>): boolean {
  return run.status === 'running' || run.activity !== ''
}

/** Only a backup that succeeded can be verified, restored or downloaded; nothing was kept of the others. */
export function backupUsable(run: Pick<BackupRun, 'status'>): boolean {
  return run.status === 'succeeded'
}

/** The status of a row, with what is being done to the backup in front of it. */
export function backupStatusDisplay(run: Pick<BackupRun, 'status' | 'activity'>): StatusDisplay {
  if (run.activity === 'verify') return { tone: 'warn', label: 'Verifying…' }
  if (run.activity === 'restore') return { tone: 'warn', label: 'Restoring…' }
  if (run.status === 'running') return { tone: 'warn', label: 'Running' }
  if (run.status === 'succeeded') return { tone: 'ok', label: 'Succeeded' }
  if (run.status === 'failed') return { tone: 'danger', label: 'Failed' }
  return { tone: 'muted', label: run.status }
}

/** What is known about whether the backup restores: proven, failed to, or never tried. */
export function verificationDisplay(run: Pick<BackupRun, 'verified_at' | 'verify_error'>, now: number = Date.now()): StatusDisplay | null {
  if (run.verify_error) return { tone: 'danger', label: 'Did not restore' }
  if (run.verified_at) return { tone: 'ok', label: `Verified ${formatRelativeTime(run.verified_at, now)}` }
  return null
}

export interface BackupSummary {
  tone: Tone
  text: string
}

/**
 * One line about how an application's backups stand, as `shipwick status`
 * prints it: "daily at 03:00 UTC, last 5h ago (2.1 GB), 7 kept", or the
 * failure. `runs` is the listing, newest first.
 */
export function describeBackups(backups: SpecBackups | null | undefined, runs: readonly BackupRun[], now: number = Date.now()): BackupSummary {
  const settled = runs.filter(r => r.status !== 'running')
  const latest = settled[0] ?? null
  const succeeded = settled.filter(r => r.status === 'succeeded')
  const good = succeeded[0] ?? null

  if (!backups && !latest) return { tone: 'warn', text: 'None scheduled and none taken. Add `backups` to deploy.yaml, or take one now.' }
  const when = backups ? describeSchedule(backups.schedule) : 'none scheduled'
  if (!latest) return { tone: 'muted', text: `${when}, none taken yet` }
  if (latest.status === 'failed' || !good) {
    const tail = good ? ` (last good one ${formatRelativeTime(good.started_at, now)})` : ''
    return { tone: 'danger', text: `${when}, last one failed ${formatRelativeTime(latest.started_at, now)}: ${truncate(latest.error, 120)}${tail}` }
  }
  return { tone: 'ok', text: `${when}, last ${formatRelativeTime(good.started_at, now)} (${formatBytes(backupSize(good))}), ${succeeded.length} kept` }
}

export interface StateBackupDisplay extends StatusDisplay {
  /** A sentence: what is the case and, where something is missing, what to set. */
  detail: string
  /** A backup of the state can be asked for now: without a passphrase the agent answers 409 BACKUPS_NOT_ENCRYPTED. */
  possible: boolean
}

/**
 * How the agent's own state — its database and the key that encrypts the
 * secrets in it — is backed up, in the sentences `shipwick doctor` prints.
 */
export function stateBackupDisplay(status: BackupStatus, now: number = Date.now()): StateBackupDisplay {
  if (!status.encrypted) {
    return {
      tone: 'warn',
      label: 'Not backed up',
      detail: 'The encryption key exists only on this server. Set SHIPWICK_BACKUP_PASSPHRASE (and an S3 bucket) in /opt/shipwick/.env to back it up; losing it loses every secret.',
      possible: false,
    }
  }
  if (status.state_error && status.state_last_at) {
    return {
      tone: 'danger',
      label: 'Last backup failed',
      detail: `${sentence(status.state_error)} The last good one is from ${formatRelativeTime(status.state_last_at, now)}.`,
      possible: true,
    }
  }
  if (status.state_error) {
    return { tone: 'danger', label: 'Never backed up', detail: sentence(status.state_error), possible: true }
  }
  if (!status.state_last_at) {
    return {
      tone: 'warn',
      label: 'Not backed up yet',
      detail: 'The first backup is taken within a minute of the agent starting.',
      possible: true,
    }
  }
  if (status.destination !== 's3') {
    return {
      tone: 'ok',
      label: `Backed up ${formatRelativeTime(status.state_last_at, now)}`,
      detail: 'To the server\'s own disk. Set SHIPWICK_BACKUP_S3_* in /opt/shipwick/.env to keep a copy elsewhere.',
      possible: true,
    }
  }
  return { tone: 'ok', label: `Backed up ${formatRelativeTime(status.state_last_at, now)}`, detail: 'To the S3 bucket, encrypted.', possible: true }
}

/** Where backups go, for the server page: "S3 bucket", "the server's own disk", "nowhere". */
export function describeDestination(destination: string): string {
  if (destination === 's3') return 'S3 bucket and the server\'s own disk'
  if (destination === 'local') return 'The server\'s own disk'
  if (destination === 'none') return 'Nowhere'
  return destination
}

function sentence(text: string): string {
  const trimmed = text.trim()
  if (trimmed === '') return ''
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
