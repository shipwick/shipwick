import { describe, expect, it } from 'vitest'
import type { Run, RunStatus } from '../app/types/api'
import { argvFromFields, describeRunOutcome, formatNextRun, runStatusDisplay, runTitle, tidyDuration } from '../app/utils/jobs'

describe('run status → tone', () => {
  const expected: Record<RunStatus, string> = {
    running: 'warn',
    succeeded: 'ok',
    failed: 'danger',
    timed_out: 'danger',
    interrupted: 'muted',
  }
  for (const [status, tone] of Object.entries(expected)) {
    it(`${status} is ${tone}`, () => {
      expect(runStatusDisplay(status).tone).toBe(tone)
      expect(runStatusDisplay(status).label).not.toBe('')
    })
  }

  it('succeeded is the only status that looks like success', () => {
    const green = Object.keys(expected).filter(s => runStatusDisplay(s).tone === 'ok')
    expect(green).toEqual(['succeeded'])
  })

  it('degrades gracefully for a status this build does not know', () => {
    expect(runStatusDisplay('paused_for_review')).toEqual({ tone: 'muted', label: 'paused for review' })
  })
})

describe('describeRunOutcome', () => {
  it('adds the exit code to a failure, and says when the container never started', () => {
    expect(describeRunOutcome({ status: 'failed', exit_code: 1 })).toBe('Failed (exit 1)')
    expect(describeRunOutcome({ status: 'failed', exit_code: 137 })).toBe('Failed (exit 137)')
    expect(describeRunOutcome({ status: 'failed', exit_code: null })).toBe('Failed (did not start)')
  })
  it('keeps the other statuses to their label', () => {
    expect(describeRunOutcome({ status: 'succeeded', exit_code: 0 })).toBe('Succeeded')
    expect(describeRunOutcome({ status: 'timed_out', exit_code: null })).toBe('Timed out')
    expect(describeRunOutcome({ status: 'running', exit_code: null })).toBe('Running')
  })
})

describe('runTitle', () => {
  const run = (patch: Partial<Run>): Pick<Run, 'job' | 'kind' | 'command'> => ({ job: 'nightly-report', kind: 'scheduled', command: ['node', 'report.js'], ...patch })
  it('names the job, the hook, or the one-off command', () => {
    expect(runTitle(run({}))).toBe('nightly-report')
    expect(runTitle(run({ job: 'pre-deploy', kind: 'hook' }))).toBe('pre-deploy hook')
    expect(runTitle(run({ job: 'run', kind: 'manual', command: ['rails', 'db:migrate', 'RAILS_ENV=production x'] }))).toBe('rails db:migrate "RAILS_ENV=production x"')
  })
})

describe('argvFromFields', () => {
  it('turns one field into one argument, as typed', () => {
    expect(argvFromFields(['rails', 'db:migrate'])).toEqual(['rails', 'db:migrate'])
    expect(argvFromFields(['sh', '-c', 'echo hi && exit 1'])).toEqual(['sh', '-c', 'echo hi && exit 1'])
  })
  it('drops only fields left empty; whitespace is an argument', () => {
    expect(argvFromFields(['node', '', 'x.js', ''])).toEqual(['node', 'x.js'])
    expect(argvFromFields([' '])).toEqual([' '])
    expect(argvFromFields([''])).toEqual([])
  })
})

describe('formatNextRun', () => {
  const now = Date.parse('2026-03-01T12:00:00Z')
  const at = (msAhead: number) => new Date(now + msAhead).toISOString()

  it('says how far away the next firing is', () => {
    expect(formatNextRun(at(30_000), now)).toBe('within a minute')
    expect(formatNextRun(at(15 * 60_000), now)).toBe('in 15m')
    expect(formatNextRun(at(5 * 3600_000), now)).toBe('in 5h')
    expect(formatNextRun(at(3 * 86_400_000), now)).toBe('in 3d')
  })
  it('reads "now" once the time has come, and explains null', () => {
    expect(formatNextRun(at(0), now)).toBe('now')
    expect(formatNextRun(at(-90_000), now)).toBe('now')
    expect(formatNextRun(null, now)).toBe('Not while stopped')
    expect(formatNextRun('nope', now)).toBe('—')
  })
})

describe('tidyDuration', () => {
  it('drops the zero components Go prints', () => {
    expect(tidyDuration('1h0m0s')).toBe('1h')
    expect(tidyDuration('10m0s')).toBe('10m')
    expect(tidyDuration('1m30s')).toBe('1m30s')
    expect(tidyDuration('2h30m0s')).toBe('2h30m')
  })
  it('keeps something for zero and passes unknown strings through', () => {
    expect(tidyDuration('0s')).toBe('0s')
    expect(tidyDuration('soon')).toBe('soon')
  })
})
