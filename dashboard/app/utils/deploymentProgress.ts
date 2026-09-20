import type { AgentEvent, Deployment, DeploymentDetail, DeploymentStatus } from '~/types/api'

/**
 * State of the "live deployment" panel, and the reducer that advances it from
 * successive polls of GET /deployments/:id.
 *
 * It renders like the CLI: `step` events become a checklist, `warn` steps are
 * warnings, and a failure shows the error plus the saved `log` events.
 *
 * The only signal that a deployment is over is `completed_at`. In particular
 * FAILED is not an end state by itself: a rolling deployment that fails after
 * some old replicas were already replaced goes on through
 * FAILED → ROLLBACK → RESTORING → ROLLED_BACK, with `error` keeping the
 * original cause throughout.
 */

export type ProgressPhase
  = | 'running'
    /** Completed as ACTIVE (or already SUPERSEDED by a later one): the only success. */
    | 'succeeded'
    /** Completed as FAILED: nothing of the previous version had been touched. */
    | 'failed'
    /** Completed as ROLLED_BACK: it failed part-way, and the previous version was restored. */
    | 'rolled_back'

export interface ProgressStep {
  id: number
  message: string
  kind: 'done' | 'warning' | 'error'
  at: string
}

export interface DeploymentProgress {
  deploymentId: number
  application: string
  sequence: number
  version: string
  image: string
  status: DeploymentStatus
  phase: ProgressPhase
  steps: ProgressStep[]
  /** What the engine is doing right now; null once the deployment is done. */
  activity: string | null
  /** Why it failed. Set as soon as the agent reports it, also while a rollback is still running. */
  error: string
  /** Output captured from a replica that crashed, one entry per `log` event. */
  logs: string[]
  startedAt: string
  completedAt: string | null
  /** Set while polling is failing; the last known state stays on screen. */
  pollError: string | null
}

export type ProgressAction
  = | { type: 'started', deployment: Deployment }
    | { type: 'polled', detail: DeploymentDetail }
    | { type: 'poll_failed', message: string }
    | { type: 'dismissed' }

const ACTIVITY: Record<DeploymentStatus, string> = {
  PENDING: 'Waiting to start',
  BUILDING: 'Pulling image',
  STARTING: 'Starting the next replica',
  HEALTH_CHECKING: 'Waiting for the replica to become healthy',
  HEALTHY: 'Going live',
  // Status settles before the engine is done: leftovers are still being swept.
  ACTIVE: 'Finishing',
  SUPERSEDED: 'Finishing',
  // Either cleanup of the new containers, or the moment before a rollback begins.
  FAILED: 'Cleaning up',
  ROLLBACK: 'Rolling back',
  RESTORING: 'Restoring the replicas of the previous version',
  ROLLED_BACK: 'Finishing the rollback',
}

const FAILURE_STATES: ReadonlySet<DeploymentStatus> = new Set(['FAILED', 'ROLLBACK', 'RESTORING', 'ROLLED_BACK'])

/** True for the outcomes that are not a success: a plain failure and a handled one. */
export function isFailurePhase(phase: ProgressPhase): boolean {
  return phase === 'failed' || phase === 'rolled_back'
}

export function progressReducer(state: DeploymentProgress | null, action: ProgressAction): DeploymentProgress | null {
  switch (action.type) {
    case 'started':
      return fromDeployment(action.deployment, [], null)

    case 'polled': {
      // A response for a deployment we stopped watching: ignore it.
      if (state && state.deploymentId !== action.detail.id) return state
      // Deployments never un-complete; a late response must not reopen a finished one.
      if (state && state.completedAt !== null && action.detail.completed_at === null) return state
      return fromDeployment(action.detail, action.detail.events ?? [], state)
    }

    case 'poll_failed':
      return state ? { ...state, pollError: action.message } : state

    case 'dismissed':
      return null
  }
}

/** One-shot derivation, for pages that already hold a full DeploymentDetail. */
export function deriveProgress(detail: DeploymentDetail): DeploymentProgress {
  return fromDeployment(detail, detail.events ?? [], null)
}

function phaseOf(d: Deployment): ProgressPhase {
  if (d.completed_at === null) return 'running'
  if (d.status === 'ACTIVE' || d.status === 'SUPERSEDED') return 'succeeded'
  if (d.status === 'ROLLED_BACK') return 'rolled_back'
  return 'failed'
}

function fromDeployment(d: Deployment, events: readonly AgentEvent[], previous: DeploymentProgress | null): DeploymentProgress {
  const ordered = [...events].sort((a, b) => a.id - b.id)
  const done = d.completed_at !== null

  return {
    deploymentId: d.id,
    application: d.application,
    sequence: d.sequence,
    version: d.version,
    image: d.image,
    status: d.status,
    phase: phaseOf(d),
    steps: mergeSteps(previous?.steps ?? [], ordered),
    activity: done ? null : ACTIVITY[d.status] ?? 'Working',
    error: failureReason(d, ordered),
    logs: ordered.filter(e => e.type === 'log').map(e => e.message),
    startedAt: d.started_at,
    completedAt: d.completed_at,
    pollError: null,
  }
}

/** Events are append-only, so steps only ever grow; never drop one already shown. */
function mergeSteps(existing: readonly ProgressStep[], events: readonly AgentEvent[]): ProgressStep[] {
  const byId = new Map<number, ProgressStep>()
  for (const step of existing) byId.set(step.id, step)
  for (const e of events) {
    if (e.type !== 'step') continue
    byId.set(e.id, {
      id: e.id,
      message: e.message,
      kind: e.level === 'warn' ? 'warning' : e.level === 'error' ? 'error' : 'done',
      at: e.created_at,
    })
  }
  return [...byId.values()].sort((a, b) => a.id - b.id)
}

function failureReason(d: Deployment, events: readonly AgentEvent[]): string {
  if (d.error) return d.error
  if (!FAILURE_STATES.has(d.status)) return ''
  // Fall back to the first state event "FAILED: <reason>": the original cause, not a later one.
  for (const e of events) {
    if (e.type === 'state' && e.message.startsWith('FAILED')) {
      return e.message.replace(/^FAILED:?\s*/, '') || 'Deployment failed'
    }
  }
  return 'Deployment failed'
}
