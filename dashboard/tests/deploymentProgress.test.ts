import { describe, expect, it } from 'vitest'
import type { AgentEvent, Deployment, DeploymentDetail } from '../app/types/api'
import { deriveProgress, isFailurePhase, progressReducer } from '../app/utils/deploymentProgress'

const base: Deployment = {
  id: 7,
  application: 'my-api',
  sequence: 3,
  version: '1.4.2',
  image: 'ghcr.io/company/my-api:1.4.2',
  status: 'PENDING',
  error: '',
  started_at: '2026-03-01T10:00:00Z',
  completed_at: null,
  kind: 'deploy',
  source_deployment_id: null,
}

const spec = {
  name: 'my-api',
  image: base.image,
  replicas: 2,
  resources: {},
  restart: { policy: 'always' },
  deploy: { strategy: 'rolling' },
}

let nextId = 1
function event(type: AgentEvent['type'], message: string, level: AgentEvent['level'] = 'info'): AgentEvent {
  return { id: nextId++, deployment_id: 7, level, type, message, created_at: '2026-03-01T10:00:01Z' }
}

function detail(patch: Partial<Deployment>, events: AgentEvent[]): DeploymentDetail {
  return { ...base, ...patch, spec, events }
}

describe('progressReducer', () => {
  it('starts in the running phase with no steps', () => {
    const state = progressReducer(null, { type: 'started', deployment: base })
    expect(state).toMatchObject({ deploymentId: 7, phase: 'running', steps: [], activity: 'Waiting to start', error: '', logs: [] })
  })

  it('turns step events into a checklist and describes the current activity', () => {
    let state = progressReducer(null, { type: 'started', deployment: base })
    state = progressReducer(state, {
      type: 'polled',
      detail: detail({ status: 'STARTING' }, [
        event('state', 'BUILDING'),
        event('step', 'Pulled image ghcr.io/company/my-api:1.4.2'),
        event('state', 'STARTING'),
        event('step', 'Created 2 containers'),
      ]),
    })
    expect(state?.steps.map(s => s.message)).toEqual(['Pulled image ghcr.io/company/my-api:1.4.2', 'Created 2 containers'])
    expect(state?.steps.every(s => s.kind === 'done')).toBe(true)
    expect(state?.activity).toBe('Starting the next replica')
    expect(state?.phase).toBe('running')
  })

  it('renders warn steps as warnings', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'STARTING' }, [event('step', 'Could not pull; using the local copy', 'warn')]),
    })
    expect(state?.steps[0]?.kind).toBe('warning')
  })

  it('is NOT done at the first ACTIVE: only completed_at finishes it', () => {
    const events = [event('step', 'Pulled image x'), event('state', 'ACTIVE')]
    const settling = progressReducer(null, { type: 'polled', detail: detail({ status: 'ACTIVE' }, events) })
    expect(settling?.phase).toBe('running')
    expect(settling?.activity).toBe('Finishing')

    const done = progressReducer(settling, {
      type: 'polled',
      detail: detail({ status: 'ACTIVE', completed_at: '2026-03-01T10:00:06.100Z' }, [...events, event('step', 'Deployment successful')]),
    })
    expect(done?.phase).toBe('succeeded')
    expect(done?.activity).toBeNull()
    expect(done?.completedAt).toBe('2026-03-01T10:00:06.100Z')
  })

  it('reports failure with the error and the captured output', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'FAILED', error: 'replica 1 exited with code 1 shortly after start', completed_at: '2026-03-01T10:00:05Z' }, [
        event('step', 'Started 2 containers'),
        event('log', 'panic: DATABASE_URL is not set', 'error'),
        event('state', 'FAILED: replica 1 exited with code 1 shortly after start', 'error'),
      ]),
    })
    expect(state?.phase).toBe('failed')
    expect(state?.error).toBe('replica 1 exited with code 1 shortly after start')
    expect(state?.logs).toEqual(['panic: DATABASE_URL is not set'])
    // log and state events are not checklist items
    expect(state?.steps.map(s => s.message)).toEqual(['Started 2 containers'])
  })

  it('shows the failure as soon as the status says so, while cleanup is still running', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'FAILED', error: 'boom' }, []),
    })
    expect(state?.phase).toBe('running')
    expect(state?.activity).toBe('Cleaning up')
    expect(state?.error).toBe('boom')
  })

  it('falls back to the FAILED state event when error is empty', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'FAILED', completed_at: '2026-03-01T10:00:05Z' }, [event('state', 'FAILED: image not found', 'error')]),
    })
    expect(state?.error).toBe('image not found')
  })

  it('treats SUPERSEDED at completion as success (a newer deployment already replaced it)', () => {
    const state = progressReducer(null, { type: 'polled', detail: detail({ status: 'SUPERSEDED', completed_at: '2026-03-01T10:00:06Z' }, []) })
    expect(state?.phase).toBe('succeeded')
  })

  it('orders steps by id even if the agent returns them newest first', () => {
    const a = event('step', 'first')
    const b = event('step', 'second')
    const state = progressReducer(null, { type: 'polled', detail: detail({ status: 'STARTING' }, [b, a]) })
    expect(state?.steps.map(s => s.message)).toEqual(['first', 'second'])
  })

  it('never drops a step that was already shown', () => {
    const a = event('step', 'first')
    const b = event('step', 'second')
    let state = progressReducer(null, { type: 'polled', detail: detail({ status: 'STARTING' }, [a, b]) })
    state = progressReducer(state, { type: 'polled', detail: detail({ status: 'STARTING' }, [a]) })
    expect(state?.steps.map(s => s.message)).toEqual(['first', 'second'])
  })

  it('ignores responses for another deployment and late unfinished responses', () => {
    const finished = progressReducer(null, { type: 'polled', detail: detail({ status: 'ACTIVE', completed_at: '2026-03-01T10:00:06Z' }, []) })
    expect(progressReducer(finished, { type: 'polled', detail: { ...detail({ status: 'BUILDING' }, []), id: 99 } })).toBe(finished)
    expect(progressReducer(finished, { type: 'polled', detail: detail({ status: 'HEALTHY' }, []) })).toBe(finished)
  })

  it('keeps the last state when polling fails, and clears the notice on the next success', () => {
    let state = progressReducer(null, { type: 'started', deployment: base })
    state = progressReducer(state, { type: 'poll_failed', message: 'Cannot reach the Shipwick agent' })
    expect(state?.pollError).toBe('Cannot reach the Shipwick agent')
    expect(state?.phase).toBe('running')
    state = progressReducer(state, { type: 'polled', detail: detail({ status: 'BUILDING' }, []) })
    expect(state?.pollError).toBeNull()
  })

  it('poll_failed without a deployment is a no-op, dismissed clears', () => {
    expect(progressReducer(null, { type: 'poll_failed', message: 'x' })).toBeNull()
    const state = progressReducer(null, { type: 'started', deployment: base })
    expect(progressReducer(state, { type: 'dismissed' })).toBeNull()
  })
})

