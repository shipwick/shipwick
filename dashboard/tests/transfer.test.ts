import { describe, expect, it } from 'vitest'
import type { Import, ImportedApplication, Standby } from '../app/types/api'
import { importLabel } from '../app/utils/deployments'
import { PROMOTE_WORD, describePull, importOutcome, importProgress, importStored, importedDisplay, isStandby, promotedDisplay, recordValue } from '../app/utils/transfer'

const now = Date.parse('2026-03-01T04:30:00Z')

function application(name: string, status: ImportedApplication['status'], message = ''): ImportedApplication {
  return { name, status, version: '1.0.0', deployment_id: status === 'imported' ? 1 : null, volumes: [], message }
}

function run(applications: ImportedApplication[], over: Partial<Import> = {}): Import {
  return {
    status: 'running',
    source: 'upload',
    stopped: false,
    overwrite: false,
    started_at: '2026-03-01T04:10:00Z',
    completed_at: null,
    exported_at: '2026-03-01T04:00:00Z',
    secrets: 0,
    registries: 0,
    certificates: 0,
    applications,
    warnings: [],
    error: '',
    ...over,
  }
}

describe('importProgress', () => {
  it('names the application in work and where it is in the order', () => {
    expect(importProgress(run([application('postgres', 'imported'), application('my-api', 'importing'), application('web', 'pending')])))
      .toBe('An import is running: my-api (2 of 3)')
  })

  it('says what it does before the export has been read and between applications', () => {
    expect(importProgress(run([]))).toBe('An import is running: reading the export')
    expect(importProgress(run([application('postgres', 'imported'), application('web', 'pending')]))).toBe('An import is running (1 of 2 done)')
    expect(importProgress(run([application('postgres', 'imported'), application('web', 'skipped')]))).toBe('An import is running: finishing')
  })
})

describe('an import that is over', () => {
  it('counts the outcomes, leaving out the ones that did not occur', () => {
    expect(importOutcome(run([application('a', 'imported'), application('b', 'imported'), application('c', 'skipped'), application('d', 'failed')])))
      .toBe('2 imported, 1 skipped, 1 failed')
    expect(importOutcome(run([application('a', 'skipped')]))).toBe('1 skipped')
    expect(importOutcome(run([]))).toBe('no applications')
  })

  it('says what was stored besides applications', () => {
    expect(importStored({ secrets: 2, registries: 1, certificates: 0 })).toBe('2 secrets, 1 registry credential')
    expect(importStored({ secrets: 1, registries: 0, certificates: 3 })).toBe('1 secret, 3 certificates')
    expect(importStored({ secrets: 0, registries: 0, certificates: 0 })).toBe('')
  })

  it('does not paint a skipped application as a failure', () => {
    expect(importedDisplay(application('a', 'skipped'))).toEqual({ tone: 'muted', label: 'Skipped' })
    expect(importedDisplay(application('a', 'failed'))).toEqual({ tone: 'danger', label: 'Failed' })
    expect(importedDisplay(application('a', 'imported'))).toEqual({ tone: 'ok', label: 'Imported' })
    expect(importedDisplay(application('a', 'importing')).tone).toBe('warn')
    expect(importedDisplay({ status: 'queued' })).toEqual({ tone: 'muted', label: 'queued' })
  })
})

describe('the standby', () => {
  const empty: Standby = { applications: [], records: [], pull: null }
  const pull = { schedule: '15 * * * *', last_at: '2026-03-01T04:15:11Z', last_export: 42, last_error: '' }

  it('is one when it holds stopped applications or fetches exports on a schedule', () => {
    expect(isStandby(empty)).toBe(false)
    expect(isStandby(null)).toBe(false)
    expect(isStandby({ ...empty, pull })).toBe(true)
    expect(isStandby({ ...empty, applications: [{ name: 'postgres', version: '17', hostnames: [], imported_at: '2026-03-01T04:15:02Z' }] })).toBe(true)
  })

  it('describes the scheduled fetch and what it imported last', () => {
    expect(describePull(pull, now)).toBe('hourly at :15, last 14m ago (export #42)')
    expect(describePull({ ...pull, last_at: null, last_export: 0 }, now)).toBe('hourly at :15, nothing fetched yet')
    expect(describePull({ ...pull, schedule: '0 5 * * *', last_export: 0 }, now)).toBe('daily at 05:00 UTC, last 14m ago')
  })

  it('tells started from ready after a promotion', () => {
    expect(promotedDisplay({ status: 'running' })).toEqual({ tone: 'ok', label: 'Running' })
    expect(promotedDisplay({ status: 'started' }).tone).toBe('warn')
    expect(promotedDisplay({ status: 'failed' }).tone).toBe('danger')
  })

  it('says so when the agent does not know its own address', () => {
    expect(recordValue({ value: '203.0.113.77' })).toBe('203.0.113.77')
    expect(recordValue({ value: '' })).toBe('this server\'s address')
  })

  it('is confirmed with the word the CLI asks for', () => {
    expect(PROMOTE_WORD).toBe('promote')
  })
})

describe('importLabel', () => {
  it('labels the deployment kinds an import adds, and no other', () => {
    expect(importLabel('import')).toBe('imported')
    expect(importLabel('standby')).toBe('imported, stopped')
    expect(importLabel('deploy')).toBeNull()
    expect(importLabel('rollback')).toBeNull()
  })
})
