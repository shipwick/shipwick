import { describe, expect, it } from 'vitest'
import type { Application } from '../app/types/api'
import { describeBackupPlan, triggerLabel } from '../app/utils/backups'
import { EXAMPLE_DOCUMENT, cliOnlyReason, inspectDocument } from '../app/utils/deployDocument'
import { applicationMarks, attentionReason } from '../app/utils/marks'
import { describeNetwork } from '../app/utils/network'
import { verdict } from '../app/utils/overview'
import { isDraining, needsAttention } from '../app/utils/status'
import { pollRetryable, promotedDisplay, promotionOutcome, promotionProgress, promotionRunning } from '../app/utils/transfer'

function app(over: Partial<Application> = {}): Application {
  return {
    name: 'web', status: 'HEALTHY', desired_state: 'running', image: 'web:1', version: '1', domain: 'example.com',
    replicas: { desired: 2, running: 2, healthy: 2 }, deploying: false, in_flight_deployment_id: null, created_at: '', updated_at: '', static: false,
    certificate_problem: null, alert_count: 0, alert_severity: '', ...over,
  }
}

const waiting = { hostname: 'example.net', status: 'waiting_for_dns', message: 'does not resolve yet' }

describe('applicationMarks', () => {
  it('has nothing to say about an application in order, or one an older agent describes', () => {
    expect(applicationMarks(app())).toEqual([])
    expect(applicationMarks({})).toEqual([])
  })

  it('marks a certificate that is not in order with its state, the hostname and the agent\'s message', () => {
    expect(applicationMarks(app({ certificate_problem: waiting }))).toEqual([
      { key: 'certificate', tone: 'warn', label: 'Waiting for DNS', title: 'example.net: does not resolve yet' },
    ])
    expect(applicationMarks(app({ certificate_problem: { hostname: 'a.example', status: 'expiring', message: 'expired on 2026-09-30' } }))[0]?.tone).toBe('danger')
  })

  it('counts the alerts, in red when one is critical', () => {
    expect(applicationMarks(app({ alert_count: 1, alert_severity: 'warning' }))[0]).toMatchObject({ key: 'alerts', tone: 'warn', label: '1 alert' })
    expect(applicationMarks(app({ alert_count: 2, alert_severity: 'critical' }))[0]).toMatchObject({ tone: 'danger', label: '2 alerts' })
  })
})

describe('needsAttention and the verdict', () => {
  it('counts a healthy application with an alert or a certificate problem among those that need a look', () => {
    expect(needsAttention(app())).toBe(false)
    expect(needsAttention(app({ alert_count: 1, alert_severity: 'warning' }))).toBe(true)
    expect(needsAttention(app({ certificate_problem: waiting }))).toBe(true)
    expect(needsAttention({ status: 'DEGRADED' })).toBe(true)
  })

  it('says what is wrong in one line, the status first', () => {
    expect(attentionReason(app({ certificate_problem: waiting }))).toBe('example.net: waiting for DNS')
    expect(attentionReason(app({ alert_count: 2, alert_severity: 'critical' }))).toBe('2 alerts, critical')
    expect(attentionReason(app({ status: 'DEGRADED', replicas: { desired: 3, running: 2, healthy: 2 }, alert_count: 1, alert_severity: 'warning' }))).toBe('2/3 healthy; 1 alert')
    expect(attentionReason(app({ status: 'DEPLOYING' }))).toBe('First deployment in progress')
  })

  it('lets a critical alert on a healthy application turn the verdict red', () => {
    const v = verdict([app(), app({ name: 'worker', alert_count: 1, alert_severity: 'critical' })])
    expect(v.title).toBe('1 application needs attention')
    expect(v.tone).toBe('danger')
    expect(verdict([app({ certificate_problem: waiting })]).tone).toBe('warn')
  })
})

describe('isDraining', () => {
  const active = { deploying: false, active_deployment: { id: 7 } }

  it('believes the agent when it says a container is stopping, or is not', () => {
    expect(isDraining({ deployment_id: 7, stopping: true }, active)).toBe(true)
    // A leftover of the active deployment itself is one the old inference could not see.
    expect(isDraining({ deployment_id: 6, stopping: false }, active)).toBe(false)
    expect(isDraining({ deployment_id: 6, stopping: true }, { deploying: true, active_deployment: { id: 7 } })).toBe(true)
  })

  it('infers it for an agent that does not say: a container of another deployment, once the deployment is over', () => {
    expect(isDraining({ deployment_id: 6 }, active)).toBe(true)
    expect(isDraining({ deployment_id: 7 }, active)).toBe(false)
    expect(isDraining({ deployment_id: 6 }, { deploying: true, active_deployment: { id: 7 } })).toBe(false)
  })
})

