import type { Alert, ApplicationDetail, Container, Deployment } from '~/types/api'
import { hostnameCertificateDisplay } from '~/utils/certificates'
import type { Tone } from '~/utils/status'

/**
 * What is wrong with an application right now and where to look next: the
 * first thing its page shows. Derived from what the agent reports, never
 * assumed; an application in order has no findings. Pure, so the wording is
 * unit-tested.
 */
export interface Finding {
  /** Stable within one application's list. */
  key: string
  tone: Tone
  title: string
  detail: string
  /** Where the answer usually is. */
  action?: { label: string, to: string }
}

export interface DiagnosisContext {
  /** Newest first, as the agent lists them. */
  deployments: readonly Deployment[]
  /** The agent's alerts about this application: what they already say is not said twice. */
  alerts?: readonly Pick<Alert, 'kind' | 'replica'>[]
  /** Why the domain is not served, when the server's proxy is off or unreachable. */
  proxyProblem?: string
}

const page = (name: string, tab = '') => `/applications/${encodeURIComponent(name)}${tab ? `/${tab}` : ''}`

/** "ran out of memory", "exited with code 137": how a container that is not running ended. */
function ending(c: Pick<Container, 'state' | 'exit_code' | 'oom_killed'>): string {
  if (c.oom_killed) return 'ran out of memory'
  if (c.state === 'exited' || c.state === 'dead') return `exited with code ${c.exit_code}`
  return ''
}

export function diagnose(app: ApplicationDetail, context: DiagnosisContext): Finding[] {
  const findings: Finding[] = []
  const alerts = context.alerts ?? []
  const alerted = (kind: string, replica?: number) => alerts.some(a => a.kind === kind && (replica === undefined || a.replica === replica))
  const logs = app.static ? undefined : { label: 'Read the logs', to: page(app.name, 'logs') }
  const newest = context.deployments[0] ?? null

  if (app.status === 'FAILED') {
    const failed = context.deployments.find(d => d.status === 'FAILED' || d.status === 'ROLLED_BACK') ?? null
    findings.push({
      key: 'never-deployed',
      tone: 'danger',
      title: 'No deployment has succeeded yet',
      detail: failed?.error
        ? `The last attempt, #${failed.sequence}, failed: ${failed.error}. Nothing is running. Fix the cause and run shipwick deploy again.`
        : 'Nothing is running. Run shipwick deploy from the project to try again.',
      action: failed ? { label: `Open deployment #${failed.sequence}`, to: `/deployments/${failed.id}` } : undefined,
    })
  }

  for (const c of app.containers) {
    if (!c.crash_loop || alerted('restarts', c.replica)) continue
    const how = ending(c)
    findings.push({
      key: `crash-${c.replica}`,
      tone: 'danger',
      title: `Replica ${c.replica} keeps crashing`,
      detail: `It was restarted ${c.restarts} ${c.restarts === 1 ? 'time' : 'times'}${how ? ` and last ${how}` : ''}. ${c.oom_killed ? 'Raise resources.memory in deploy.yaml, or find what uses the memory.' : 'Its last output usually says why.'}`,
      action: logs,
    })
  }

  const crashing = app.containers.some(c => c.crash_loop)
  if (app.status === 'DOWN' && !alerted('unhealthy')) {
    findings.push({
      key: 'down',
      tone: 'danger',
      title: 'No replica is healthy',
      detail: `${app.replicas.running} of ${app.replicas.desired} running, none passing the health check. ${app.domain ? 'Visitors get an error until one does.' : 'Nothing answers until one does.'}`,
      action: logs,
    })
  }
  else if (app.status === 'DEGRADED' && !crashing && !alerted('unhealthy')) {
    const broken = app.containers.find(c => c.health === 'unhealthy' || ending(c) !== '')
    findings.push({
      key: 'degraded',
      tone: 'warn',
      title: `${app.replicas.healthy} of ${app.replicas.desired} replicas healthy`,
      detail: broken
        ? `Replica ${broken.replica} ${ending(broken) || 'fails its health check'}. The others keep serving.`
        : 'The others keep serving while the missing ones start.',
      action: logs,
    })
  }

  // An attempt that failed after the version that runs now: the application is fine, the last change is not in.
  if (app.active_deployment && newest && newest.id !== app.active_deployment.id && newest.completed_at && (newest.status === 'FAILED' || newest.status === 'ROLLED_BACK')) {
    findings.push({
      key: `attempt-${newest.id}`,
      tone: 'warn',
      title: `The last deployment failed; ${app.version || 'the previous version'} is still running`,
      detail: `#${newest.sequence}${newest.version ? ` (${newest.version})` : ''} ${newest.status === 'ROLLED_BACK' ? 'failed part-way and was rolled back' : 'failed before anything was replaced'}${newest.error ? `: ${newest.error}` : ''}.`,
      action: { label: `Open deployment #${newest.sequence}`, to: `/deployments/${newest.id}` },
    })
  }

  for (const certificate of app.certificates ?? []) {
    const display = hostnameCertificateDisplay(certificate)
    if (!display || display.tone === 'muted') continue
    findings.push({
      key: `certificate-${certificate.hostname}`,
      tone: display.tone,
      title: `${certificate.hostname}: ${display.label.charAt(0).toLowerCase()}${display.label.slice(1)}`,
      detail: certificate.message ? `${certificate.message.charAt(0).toUpperCase()}${certificate.message.slice(1)}.`.replace(/\.\.$/, '.') : '',
    })
  }

  if (app.domain && context.proxyProblem) {
    findings.push({ key: 'proxy', tone: 'warn', title: `${app.domain} is not being served`, detail: `${context.proxyProblem}.`, action: { label: 'Server status', to: '/servers' } })
  }

  return findings
}

/** One line under the name: the state in words, for an application with nothing wrong as well. */
export function headline(app: ApplicationDetail): string {
  const loop = app.containers.find(c => c.crash_loop)
  if (loop) return `Replica ${loop.replica} keeps crashing`
  if (app.status === 'FAILED') return 'No deployment has succeeded yet'
  if (app.status === 'DEPLOYING') return 'First deployment in progress'
  if (app.status === 'STOPPED') return 'Stopped on request; it stays stopped until it is started'
  if (app.static) return 'Served by the proxy'
  return `${app.replicas.healthy} of ${app.replicas.desired} ${app.replicas.desired === 1 ? 'replica' : 'replicas'} healthy`
}
