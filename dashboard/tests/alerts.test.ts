import { describe, expect, it } from 'vitest'
import type { Alert } from '../app/types/api'
import { alertKindLabel, alertSubject, alertTone, alertsFor, diskDisplay, sortAlerts, summarizeAlerts, worstAlertTone } from '../app/utils/alerts'

const disk: Alert = { kind: 'disk', severity: 'critical', application: '', replica: 0, message: 'The server\'s disk is 97% full', since: '2026-03-01T09:41:30Z' }
const memory: Alert = { kind: 'memory', severity: 'warning', application: 'my-api', replica: 1, message: 'my-api replica 1 is at 93% of its memory limit', since: '2026-03-01T09:58:00Z' }
const unhealthy: Alert = { kind: 'unhealthy', severity: 'warning', application: 'web', replica: 0, message: 'web has had 2 of 3 replicas healthy for 5m', since: '2026-03-01T09:30:00Z' }

describe('alert tones', () => {
  it('is red for critical and amber for a warning', () => {
    expect(alertTone(disk)).toBe('danger')
    expect(alertTone(memory)).toBe('warn')
  })

  it('takes the worst of a list, and nothing from an empty one', () => {
    expect(worstAlertTone([memory, disk])).toBe('danger')
    expect(worstAlertTone([memory, unhealthy])).toBe('warn')
    expect(worstAlertTone([])).toBeNull()
    expect(worstAlertTone(undefined)).toBeNull()
  })
})

describe('alertsFor', () => {
  it('keeps the alerts about one application; the disk\'s are about none', () => {
    expect(alertsFor([disk, memory, unhealthy], 'my-api')).toEqual([memory])
    expect(alertsFor([disk, memory, unhealthy], 'postgres')).toEqual([])
    expect(alertsFor(undefined, 'my-api')).toEqual([])
  })
})

describe('wording', () => {
  it('names what an alert is about', () => {
    expect(alertSubject(memory)).toBe('my-api replica 1')
    expect(alertSubject(unhealthy)).toBe('web')
    expect(alertSubject(disk)).toBe('the server')
  })

  it('labels the kinds, and passes an unknown one through', () => {
    expect(alertKindLabel(memory)).toBe('Memory')
    expect(alertKindLabel(disk)).toBe('Disk')
    expect(alertKindLabel({ kind: 'restarts' })).toBe('Restarts')
    expect(alertKindLabel({ kind: 'network' })).toBe('network')
  })

  it('sums a list up for the mark that is on every page', () => {
    expect(summarizeAlerts([memory])).toBe('1 alert')
    expect(summarizeAlerts([memory, unhealthy])).toBe('2 alerts')
    expect(summarizeAlerts([memory, disk])).toBe('2 alerts, 1 critical')
    expect(summarizeAlerts([disk])).toBe('1 alert, critical')
  })
})

describe('sortAlerts', () => {
  it('puts critical first and keeps the oldest first within a severity', () => {
    expect(sortAlerts([memory, disk, unhealthy]).map(a => a.kind)).toEqual(['disk', 'unhealthy', 'memory'])
  })
})

describe('diskDisplay', () => {
  const GB = 1024 ** 3

  it('shows what df shows', () => {
    expect(diskDisplay({ total_bytes: 40 * GB, used_bytes: 35 * GB })).toEqual({ percent: 88, used: '35 GB of 40 GB used', free: '5 GB free', tone: 'ok' })
  })

  it('takes its color from the agent\'s disk alert, not from thresholds of its own', () => {
    const usage = { total_bytes: 40 * GB, used_bytes: 35 * GB }
    expect(diskDisplay(usage, [{ kind: 'disk', severity: 'warning' }])?.tone).toBe('warn')
    expect(diskDisplay(usage, [{ kind: 'disk', severity: 'critical' }])?.tone).toBe('danger')
    expect(diskDisplay(usage, [{ kind: 'memory', severity: 'critical' }])?.tone).toBe('ok')
    expect(diskDisplay({ total_bytes: 40 * GB, used_bytes: 39.9 * GB })?.tone).toBe('ok')
  })

  it('is null where the agent cannot measure the disk', () => {
    expect(diskDisplay(null)).toBeNull()
    expect(diskDisplay(undefined)).toBeNull()
    expect(diskDisplay({ total_bytes: 0, used_bytes: 0 })).toBeNull()
  })

  it('stays within 0 and 100', () => {
    expect(diskDisplay({ total_bytes: GB, used_bytes: 2 * GB })?.percent).toBe(100)
    expect(diskDisplay({ total_bytes: GB, used_bytes: 2 * GB })?.free).toBe('0 B free')
  })
})
