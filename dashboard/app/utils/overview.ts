import type { Alert, Application, ApplicationStatus } from '~/types/api'
import { pluralize } from '~/utils/format'
import type { Tone } from '~/utils/status'
import { applicationStatusDisplay, needsAttention } from '~/utils/status'

/**
 * The answer to "is everything fine?", in one sentence, from the applications
 * and the agent's alerts. Pure, so the wording is unit-tested.
 */
export interface Verdict {
  tone: Tone
  title: string
  /** The counts behind the title: "4 healthy, 1 degraded, 1 stopped". */
  detail: string
}

const ORDER: ApplicationStatus[] = ['HEALTHY', 'DEGRADED', 'DOWN', 'CRASH_LOOP', 'FAILED', 'DEPLOYING', 'STOPPED']
const BROKEN: ReadonlySet<string> = new Set(['DOWN', 'CRASH_LOOP', 'FAILED'])

export function statusCounts(apps: readonly Pick<Application, 'status'>[]): { status: ApplicationStatus, count: number }[] {
  return ORDER.map(status => ({ status, count: apps.filter(a => a.status === status).length }))
}

export function verdict(apps: readonly Pick<Application, 'status' | 'certificate_problem' | 'alert_count' | 'alert_severity'>[], alerts: readonly Pick<Alert, 'severity' | 'application'>[] = []): Verdict {
  if (apps.length === 0) {
    return { tone: 'muted', title: 'Nothing is deployed yet', detail: 'This server is ready for its first application.' }
  }
  const detail = statusCounts(apps)
    .filter(c => c.count > 0)
    .map(c => `${c.count} ${applicationStatusDisplay(c.status).label.toLowerCase()}`)
    .join(', ')

  const troubled = apps.filter(a => needsAttention(a) && a.status !== 'DEPLOYING')
  const deploying = apps.filter(a => a.status === 'DEPLOYING').length
  // An alert about an application that already counts as troubled is the same trouble; the disk's is its own.
  const serverAlerts = alerts.filter(a => a.application === '')
  const critical = alerts.some(a => a.severity === 'critical') || apps.some(a => a.alert_severity === 'critical')

  if (troubled.length > 0) {
    return {
      tone: critical || troubled.some(a => BROKEN.has(a.status)) ? 'danger' : 'warn',
      title: `${pluralize(troubled.length, 'application needs', 'applications need')} attention`,
      detail,
    }
  }
  if (serverAlerts.length > 0 || alerts.length > 0) {
    return {
      tone: critical ? 'danger' : 'warn',
      title: serverAlerts.length > 0 ? 'The server needs attention' : `Applications are healthy, with ${pluralize(alerts.length, 'warning')}`,
      detail,
    }
  }
  if (deploying > 0) {
    return { tone: 'warn', title: `${pluralize(deploying, 'first deployment')} in progress; everything else is healthy`, detail }
  }
  return { tone: 'ok', title: 'Everything that should be running is healthy', detail }
}
