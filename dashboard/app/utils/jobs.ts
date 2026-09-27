import type { Run, RunStatus } from '~/types/api'
import { formatRelativeTime } from '~/utils/format'
import { formatArgv } from '~/utils/spec'
import type { StatusDisplay } from '~/utils/status'

/**
 * Display helpers for jobs and their runs. Pure, so the wording and the
 * argv handling are unit-tested.
 */

const RUN_STATUS: Record<RunStatus, StatusDisplay> = {
  running: { tone: 'warn', label: 'Running' },
  succeeded: { tone: 'ok', label: 'Succeeded' },
  failed: { tone: 'danger', label: 'Failed' },
  timed_out: { tone: 'danger', label: 'Timed out' },
  // The agent restarted while it ran: nothing is known about the outcome.
  interrupted: { tone: 'muted', label: 'Interrupted' },
}

export function runStatusDisplay(status: RunStatus | string): StatusDisplay {
  return RUN_STATUS[status as RunStatus] ?? { tone: 'muted', label: status.replace(/_/g, ' ') }
}

/** "Failed (exit 1)", "Failed (did not start)", "Succeeded": the status with what the exit code adds. */
export function describeRunOutcome(run: Pick<Run, 'status' | 'exit_code'>): string {
  const { label } = runStatusDisplay(run.status)
  if (run.status === 'failed') return run.exit_code === null ? `${label} (did not start)` : `${label} (exit ${run.exit_code})`
  return label
}

/** What a run ran: the job's name, "pre-deploy hook", or the command itself for a one-off run. */
export function runTitle(run: Pick<Run, 'job' | 'kind' | 'command'>): string {
  if (run.job === 'pre-deploy') return 'pre-deploy hook'
  if (run.job === 'run') return formatArgv(run.command)
  return run.job
}

/**
 * The argv the API receives from the command editor: one field is one
 * argument, exactly as typed. Only fields left empty are dropped, so a field
 * holding a space is still an argument.
 */
export function argvFromFields(fields: readonly string[]): string[] {
  return fields.filter(field => field !== '')
}

/**
 * "in 2h", "in 3d", "within a minute", "now"; "Not while stopped" for null,
 * which is what the agent answers for a stopped application.
 */
export function formatNextRun(nextRunAt: string | null | undefined, now: number = Date.now()): string {
  if (!nextRunAt) return 'Not while stopped'
  const at = Date.parse(nextRunAt)
  if (Number.isNaN(at)) return '—'
  if (at <= now) return 'now'
  const relative = formatRelativeTime(nextRunAt, now)
  return relative === 'just now' ? 'within a minute' : relative
}

/** Go prints "1h0m0s" and "10m0s"; the zero components say nothing. "1m30s" and "0s" stay as they are. */
export function tidyDuration(duration: string): string {
  const parts = [...duration.matchAll(/(\d+(?:\.\d+)?)(h|m|s|ms)/g)]
  if (parts.length === 0) return duration
  const kept = parts.filter(([, n]) => Number(n) !== 0).map(([whole]) => whole)
  return kept.length > 0 ? kept.join('') : parts[parts.length - 1]![0]
}
