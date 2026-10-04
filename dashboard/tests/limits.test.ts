import { describe, expect, it } from 'vitest'
import type { ApplicationDetail } from '../app/types/api'
import { diagnose } from '../app/utils/diagnosis'
import { describeDocker, isUnenforced, limitsNotice, unenforcedLimits, unenforcedOf, unenforcedSentence, usageIsTheDaemons, withoutUnenforced } from '../app/utils/limits'
import { historyLimit } from '../app/utils/metricsHistory'

const BOTH = ['memory', 'cpu']

describe('the Docker row of the server\'s page', () => {
  it('is the version alone for a daemon that enforces both limits', () => {
    expect(describeDocker({ docker_version: '29.8.2', docker: { rootless: false, unenforced_limits: [] } })).toEqual({ version: '29.8.2', warning: '', advice: '' })
  })

  it('says rootless next to the version', () => {
    expect(describeDocker({ docker_version: '29.8.2', docker: { rootless: true, unenforced_limits: [] } })).toEqual({ version: '29.8.2, rootless', warning: '', advice: '' })
  })

  it('says which limits are not enforced, in the CLI\'s words, and what to do on a rootless daemon', () => {
    const d = describeDocker({ docker_version: '29.8.2', docker: { rootless: true, unenforced_limits: ['cpu', 'memory'] } })
    expect(d.version).toBe('29.8.2, rootless')
    expect(d.warning).toBe('Memory and CPU limits are not enforced')
    expect(d.advice).toBe('No replica is held to resources.memory and resources.cpu of its deploy.yaml, and the usage shown for a replica is not its own. Delegate the cpu and memory cgroup controllers to the user who runs Docker, on a server with systemd (handbook: Rootless Docker).')
  })

  it('names the one limit of a daemon that lacks one controller', () => {
    const d = describeDocker({ docker_version: '27.4.1', docker: { rootless: false, unenforced_limits: ['memory'] } })
    expect(d.version).toBe('27.4.1')
    expect(d.warning).toBe('Memory limits are not enforced')
    expect(d.advice).toBe('No replica is held to resources.memory of its deploy.yaml. The server\'s kernel offers Docker no cgroup controller for it; docker info on the server says which.')
  })

  it('claims nothing for an agent before 0.8, which does not say', () => {
    expect(describeDocker({ docker_version: '27.4.1' })).toEqual({ version: '27.4.1', warning: '', advice: '' })
    expect(describeDocker({ docker_version: '' }).version).toBe('—')
  })
})

describe('the limits an application sets and Docker does not apply', () => {
  it('are those of the agent\'s list that the application asks for', () => {
    expect(unenforcedOf(BOTH, { memory: 512, cpu: 1 })).toEqual(['memory', 'cpu'])
    expect(unenforcedOf(BOTH, { memory: 512, cpu: 0 })).toEqual(['memory'])
    expect(unenforcedOf(['memory'], { memory: 0, cpu: 2 })).toEqual([])
    expect(unenforcedOf([], { memory: 512, cpu: 1 })).toEqual([])
  })

  it('are none when the agent did not say: absent is not "enforced", and not "unenforced" either', () => {
    expect(unenforcedOf(undefined, { memory: 512, cpu: 1 })).toEqual([])
    expect(isUnenforced(undefined, 'memory')).toBe(false)
    expect(limitsNotice(undefined, { memory: 512, cpu: 1 })).toBe('')
    expect(unenforcedSentence(undefined)).toBe('')
  })

  it('are said in one sentence next to the limits', () => {
    expect(limitsNotice(BOTH, { memory: 512, cpu: 1 })).toBe('Docker on this server does not enforce the memory and CPU limits: the replicas run without them.')
    expect(limitsNotice(BOTH, { memory: 0, cpu: 1 })).toBe('Docker on this server does not enforce the CPU limit: the replicas run without it.')
    expect(limitsNotice(BOTH, { memory: 0, cpu: 0 })).toBe('')
  })

  it('ignore a word the dashboard does not know', () => {
    expect(unenforcedOf(['pids', 'memory'], { memory: 1, cpu: 1 })).toEqual(['memory'])
  })
})

