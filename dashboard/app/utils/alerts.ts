import type { Alert, DiskUsage } from '~/types/api'
import { formatBytes } from '~/utils/format'
import type { Tone } from '~/utils/status'

/**
 * Display rules for the agent's alerts and the disk two of them are about.
 * Pure, so the wording is unit-tested. An alert's `message` is a complete
 * sentence from the agent and is shown as it is.
 */

/** Critical is red, a warning amber. */
export function alertTone(alert: Pick<Alert, 'severity'>): Tone {
  return alert.severity === 'critical' ? 'danger' : 'warn'
}

/** The tone of the worst alert in a list; null when the list is empty. */
export function worstAlertTone(alerts: readonly Pick<Alert, 'severity'>[] | null | undefined): Tone | null {
  if (!alerts || alerts.length === 0) return null
  return alerts.some(a => a.severity === 'critical') ? 'danger' : 'warn'
}

/** The alerts about one application; the disk's are about none. */
export function alertsFor<T extends Pick<Alert, 'application'>>(alerts: readonly T[] | null | undefined, application: string): T[] {
  return (alerts ?? []).filter(a => a.application === application)
}

const KIND_LABEL: Record<string, string> = {
  memory: 'Memory',
  disk: 'Disk',
  restarts: 'Restarts',
  unhealthy: 'Unhealthy',
  docker: 'Docker',
}

/** "Memory", "Disk": the kind as a short label. */
export function alertKindLabel(alert: Pick<Alert, 'kind'>): string {
  return KIND_LABEL[alert.kind] ?? alert.kind
}

/** What the alert is about: "my-api replica 1", "my-api", or "the server" for the disk. */
export function alertSubject(alert: Pick<Alert, 'application' | 'replica'>): string {
  if (alert.application === '') return 'the server'
  return alert.replica > 0 ? `${alert.application} replica ${alert.replica}` : alert.application
}

/** Critical first, then oldest first within a severity, which is the agent's order. */
export function sortAlerts<T extends Pick<Alert, 'severity' | 'since'>>(alerts: readonly T[]): T[] {
  const rank = (a: Pick<Alert, 'severity'>) => (a.severity === 'critical' ? 0 : 1)
  return [...alerts].sort((a, b) => rank(a) - rank(b) || a.since.localeCompare(b.since))
}

/** "2 alerts, 1 critical", "1 alert": for the indicator that is on every page. */
export function summarizeAlerts(alerts: readonly Pick<Alert, 'severity'>[]): string {
  const critical = alerts.filter(a => a.severity === 'critical').length
  const total = `${alerts.length} ${alerts.length === 1 ? 'alert' : 'alerts'}`
  return critical > 0 && critical < alerts.length ? `${total}, ${critical} critical` : critical > 0 ? `${total}, critical` : total
}

export interface DiskDisplay {
  /** 0–100, what `df` shows as Use%. */
  percent: number
  /** "35 GB of 40 GB used" */
  used: string
  /** "5 GB free" */
  free: string
  tone: Tone
}

/**
 * The disk meter. Its color follows the agent's own judgement — a `disk`
 * alert, when there is one — rather than thresholds of the dashboard's own:
 * the agent's are configurable. Null where the agent cannot measure the disk.
 */
export function diskDisplay(disk: DiskUsage | null | undefined, alerts: readonly Pick<Alert, 'kind' | 'severity'>[] = []): DiskDisplay | null {
  if (!disk || disk.total_bytes <= 0) return null
  const percent = Math.min(100, Math.max(0, Math.round((disk.used_bytes / disk.total_bytes) * 100)))
  const alert = alerts.find(a => a.kind === 'disk')
  return {
    percent,
    used: `${formatBytes(disk.used_bytes)} of ${formatBytes(disk.total_bytes)} used`,
    free: `${formatBytes(Math.max(0, disk.total_bytes - disk.used_bytes))} free`,
    tone: alert ? alertTone(alert) : 'ok',
  }
}
