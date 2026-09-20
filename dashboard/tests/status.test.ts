import { describe, expect, it } from 'vitest'
import type { ApplicationStatus, DeploymentStatus } from '../app/types/api'
import {
  applicationStatusDisplay,
  containerStateDisplay,
  deploymentStatusDisplay,
  eventLevelTone,
  needsAttention,
  replicaHealthDisplay,
  replicaSummary,
  sortBySeverity,
} from '../app/utils/status'

describe('application status → tone', () => {
  const expected: Record<ApplicationStatus, string> = {
    HEALTHY: 'ok',
    DEGRADED: 'warn',
    DEPLOYING: 'warn',
    DOWN: 'danger',
    CRASH_LOOP: 'danger',
    FAILED: 'danger',
    STOPPED: 'muted',
  }
  for (const [status, tone] of Object.entries(expected)) {
    it(`${status} is ${tone}`, () => {
      expect(applicationStatusDisplay(status).tone).toBe(tone)
      expect(applicationStatusDisplay(status).label).not.toBe('')
    })
  }

  it('degrades gracefully for a status this build does not know', () => {
    expect(applicationStatusDisplay('HIBERNATING')).toEqual({ tone: 'muted', label: 'Hibernating' })
  })
})

describe('deployment status → tone', () => {
  const expected: Record<DeploymentStatus, string> = {
    PENDING: 'warn',
    BUILDING: 'warn',
    STARTING: 'warn',
    HEALTH_CHECKING: 'warn',
    HEALTHY: 'warn',
    ACTIVE: 'ok',
    SUPERSEDED: 'muted',
    FAILED: 'danger',
    ROLLBACK: 'warn',
    RESTORING: 'warn',
    // A failure that was handled: amber. Never green, and distinct from a plain FAILED.
    ROLLED_BACK: 'warn',
  }
  for (const [status, tone] of Object.entries(expected)) {
    it(`${status} is ${tone}`, () => expect(deploymentStatusDisplay(status).tone).toBe(tone))
  }

  it('ACTIVE is the only status that looks like success', () => {
    const green = Object.keys(expected).filter(status => deploymentStatusDisplay(status).tone === 'ok')
    expect(green).toEqual(['ACTIVE'])
  })

  it('ROLLED_BACK says what happened and does not read as FAILED', () => {
    expect(deploymentStatusDisplay('ROLLED_BACK')).toEqual({ tone: 'warn', label: 'Rolled back' })
    expect(deploymentStatusDisplay('ROLLED_BACK').tone).not.toBe(deploymentStatusDisplay('FAILED').tone)
  })

  it('labels the serving deployment "Active" and a replaced one "Previous"', () => {
    expect(deploymentStatusDisplay('ACTIVE').label).toBe('Active')
    expect(deploymentStatusDisplay('SUPERSEDED').label).toBe('Previous')
  })

  it('formats unknown statuses', () => {
    expect(deploymentStatusDisplay('SOME_NEW_STATE')).toEqual({ tone: 'muted', label: 'Some new state' })
  })
})

describe('replica health and container state', () => {
  it('maps every health value', () => {
    expect(replicaHealthDisplay('')).toEqual({ tone: 'muted', label: 'No check' })
    expect(replicaHealthDisplay('unknown').tone).toBe('muted')
    expect(replicaHealthDisplay('starting').tone).toBe('warn')
    expect(replicaHealthDisplay('healthy').tone).toBe('ok')
    expect(replicaHealthDisplay('unhealthy').tone).toBe('danger')
    expect(replicaHealthDisplay('???').tone).toBe('muted')
  })

  it('describes how a container ended', () => {
    expect(containerStateDisplay({ state: 'running', exit_code: 0, oom_killed: false })).toEqual({ tone: 'ok', label: 'Running' })
    expect(containerStateDisplay({ state: 'exited', exit_code: 1, oom_killed: false })).toEqual({ tone: 'danger', label: 'Exited (1)' })
    expect(containerStateDisplay({ state: 'exited', exit_code: 137, oom_killed: true })).toEqual({ tone: 'danger', label: 'Out of memory' })
    expect(containerStateDisplay({ state: 'created', exit_code: 0, oom_killed: false }).tone).toBe('warn')
    expect(containerStateDisplay({ state: '', exit_code: 0, oom_killed: false })).toEqual({ tone: 'muted', label: 'Unknown' })
  })

  it('maps event levels', () => {
    expect(eventLevelTone('info')).toBe('muted')
    expect(eventLevelTone('warn')).toBe('warn')
    expect(eventLevelTone('error')).toBe('danger')
  })
})

describe('attention and ordering', () => {
  it('everything except HEALTHY and STOPPED needs attention', () => {
    const all: ApplicationStatus[] = ['HEALTHY', 'DEGRADED', 'DOWN', 'CRASH_LOOP', 'STOPPED', 'DEPLOYING', 'FAILED']
    expect(all.filter(status => needsAttention({ status }))).toEqual(['DEGRADED', 'DOWN', 'CRASH_LOOP', 'DEPLOYING', 'FAILED'])
  })

  it('sorts worst first, then by name, without mutating', () => {
    const input = [
      { name: 'b', status: 'HEALTHY' as const },
      { name: 'z', status: 'CRASH_LOOP' as const },
      { name: 'a', status: 'HEALTHY' as const },
      { name: 'c', status: 'STOPPED' as const },
      { name: 'd', status: 'DEGRADED' as const },
      { name: 'e', status: 'DOWN' as const },
    ]
    const copy = [...input]
    expect(sortBySeverity(input).map(a => a.name)).toEqual(['z', 'e', 'd', 'a', 'b', 'c'])
    expect(input).toEqual(copy)
  })
})

describe('replica summary', () => {
  it('summarizes replicas like the CLI', () => {
    expect(replicaSummary({ desired: 2, healthy: 2 })).toBe('2/2 healthy')
  })
})
