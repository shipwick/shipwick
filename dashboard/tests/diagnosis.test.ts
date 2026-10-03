import { describe, expect, it } from 'vitest'
import type { ApplicationDetail, Container, Deployment } from '../app/types/api'
import { diagnose, headline } from '../app/utils/diagnosis'
import { statusCounts, verdict } from '../app/utils/overview'

function container(over: Partial<Container> = {}): Container {
  return { id: 'c1', name: 'shipwick_app_1_1', deployment_id: 1, replica: 1, image: 'app:1', state: 'running', exit_code: 0, oom_killed: false, ip: '', started_at: null, health: 'healthy', restarts: 0, crash_loop: false, ...over } as Container
}

function deployment(over: Partial<Deployment> = {}): Deployment {
  return { id: 1, application: 'app', sequence: 1, version: '1.0.0', image: 'app:1', status: 'ACTIVE', error: '', started_at: '2026-01-01T00:00:00Z', completed_at: '2026-01-01T00:00:05Z', kind: 'deploy', source_deployment_id: null, ...over } as Deployment
}

function app(over: Partial<ApplicationDetail> = {}): ApplicationDetail {
  return {
    name: 'app', status: 'HEALTHY', desired_state: 'running', image: 'app:1', version: '1.0.0', domain: '', replicas: { desired: 2, running: 2, healthy: 2 },
    deploying: false, in_flight_deployment_id: null, created_at: '', updated_at: '', static: false, spec: null, active_deployment: deployment(),
    containers: [container(), container({ replica: 2 })], ...over,
  } as ApplicationDetail
}

describe('diagnose', () => {
  it('finds nothing wrong with a healthy application', () => {
    expect(diagnose(app(), { deployments: [deployment()] })).toEqual([])
  })

  it('says why nothing runs when no deployment has succeeded, and links the attempt', () => {
    const failed = deployment({ id: 7, sequence: 2, status: 'FAILED', error: 'replica 1 exited with code 127 shortly after start' })
    const [finding] = diagnose(app({ status: 'FAILED', active_deployment: null, containers: [] }), { deployments: [failed] })
    expect(finding?.tone).toBe('danger')
    expect(finding?.detail).toContain('#2')
    expect(finding?.detail).toContain('exited with code 127')
    expect(finding?.action?.to).toBe('/deployments/7')
  })

  it('names the replica that keeps crashing and points at the logs', () => {
    const crashing = container({ replica: 2, crash_loop: true, restarts: 14, state: 'exited', exit_code: 1, health: 'unhealthy' })
    const findings = diagnose(app({ status: 'CRASH_LOOP', containers: [container(), crashing] }), { deployments: [deployment()] })
    expect(findings).toHaveLength(1)
    expect(findings[0]?.title).toBe('Replica 2 keeps crashing')
    expect(findings[0]?.detail).toContain('14 times')
    expect(findings[0]?.detail).toContain('exited with code 1')
    expect(findings[0]?.action?.to).toBe('/applications/app/logs')
  })

  it('does not repeat what an alert of the agent already says', () => {
    const crashing = container({ replica: 2, crash_loop: true, restarts: 14 })
    const findings = diagnose(app({ status: 'CRASH_LOOP', containers: [container(), crashing] }), { deployments: [deployment()], alerts: [{ kind: 'restarts', replica: 2 }] })
    expect(findings).toEqual([])
  })

  it('tells a memory kill from a crash', () => {
    const killed = container({ replica: 3, crash_loop: true, restarts: 4, state: 'exited', exit_code: 137, oom_killed: true })
    const [finding] = diagnose(app({ status: 'CRASH_LOOP', containers: [killed] }), { deployments: [deployment()] })
    expect(finding?.detail).toContain('ran out of memory')
    expect(finding?.detail).toContain('resources.memory')
  })

  it('says that visitors are affected when no replica is healthy', () => {
    const [finding] = diagnose(app({ status: 'DOWN', domain: 'example.com', replicas: { desired: 2, running: 1, healthy: 0 }, containers: [container({ health: 'unhealthy' })] }), { deployments: [deployment()] })
    expect(finding?.title).toBe('No replica is healthy')
    expect(finding?.detail).toContain('Visitors')
  })

  it('counts the healthy replicas of a degraded application', () => {
    const [finding] = diagnose(app({ status: 'DEGRADED', replicas: { desired: 3, running: 2, healthy: 2 }, containers: [container(), container({ replica: 2 }), container({ replica: 3, health: 'unhealthy' })] }), { deployments: [deployment()] })
    expect(finding?.tone).toBe('warn')
    expect(finding?.title).toBe('2 of 3 replicas healthy')
    expect(finding?.detail).toContain('Replica 3 fails its health check')
  })

  it('reports a failed attempt after the version that runs, and that the old version still serves', () => {
    const active = deployment({ id: 8, sequence: 8, version: '1.4.2' })
    const attempt = deployment({ id: 9, sequence: 9, version: '1.5.0', status: 'ROLLED_BACK', error: 'replica 2 exited with code 1 shortly after start' })
    const [finding] = diagnose(app({ version: '1.4.2', active_deployment: active }), { deployments: [attempt, active] })
    expect(finding?.title).toBe('The last deployment failed; 1.4.2 is still running')
    expect(finding?.detail).toContain('#9 (1.5.0) failed part-way and was rolled back')
    expect(finding?.action?.to).toBe('/deployments/9')
  })

  it('does not report an old failure once a later deployment succeeded', () => {
    const old = deployment({ id: 7, sequence: 7, status: 'FAILED', error: 'x' })
    const active = deployment({ id: 8, sequence: 8 })
    expect(diagnose(app({ active_deployment: active }), { deployments: [active, old] })).toEqual([])
  })

  it('does not call a deployment in flight a failure', () => {
    const active = deployment({ id: 8 })
    const running = deployment({ id: 9, status: 'FAILED', completed_at: null })
    expect(diagnose(app({ active_deployment: active }), { deployments: [running, active] })).toEqual([])
  })

  it('reports a certificate that is not in order, in the agent\'s words', () => {
    const findings = diagnose(app({ certificates: [
      { hostname: 'a.example.com', status: 'ok', message: '', issuer: '', not_after: null },
      { hostname: 'b.example.com', status: 'waiting_for_dns', message: 'b.example.com does not point at this server yet', issuer: '', not_after: null },
    ] } as Partial<ApplicationDetail>), { deployments: [deployment()] })
    expect(findings).toHaveLength(1)
    expect(findings[0]?.title).toBe('b.example.com: waiting for DNS')
    expect(findings[0]?.detail).toBe('B.example.com does not point at this server yet.')
  })

  it('says when the domain is not served because of the proxy', () => {
    const findings = diagnose(app({ domain: 'example.com' }), { deployments: [deployment()], proxyProblem: 'The reverse proxy is unreachable; routing may be stale' })
    expect(findings[0]?.title).toBe('example.com is not being served')
    expect(diagnose(app(), { deployments: [deployment()], proxyProblem: 'x' })).toEqual([])
  })

  it('offers no logs for a static application', () => {
    const [finding] = diagnose(app({ status: 'DOWN', static: true, containers: [] }), { deployments: [deployment()] })
    expect(finding?.action).toBeUndefined()
  })
})