describe('describeNetwork', () => {
  const none = { proxy: '', docker_proxy: false, ca_file: false, dns_resolvers: [], acme_directory: '' }

  it('says nothing on a server with nothing set, and for an agent that does not say', () => {
    expect(describeNetwork(none)).toBeNull()
    expect(describeNetwork(undefined)).toBeNull()
  })

  it('lists what was set, in the CLI\'s words', () => {
    expect(describeNetwork({ proxy: 'proxy.example.com:3128', docker_proxy: true, ca_file: true, dns_resolvers: ['system'], acme_directory: 'https://ca.example.internal/acme/acme/directory' })).toEqual({
      parts: ['proxy proxy.example.com:3128', 'certificate authorities of its own', 'DNS system', 'certificates from https://ca.example.internal/acme/acme/directory'],
      warning: '',
    })
    expect(describeNetwork({ ...none, dns_resolvers: ['10.0.0.2:53', '10.0.0.3:53'] })?.parts).toEqual(['DNS 10.0.0.2:53, 10.0.0.3:53'])
  })

  it('warns when the agent has a proxy and the Docker daemon has none', () => {
    expect(describeNetwork({ ...none, proxy: 'proxy.example.com:3128' })?.warning).toContain('/etc/docker/daemon.json')
    expect(describeNetwork({ ...none, proxy: 'proxy.example.com:3128', docker_proxy: true })?.warning).toBe('')
    expect(describeNetwork({ ...none, ca_file: true })?.warning).toBe('')
  })
})

describe('backups', () => {
  it('names an adopted backup as such', () => {
    expect(triggerLabel('adopted')).toBe('adopted')
    expect(triggerLabel('schedule')).toBe('schedule')
    expect(triggerLabel('schedule', 'daily schedule')).toBe('daily schedule')
    expect(triggerLabel('manual')).toBe('by hand')
  })

  it('says how long the command before a backup gets only when it is not the default hour', () => {
    const plan = { schedule: '0 3 * * *', keep: 7, before: ['pg_dump', '-f', '/data/dump.sql'] }
    expect(describeBackupPlan(plan)).toBe('daily at 03:00 UTC, 7 kept, after pg_dump -f /data/dump.sql')
    expect(describeBackupPlan({ ...plan, before_timeout: '1h0m0s' })).toBe('daily at 03:00 UTC, 7 kept, after pg_dump -f /data/dump.sql')
    expect(describeBackupPlan({ ...plan, before_timeout: '2h0m0s', stop: true })).toBe('daily at 03:00 UTC, 7 kept, after pg_dump -f /data/dump.sql (2h at most), with the application stopped')
    expect(describeBackupPlan({ ...plan, before_timeout: '1h30m0s' })).toContain('(1h30m at most)')
  })
})

describe('a promotion that is followed', () => {
  const record = (statuses: string[], over = {}) => ({
    applications: statuses.map((status, i) => ({ name: ['postgres', 'my-api', 'web'][i]!, status, message: '' })),
    records: [],
    id: 1,
    status: 'running',
    started_at: '2026-03-01T09:30:00Z',
    completed_at: null,
    ...over,
  })

  it('runs until the agent sets completed_at; the held answer of an older agent has none and is over', () => {
    expect(promotionRunning(record(['pending']))).toBe(true)
    expect(promotionRunning(record(['running'], { completed_at: '2026-03-01T09:31:00Z' }))).toBe(false)
    expect(promotionRunning({ })).toBe(false)
    expect(promotionRunning(null)).toBe(false)
  })

  it('says which application is being started', () => {
    expect(promotionProgress(record(['pending', 'pending', 'pending']))).toBe('Promoting: about to start the applications')
    expect(promotionProgress(record(['running', 'starting', 'pending']))).toBe('Promoting: starting my-api (2 of 3)')
    expect(promotionProgress(record(['running', 'running', 'pending']))).toBe('Promoting (2 of 3 started)')
    expect(promotionProgress(record(['running', 'running', 'started']))).toBe('Promoting: finishing')
  })

  it('sums up how it went, naming what is not ready and what failed', () => {
    expect(promotionOutcome(record(['running', 'running', 'running']))).toBe('3 running')
    expect(promotionOutcome(record(['running', 'started', 'failed']))).toBe('1 running, 1 not ready yet, 1 could not be started')
    expect(promotionOutcome(record([]))).toBe('nothing to start')
  })

  it('shows the two states an application has before it runs', () => {
    expect(promotedDisplay({ status: 'pending' })).toEqual({ tone: 'muted', label: 'Waiting' })
    expect(promotedDisplay({ status: 'starting' })).toEqual({ tone: 'warn', label: 'Starting' })
  })

  it('asks again when the agent was away, and reports everything else', () => {
    for (const status of [502, 503, 504]) expect(pollRetryable({ status, code: 'BAD_RESPONSE' })).toBe(true)
    expect(pollRetryable({ status: 502, code: 'AGENT_UNREACHABLE' })).toBe(true)
    expect(pollRetryable({ status: 0, code: 'NETWORK' })).toBe(true)
    expect(pollRetryable({ status: 403, code: 'FORBIDDEN' })).toBe(false)
    expect(pollRetryable({ status: 500, code: 'INTERNAL_ERROR' })).toBe(false)
  })
})

