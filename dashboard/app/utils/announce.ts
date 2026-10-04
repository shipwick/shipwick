import type { BackupRun, Promotion } from '~/types/api'
import { backupBusy } from '~/utils/backups'
import type { DeploymentProgress } from '~/utils/deploymentProgress'
import { promotedDisplay, promotionOutcome, promotionProgress, promotionRunning } from '~/utils/transfer'

/**
 * What a screen reader is told about things that change without a navigation.
 * A region that is live as a whole reads every clock and every poll; these
 * functions pick the sentence that is news. Pure, so they are unit-tested;
 * useAnnounce speaks the result.
 */

export interface Spoken {
  text: string
  at: number
}

/** The same sentence is not said twice within this time: a poll that fails every three seconds is said once. */
export const REPEAT_AFTER_MS = 20_000

export function shouldAnnounce(last: Spoken | null, text: string, now: number): boolean {
  if (text.trim() === '') return false
  return last === null || last.text !== text || now - last.at >= REPEAT_AFTER_MS
}

type ProgressSeen = Pick<DeploymentProgress, 'deploymentId' | 'steps' | 'phase' | 'error'>

/**
 * What changed between two states of a followed deployment: the steps that
 * are new, and the headline when it started, failed or ended. Nothing for a
 * poll that changed nothing — the elapsed time is not news.
 */
export function progressAnnouncement(previous: ProgressSeen | null, next: ProgressSeen, headline: string): string {
  if (previous === null || previous.deploymentId !== next.deploymentId) return headline
  const seen = new Set(previous.steps.map(step => step.id))
  const parts = next.steps
    .filter(step => !seen.has(step.id))
    .map(step => (step.kind === 'warning' ? `Warning: ${step.message}` : step.kind === 'error' ? `Failed: ${step.message}` : step.message))
  const failedNow = previous.error === '' && next.error !== ''
  if (previous.phase !== next.phase || failedNow) {
    parts.push(headline)
    if (next.error !== '' && (failedNow || next.phase === 'failed')) parts.push(next.error)
  }
  return parts.join('. ')
}

/**
 * The backups that were being taken, verified or restored and no longer are,
 * each with how it went. The list is polled; a backup that was at rest in
 * both answers is not mentioned.
 */
export function backupAnnouncement(previous: readonly BackupRun[], next: readonly BackupRun[]): string {
  const busy = new Map(previous.filter(backupBusy).map(run => [run.id, run.activity]))
  return next
    .filter(run => busy.has(run.id) && !backupBusy(run))
    .map((run) => {
      const was = busy.get(run.id)
      if (was === 'verify') return run.verify_error ? `Backup ${run.id} did not restore: ${run.verify_error}` : `Backup ${run.id} verified: it restores`
      if (was === 'restore') return run.restore_error ? `Restoring backup ${run.id} failed: ${run.restore_error}` : `Backup ${run.id} restored`
      return run.status === 'failed' ? `Backup ${run.id} failed: ${run.error}` : `Backup ${run.id} taken`
    })
    .join('. ')
}

/** What changed between two answers about a promotion: the applications whose state moved, and how it ended. */
export function promotionAnnouncement(previous: Promotion | null, next: Promotion): string {
  if (previous === null) return promotionRunning(next) ? promotionProgress(next) : ''
  const before = new Map(previous.applications.map(a => [a.name, a.status]))
  const parts = next.applications
    .filter(a => before.get(a.name) !== a.status)
    .map(a => `${a.name}: ${promotedDisplay(a).label}`)
  if (promotionRunning(previous) && !promotionRunning(next)) parts.push(`Promotion over: ${promotionOutcome(next)}`)
  return parts.join('. ')
}
