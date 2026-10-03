import type { Application } from '~/types/api'
import { hostnameCertificateDisplay } from '~/utils/certificates'
import { pluralize } from '~/utils/format'
import type { Tone } from '~/utils/status'
import { replicaSummary } from '~/utils/status'

/**
 * What the agent says about an application besides its status, in a list:
 * a hostname whose certificate is not in order, and its active alerts. Pure,
 * so the wording is unit-tested. An agent before 0.6 says neither, and an
 * application has no marks.
 */
export interface ApplicationMark {
  key: 'certificate' | 'alerts'
  tone: Tone
  /** "Waiting for DNS", "2 alerts". */
  label: string
  /** The hostname and the agent's message, or where the alerts are listed. */
  title: string
}

type Marked = Pick<Application, 'certificate_problem' | 'alert_count' | 'alert_severity'>

export function applicationMarks(app: Marked): ApplicationMark[] {
  const marks: ApplicationMark[] = []
  const problem = app.certificate_problem
  if (problem) {
    const display = hostnameCertificateDisplay(problem) ?? { tone: 'warn' as Tone, label: 'Certificate' }
    marks.push({ key: 'certificate', tone: display.tone === 'muted' ? 'warn' : display.tone, label: display.label, title: `${problem.hostname}: ${problem.message}` })
  }
  const count = app.alert_count ?? 0
  if (count > 0) {
    marks.push({
      key: 'alerts',
      tone: app.alert_severity === 'critical' ? 'danger' : 'warn',
      label: pluralize(count, 'alert'),
      title: 'The agent\'s alerts about this application are on its page',
    })
  }
  return marks
}

/** One factual sentence about why an application is in the list of those that need attention. */
export function attentionReason(app: Pick<Application, 'status' | 'replicas'> & Marked): string {
  const parts: string[] = []
  switch (app.status) {
    case 'CRASH_LOOP': parts.push(`A replica keeps crashing; ${replicaSummary(app.replicas)}`); break
    case 'DOWN': parts.push(`No healthy replica; ${app.replicas.running}/${app.replicas.desired} running`); break
    case 'DEGRADED': parts.push(replicaSummary(app.replicas)); break
    case 'DEPLOYING': parts.push('First deployment in progress'); break
    case 'FAILED': parts.push('No deployment has succeeded yet'); break
  }
  const problem = app.certificate_problem
  if (problem) {
    const label = hostnameCertificateDisplay(problem)?.label ?? 'certificate not in order'
    parts.push(`${problem.hostname}: ${label.charAt(0).toLowerCase()}${label.slice(1)}`)
  }
  const count = app.alert_count ?? 0
  if (count > 0) parts.push(app.alert_severity === 'critical' ? `${pluralize(count, 'alert')}, critical` : pluralize(count, 'alert'))
  return parts.length > 0 ? parts.join('; ') : replicaSummary(app.replicas)
}