describe('usage that is not the replica\'s own', () => {
  it('is what a daemon without any cgroup reports: both limits listed', () => {
    expect(usageIsTheDaemons(BOTH)).toBe(true)
    expect(usageIsTheDaemons(['cpu', 'memory'])).toBe(true)
  })

  it('is not assumed of a daemon that lacks one controller, nor of one that did not say', () => {
    expect(usageIsTheDaemons(['memory'])).toBe(false)
    expect(usageIsTheDaemons([])).toBe(false)
    expect(usageIsTheDaemons(undefined)).toBe(false)
  })
})

describe('where the list comes from', () => {
  it('is the metrics sample when it carries one, and the server\'s own otherwise', () => {
    expect(unenforcedLimits({ unenforced_limits: ['memory'] }, { rootless: true, unenforced_limits: BOTH })).toEqual(['memory'])
    expect(unenforcedLimits({}, { rootless: true, unenforced_limits: BOTH })).toEqual(BOTH)
    expect(unenforcedLimits(null, { rootless: false, unenforced_limits: [] })).toEqual([])
  })

  it('is nowhere on an agent before 0.8', () => {
    expect(unenforcedLimits({}, undefined)).toBeUndefined()
    expect(unenforcedLimits(null, null)).toBeUndefined()
  })
})

describe('a history whose limit nothing is held to', () => {
  const history = { application: 'web', since: '2026-03-01T09:00:00Z', step: '30s', series: [], limits: { cpu: 1, memory_bytes: 512 } }

  it('draws no line for it', () => {
    const charted = withoutUnenforced(history, ['memory'])
    expect(historyLimit(charted, 'memory_bytes')).toBeNull()
    expect(historyLimit(charted, 'cpu_percent')).toBe(100)
    expect(withoutUnenforced(history, BOTH).limits).toEqual({ cpu: 0, memory_bytes: 0 })
  })

  it('is left as it is where Docker applies the limits, or the agent did not say', () => {
    expect(withoutUnenforced(history, [])).toBe(history)
    expect(withoutUnenforced(history, undefined)).toBe(history)
  })
})

describe('the finding on an application\'s page', () => {
  const app = {
    name: 'web', status: 'HEALTHY', domain: '', static: false, version: '1.0', containers: [], certificates: [],
    replicas: { desired: 1, running: 1, healthy: 1 }, active_deployment: null,
  } as unknown as ApplicationDetail

  it('names the limits and where to look', () => {
    expect(diagnose(app, { deployments: [], unenforcedLimits: ['memory', 'cpu'] })).toEqual([{
      key: 'limits',
      tone: 'warn',
      title: 'Docker on this server does not enforce the memory and CPU limits',
      detail: 'No replica is held to resources.memory and resources.cpu of deploy.yaml. The server\'s page says what Docker lacks.',
      action: { label: 'Server status', to: '/servers' },
    }])
  })

  it('says that the usage is not shown when it is the daemon\'s', () => {
    const [finding] = diagnose(app, { deployments: [], unenforcedLimits: ['memory'], usageIsTheDaemons: true })
    expect(finding!.title).toBe('Docker on this server does not enforce the memory limit')
    expect(finding!.detail).toBe('No replica is held to resources.memory of deploy.yaml, and the usage Docker reports for a replica is that of everything it runs, so it is not shown. The server\'s page says what Docker lacks.')
  })

  it('is not there for an application whose limits are applied, or on an agent that does not say', () => {
    expect(diagnose(app, { deployments: [], unenforcedLimits: [] })).toEqual([])
    expect(diagnose(app, { deployments: [] })).toEqual([])
  })
})
