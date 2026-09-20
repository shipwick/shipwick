import type {
  Application,
  ApplicationStatus,
  Container,
  DeploymentStatus,
  EventLevel,
  ReplicaHealth,
} from '~/types/api'

/**
 * The four meanings color is allowed to carry in this UI.
 * ok = green, warn = amber, danger = red, muted = gray.
 */
export type Tone = 'ok' | 'warn' | 'danger' | 'muted'

export interface StatusDisplay {
  tone: Tone
  label: string
}

const APPLICATION_STATUS: Record<ApplicationStatus, StatusDisplay> = {
  HEALTHY: { tone: 'ok', label: 'Healthy' },
  DEGRADED: { tone: 'warn', label: 'Degraded' },
  DOWN: { tone: 'danger', label: 'Down' },
  CRASH_LOOP: { tone: 'danger', label: 'Crash loop' },
  STOPPED: { tone: 'muted', label: 'Stopped' },
  DEPLOYING: { tone: 'warn', label: 'Deploying' },
  FAILED: { tone: 'danger', label: 'Failed' },
}

const DEPLOYMENT_STATUS: Record<DeploymentStatus, StatusDisplay> = {
  PENDING: { tone: 'warn', label: 'Pending' },
  BUILDING: { tone: 'warn', label: 'Pulling image' },
  STARTING: { tone: 'warn', label: 'Starting' },
  HEALTH_CHECKING: { tone: 'warn', label: 'Health checking' },
  HEALTHY: { tone: 'warn', label: 'Going live' },
  ACTIVE: { tone: 'ok', label: 'Active' },
  SUPERSEDED: { tone: 'muted', label: 'Previous' },
  FAILED: { tone: 'danger', label: 'Failed' },
  ROLLBACK: { tone: 'warn', label: 'Rolling back' },
  RESTORING: { tone: 'warn', label: 'Restoring' },
  // A failure, but one that was handled: the previous version was restored and
  // serves again. Amber, never green; the row's error still says why it failed.
  ROLLED_BACK: { tone: 'warn', label: 'Rolled back' },
}

const REPLICA_HEALTH: Record<ReplicaHealth, StatusDisplay> = {
  '': { tone: 'muted', label: 'No check' },
  'unknown': { tone: 'muted', label: 'Unknown' },
  'starting': { tone: 'warn', label: 'Starting' },
  'healthy': { tone: 'ok', label: 'Healthy' },
  'unhealthy': { tone: 'danger', label: 'Unhealthy' },
}

const UNKNOWN: StatusDisplay = { tone: 'muted', label: 'Unknown' }

export function applicationStatusDisplay(status: ApplicationStatus | string): StatusDisplay {
  return APPLICATION_STATUS[status as ApplicationStatus] ?? { tone: 'muted', label: titleCase(status) }
}

export function deploymentStatusDisplay(status: DeploymentStatus | string): StatusDisplay {
  return DEPLOYMENT_STATUS[status as DeploymentStatus] ?? { tone: 'muted', label: titleCase(status) }
}

export function replicaHealthDisplay(health: ReplicaHealth | string): StatusDisplay {
  return REPLICA_HEALTH[health as ReplicaHealth] ?? UNKNOWN
}

/** Docker container states: created, running, paused, restarting, removing, exited, dead. */
export function containerStateDisplay(container: Pick<Container, 'state' | 'exit_code' | 'oom_killed'>): StatusDisplay {
  switch (container.state) {
    case 'running':
      return { tone: 'ok', label: 'Running' }
    case 'exited':
    case 'dead':
      if (container.oom_killed) return { tone: 'danger', label: 'Out of memory' }
      return { tone: 'danger', label: `Exited (${container.exit_code})` }
    case 'created':
    case 'restarting':
    case 'paused':
    case 'removing':
      return { tone: 'warn', label: titleCase(container.state) }
    default:
      return { tone: 'muted', label: titleCase(container.state) || 'Unknown' }
  }
}

export function eventLevelTone(level: EventLevel | string): Tone {
  if (level === 'error') return 'danger'
  if (level === 'warn') return 'warn'
  return 'muted'
}

/** Everything that is neither fine nor intentionally off deserves a look. */
export function needsAttention(app: Pick<Application, 'status'>): boolean {
  return app.status !== 'HEALTHY' && app.status !== 'STOPPED'
}

const SEVERITY: Record<ApplicationStatus, number> = {
  CRASH_LOOP: 0,
  DOWN: 1,
  FAILED: 2,
  DEGRADED: 3,
  DEPLOYING: 4,
  HEALTHY: 5,
  STOPPED: 6,
}

/** Worst first, then by name. Does not mutate its input. */
export function sortBySeverity<T extends Pick<Application, 'status' | 'name'>>(apps: readonly T[]): T[] {
  return [...apps].sort((a, b) => {
    const diff = (SEVERITY[a.status] ?? 99) - (SEVERITY[b.status] ?? 99)
    return diff !== 0 ? diff : a.name.localeCompare(b.name)
  })
}

/** "2/2 healthy", the CLI's phrasing. */
export function replicaSummary(replicas: { desired: number, healthy: number }): string {
  return `${replicas.healthy}/${replicas.desired} healthy`
}

function titleCase(value: string): string {
  if (!value) return ''
  const lower = value.replace(/_/g, ' ').toLowerCase()
  return lower.charAt(0).toUpperCase() + lower.slice(1)
}