describe('headline', () => {
  it('counts replicas for a running application', () => {
    expect(headline(app())).toBe('2 of 2 replicas healthy')
    expect(headline(app({ replicas: { desired: 1, running: 1, healthy: 1 } }))).toBe('1 of 1 replica healthy')
  })

  it('says what a stop means', () => {
    expect(headline(app({ status: 'STOPPED' }))).toContain('stays stopped')
  })
})

describe('verdict', () => {
  const of = (...statuses: string[]) => statuses.map(status => ({ status })) as { status: ApplicationDetail['status'] }[]

  it('is green only when everything that should run is healthy', () => {
    const v = verdict(of('HEALTHY', 'HEALTHY', 'STOPPED'))
    expect(v.tone).toBe('ok')
    expect(v.title).toBe('Everything that should be running is healthy')
    expect(v.detail).toBe('2 healthy, 1 stopped')
  })

  it('counts the applications that need attention, red when one is broken', () => {
    const v = verdict(of('HEALTHY', 'CRASH_LOOP', 'DEGRADED', 'DEPLOYING'))
    expect(v.tone).toBe('danger')
    expect(v.title).toBe('2 applications need attention')
  })

  it('is amber when the worst is degraded', () => {
    const v = verdict(of('HEALTHY', 'DEGRADED'))
    expect(v.tone).toBe('warn')
    expect(v.title).toBe('1 application needs attention')
  })

  it('reports an alert about the server when the applications are fine', () => {
    const v = verdict(of('HEALTHY'), [{ severity: 'critical', application: '' }])
    expect(v.tone).toBe('danger')
    expect(v.title).toBe('The server needs attention')
  })

  it('mentions warnings about healthy applications', () => {
    expect(verdict(of('HEALTHY'), [{ severity: 'warning', application: 'web' }]).title).toBe('Applications are healthy, with 1 warning')
  })

  it('does not call a first deployment a problem', () => {
    expect(verdict(of('HEALTHY', 'DEPLOYING')).title).toBe('1 first deployment in progress; everything else is healthy')
  })

  it('says so when nothing is deployed', () => {
    expect(verdict([]).tone).toBe('muted')
  })

  it('counts every status, in a fixed order', () => {
    expect(statusCounts(of('STOPPED', 'HEALTHY')).map(c => c.status)).toEqual(['HEALTHY', 'DEGRADED', 'DOWN', 'CRASH_LOOP', 'FAILED', 'DEPLOYING', 'STOPPED'])
  })
})
