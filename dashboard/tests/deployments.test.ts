import { describe, expect, it } from 'vitest'
import type { Deployment, DeploymentStatus } from '../app/types/api'
import { deploymentOrigin, formatOrigin, rollbackCandidates } from '../app/utils/deployments'

function dep(id: number, status: DeploymentStatus, patch: Partial<Deployment> = {}): Deployment {
  return {
    id,
    application: 'my-api',
    sequence: id,
    version: `1.0.${id}`,
    image: `ghcr.io/acme/my-api:1.0.${id}`,
    status,
    error: '',
    started_at: '2026-03-01T10:00:00Z',
    completed_at: '2026-03-01T10:00:06Z',
    kind: 'deploy',
    source_deployment_id: null,
    ...patch,
  }
}

describe('rollbackCandidates', () => {
  const history = [
    dep(9, 'HEALTH_CHECKING', { completed_at: null }),
    dep(8, 'ACTIVE'),
    dep(7, 'ROLLED_BACK', { error: 'replica 2 exited' }),
    dep(6, 'FAILED', { error: 'replica 1 exited' }),
    dep(5, 'SUPERSEDED'),
    dep(4, 'SUPERSEDED', { application: 'web' }),
    dep(3, 'SUPERSEDED'),
  ]

  it('lists exactly the application\'s SUPERSEDED deployments', () => {
    expect(rollbackCandidates(history, 'my-api').map(d => d.id)).toEqual([5, 3])
  })

  it('never offers the active deployment, a failed or rolled-back attempt, or one in flight', () => {
    const statuses = rollbackCandidates(history, 'my-api').map(d => d.status)
    expect(new Set(statuses)).toEqual(new Set(['SUPERSEDED']))
  })

  it('never offers another application\'s deployment', () => {
    expect(rollbackCandidates(history, 'my-api').some(d => d.application !== 'my-api')).toBe(false)
    expect(rollbackCandidates(history, 'web').map(d => d.id)).toEqual([4])
  })

  it('puts the most recent first (the agent\'s default target), whatever order it was given', () => {
    const shuffled = [dep(3, 'SUPERSEDED'), dep(8, 'ACTIVE'), dep(5, 'SUPERSEDED')]
    expect(rollbackCandidates(shuffled, 'my-api').map(d => d.id)).toEqual([5, 3])
    // and does not reorder its input
    expect(shuffled.map(d => d.id)).toEqual([3, 8, 5])
  })

  it('is empty after a first deployment', () => {
    expect(rollbackCandidates([dep(1, 'ACTIVE')], 'my-api')).toEqual([])
    expect(rollbackCandidates([], 'my-api')).toEqual([])
  })
})

describe('deploymentOrigin', () => {
  const known = [dep(3, 'SUPERSEDED', { version: '1.4.1' }), dep(6, 'SUPERSEDED', { version: '1.4.2' })]

  it('is null for a plain deploy', () => {
    expect(deploymentOrigin(dep(7, 'ACTIVE'), known)).toBeNull()
  })

  it('resolves a rollback to the deployment it re-used', () => {
    const origin = deploymentOrigin(dep(7, 'ACTIVE', { kind: 'rollback', source_deployment_id: 3 }), known)
    expect(origin).toMatchObject({ kind: 'rollback', phrase: 'rollback to', sourceId: 3 })
    expect(origin?.source?.version).toBe('1.4.1')
    expect(origin && formatOrigin(origin)).toBe('rollback to #3 (1.4.1)')
  })

  it('resolves a redeploy', () => {
    const origin = deploymentOrigin(dep(7, 'ACTIVE', { kind: 'redeploy', source_deployment_id: 6 }), known)
    expect(origin && formatOrigin(origin)).toBe('redeploy of #6 (1.4.2)')
  })

  it('keeps the id when the source is not among the known deployments', () => {
    const origin = deploymentOrigin(dep(7, 'ACTIVE', { kind: 'rollback', source_deployment_id: 99 }), known)
    expect(origin).toMatchObject({ sourceId: 99, source: null })
    expect(origin && formatOrigin(origin)).toBe('rollback')
  })

  it('tolerates a rollback without a source', () => {
    const origin = deploymentOrigin(dep(7, 'ACTIVE', { kind: 'rollback', source_deployment_id: null }), known)
    expect(origin).toMatchObject({ kind: 'rollback', sourceId: null, source: null })
  })
})
