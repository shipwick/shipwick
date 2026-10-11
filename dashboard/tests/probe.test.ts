import { describe, expect, it } from 'vitest'
import type { AgentEvent } from '../app/types/api'
import { PROBE_RECHECKS_MS, PROBE_WINDOW_MS, probeExpected, probeFinding, probeOf } from '../app/utils/probe'

let nextId = 1
function event(type: AgentEvent['type'], message: string, level: AgentEvent['level'] = 'info'): AgentEvent {
  return { id: nextId++, deployment_id: 7, level, type, message, created_at: '2026-10-11T12:00:00Z' }
}

const LOOP = 'https://example.com/ sends its visitors to www.example.com, which `redirects` sends back: every request loops. Make www.example.com the `domain` and example.com a redirect, or stop the application redirecting'
const routed = [event('state', 'HEALTHY'), event('step', 'Routed https://example.com to 2 replicas'), event('state', 'ACTIVE'), event('step', 'Deployment successful')]
const completedAt = '2026-10-11T12:00:03Z'
const now = Date.parse(completedAt) + 4000

describe('probeOf', () => {
  it('finds the probe among the events, and nothing while there is none', () => {
    const probe = event('probe', LOOP, 'warn')
    expect(probeOf([...routed, probe])).toBe(probe)
    expect(probeOf(routed)).toBeNull()
    expect(probeOf(undefined)).toBeNull()
  })
})

describe('probeExpected', () => {
  it('expects one of a completed, routed deployment that has none yet', () => {
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, events: routed }, now)).toBe(true)
    expect(probeExpected({ status: 'SUPERSEDED', completed_at: completedAt, events: routed }, now)).toBe(true)
  })

  it('expects none while the deployment runs, of one that failed, of one without a routed hostname, or once the probe is there', () => {
    expect(probeExpected({ status: 'HEALTHY', completed_at: null, events: routed }, now)).toBe(false)
    expect(probeExpected({ status: 'FAILED', completed_at: completedAt, events: routed }, now)).toBe(false)
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, events: [event('step', 'Deployment successful')] }, now)).toBe(false)
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, static: { digest: 'sha256:ab', size_bytes: 1, files: 1 }, events: routed }, now)).toBe(false)
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, events: [...routed, event('probe', 'https://example.com/ answers HTTP 200 through the proxy')] }, now)).toBe(false)
  })

  it('stops expecting one after the window: an agent before 0.9 never writes one', () => {
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, events: routed }, Date.parse(completedAt) + PROBE_WINDOW_MS - 1)).toBe(true)
    expect(probeExpected({ status: 'ACTIVE', completed_at: completedAt, events: routed }, Date.parse(completedAt) + PROBE_WINDOW_MS)).toBe(false)
  })

  it('asks again a few seconds after completion, and once more later', () => {
    expect(PROBE_RECHECKS_MS).toEqual([6000, 30_000])
  })
})

describe('probeFinding', () => {
  it('turns a warn probe into a finding with the message verbatim and the way to the deployment', () => {
    const finding = probeFinding(event('probe', LOOP, 'warn'), { id: 7, sequence: 3 })
    expect(finding).toMatchObject({ key: 'probe-7', tone: 'warn', detail: LOOP, action: { label: 'Open deployment #3', to: '/deployments/7' } })
  })

  it('is nothing for an info probe or no probe', () => {
    expect(probeFinding(event('probe', 'https://example.com/ answers HTTP 200 through the proxy'), { id: 7, sequence: 3 })).toBeNull()
    expect(probeFinding(null, { id: 7, sequence: 3 })).toBeNull()
  })
})