describe('progressReducer: rolling deployments and the rollback path', () => {
  const cause = 'replica 2 exited with code 1 shortly after start'
  const rolling = [
    event('state', 'BUILDING'),
    event('step', 'Pulled image ghcr.io/company/my-api:1.4.2'),
    event('state', 'STARTING'),
    event('step', 'Started 1 container'),
    event('state', 'HEALTH_CHECKING'),
    event('step', 'Replica 1 passed health checks'),
    event('step', 'Replica 1/2 is serving 1.4.2; its 1.4.1 predecessor is retired'),
    event('step', 'Started 1 container'),
  ]
  const failed = [...rolling, event('log', 'panic: boom', 'error'), event('state', `FAILED: ${cause}`, 'error')]
  const rollback = [...failed, event('state', 'ROLLBACK'), event('step', 'Rolling back: restoring 1 replica of 1.4.1')]
  const restoring = [...rollback, event('state', 'RESTORING')]
  const rolledBack = [
    ...restoring,
    event('step', 'Replica 1 passed health checks'),
    event('step', 'Rolled back: my-api is running 1.4.1 again'),
    event('state', 'ROLLED_BACK'),
  ]

  it('narrates per-replica steps, repeated messages included', () => {
    const state = progressReducer(null, { type: 'polled', detail: detail({ status: 'HEALTH_CHECKING' }, rolling) })
    expect(state?.steps.map(s => s.message)).toEqual([
      'Pulled image ghcr.io/company/my-api:1.4.2',
      'Started 1 container',
      'Replica 1 passed health checks',
      'Replica 1/2 is serving 1.4.2; its 1.4.1 predecessor is retired',
      'Started 1 container',
    ])
    expect(state?.activity).toBe('Waiting for the replica to become healthy')
  })

  it('does not stop at FAILED: ROLLBACK and RESTORING follow, and it is still running', () => {
    let state = progressReducer(null, { type: 'started', deployment: base })
    const phases: string[] = []
    const activities: (string | null)[] = []
    for (const [status, events] of [
      ['FAILED', failed],
      ['ROLLBACK', rollback],
      ['RESTORING', restoring],
      ['ROLLED_BACK', rolledBack], // status settled, completed_at not yet set
    ] as const) {
      state = progressReducer(state, { type: 'polled', detail: detail({ status, error: cause }, [...events]) })
      phases.push(state?.phase ?? '')
      activities.push(state?.activity ?? null)
      // The original cause stays on screen throughout.
      expect(state?.error).toBe(cause)
    }
    expect(phases).toEqual(['running', 'running', 'running', 'running'])
    expect(activities).toEqual(['Cleaning up', 'Rolling back', 'Restoring the replicas of the previous version', 'Finishing the rollback'])
  })

  it('ends as rolled_back, a failure outcome distinct from both success and plain failure', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'ROLLED_BACK', error: cause, completed_at: '2026-03-01T10:00:09Z' }, rolledBack),
    })
    expect(state?.phase).toBe('rolled_back')
    expect(state && isFailurePhase(state.phase)).toBe(true)
    expect(state?.activity).toBeNull()
    expect(state?.error).toBe(cause)
    expect(state?.logs).toEqual(['panic: boom'])
    expect(state?.steps.map(s => s.message).slice(-3)).toEqual([
      'Rolling back: restoring 1 replica of 1.4.1',
      'Replica 1 passed health checks',
      'Rolled back: my-api is running 1.4.1 again',
    ])
  })

  it('takes the cause from the first FAILED state event when error is empty, not from a later one', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'ROLLED_BACK', completed_at: '2026-03-01T10:00:09Z' }, [
        ...rolledBack,
        event('state', 'FAILED: something later', 'error'),
      ]),
    })
    expect(state?.error).toBe(cause)
  })

  it('a rollback that itself fails completes as failed', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'FAILED', error: `${cause}; rollback failed: replica 1 did not become healthy`, completed_at: '2026-03-01T10:00:40Z' }, restoring),
    })
    expect(state?.phase).toBe('failed')
  })

  it('isFailurePhase separates the two failure outcomes from the rest', () => {
    expect(isFailurePhase('failed')).toBe(true)
    expect(isFailurePhase('rolled_back')).toBe(true)
    expect(isFailurePhase('succeeded')).toBe(false)
    expect(isFailurePhase('running')).toBe(false)
  })

  it('renders the "no reverse proxy" warn step as a warning on an otherwise successful deployment', () => {
    const state = progressReducer(null, {
      type: 'polled',
      detail: detail({ status: 'ACTIVE', completed_at: '2026-03-01T10:00:06Z' }, [
        event('step', 'No reverse proxy is configured, so api.example.com is not being served. Set SHIPWICK_CADDY_ADMIN on the agent', 'warn'),
        event('step', 'Deployment successful'),
      ]),
    })
    expect(state?.phase).toBe('succeeded')
    expect(state?.steps.map(s => s.kind)).toEqual(['warning', 'done'])
  })
})

describe('deriveProgress', () => {
  it('derives the same view from a stored deployment', () => {
    const p = deriveProgress(detail({ status: 'ACTIVE', completed_at: '2026-03-01T10:00:06Z' }, [event('step', 'Deployment successful')]))
    expect(p.phase).toBe('succeeded')
    expect(p.steps).toHaveLength(1)
  })
})