describe('inspectDocument', () => {
  it('reads the name of a deploy.yaml, quoted or not, with a comment after it', () => {
    expect(inspectDocument('name: my-api\nimage: ghcr.io/acme/my-api:1\n')).toEqual({ name: 'my-api', kind: 'image', problem: '' })
    expect(inspectDocument('# the API\nimage: x\nname: "my-api"  # production\n').name).toBe('my-api')
    expect(inspectDocument('name: \'web\'\r\nimage: x\r\n').name).toBe('web')
  })

  it('reads only the top level: a job\'s or a volume\'s name is not the application\'s', () => {
    const text = 'image: x\njobs:\n  - name: nightly\n    schedule: "0 3 * * *"\nvolumes:\n  - name: data\n    path: /data\n'
    expect(inspectDocument(text).name).toBe('')
    expect(inspectDocument(text).problem).toContain('has no name')
  })

  it('tells what only the CLI can deploy', () => {
    expect(inspectDocument('name: shop\nbuild: .\n').kind).toBe('build')
    expect(inspectDocument('name: shop\nbuild:\n  context: .\n').kind).toBe('build')
    expect(inspectDocument('name: landing\nstatic: dist\ndomain: example.com\n').kind).toBe('static')
    expect(cliOnlyReason('build')).toContain('built')
    expect(cliOnlyReason('static')).toContain('folder')
    expect(cliOnlyReason('image')).toBe('')
  })

  it('reads a document written as JSON too', () => {
    expect(inspectDocument('{"name": "my-api", "image": "x"}')).toEqual({ name: 'my-api', kind: 'image', problem: '' })
    expect(inspectDocument('{"name": "shop", "build": {"context": "."}}').kind).toBe('build')
    expect(inspectDocument('{"name": ').name).toBe('')
  })

  it('refuses a name that cannot be an application\'s, and says nothing about an empty document', () => {
    expect(inspectDocument('name: My_API\n').problem).toContain('cannot be an application\'s name')
    expect(inspectDocument('').problem).toBe('')
    expect(inspectDocument(`name: big\n# ${'x'.repeat(70_000)}\n`).problem).toContain('64 KB')
  })

  it('offers an example that is itself a document this page can deploy', () => {
    expect(inspectDocument(EXAMPLE_DOCUMENT)).toEqual({ name: 'my-api', kind: 'image', problem: '' })
  })
})

describe('the mock agent\'s YAML reader', async () => {
  const { parseYaml } = await import('../mock/yaml.mjs' as string) as { parseYaml: (text: string) => unknown }

  it('reads the example document as the agent would', () => {
    expect(parseYaml(EXAMPLE_DOCUMENT)).toEqual({
      name: 'my-api',
      image: 'ghcr.io/company/my-api:1.0.0',
      port: 8080,
      domain: 'api.example.com',
      replicas: 2,
      env: { LOG_LEVEL: 'info', DATABASE_URL: 'postgres://app:${DATABASE_PASSWORD}@postgres:5432/app' },
      health: { path: '/health' },
      resources: { cpu: 1, memory: '512mb' },
    })
  })

  it('reads sequences of scalars and of mappings, flow lists, quotes, booleans and comments', () => {
    const text = [
      'name: db # the database',
      'init: true',
      'aliases: [a.example.com, "b.example.com"]',
      'command:',
      '  - sh',
      '  - -c',
      '  - "echo hi # not a comment"',
      'volumes:',
      '- name: data',
      '  path: /var/lib/data',
      'jobs:',
      '  - name: nightly',
      '    schedule: \'0 3 * * *\'',
      '    command: [node, report.js]',
      'health: {tcp: 5432, retries: 3}',
      'user:',
    ].join('\n')
    expect(parseYaml(text)).toEqual({
      name: 'db',
      init: true,
      aliases: ['a.example.com', 'b.example.com'],
      command: ['sh', '-c', 'echo hi # not a comment'],
      volumes: [{ name: 'data', path: '/var/lib/data' }],
      jobs: [{ name: 'nightly', schedule: '0 3 * * *', command: ['node', 'report.js'] }],
      health: { tcp: 5432, retries: 3 },
      user: null,
    })
  })

  it('names the line of what it cannot read', () => {
    expect(() => parseYaml('name: x\n\timage: y\n')).toThrow('yaml: line 2')
    expect(() => parseYaml('name: x\nimage y\n')).toThrow('yaml: line 2')
    expect(() => parseYaml('name: x\nname: y\n')).toThrow('already defined')
  })
})
