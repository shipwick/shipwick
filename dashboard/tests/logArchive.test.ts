import { describe, expect, it } from 'vitest'
import type { LogArchiveEntry, LogMatch } from '../app/types/api'
import {
  archiveSupported,
  chooseLogView,
  describeLogArchive,
  endedBadly,
  endedBecause,
  entryHeadline,
  entrySubject,
  groupMatches,
  groupSource,
  hasDied,
  lastOutputEntry,
  linesKept,
  logViewOf,
  logsPath,
  outputOf,
  positiveInt,
  searchBounds,
  searchSummary,
  searchTextProblem,
} from '../app/utils/logArchive'

const now = Date.parse('2026-10-04T09:00:00Z')
const MINUTE = 60_000

function entry(over: Partial<LogArchiveEntry> = {}): LogArchiveEntry {
  return {
    id: 364,
    application: 'web',
    kind: 'replica',
    deployment_id: 7,
    deployment: 3,
    version: '1.4.2',
    replica: 2,
    job: '',
    run_id: null,
    container: 'shipwick_web_3_2',
    reason: 'crashed',
    exit_code: 1,
    oom_killed: false,
    ended_at: new Date(now - 5 * MINUTE).toISOString(),
    first_line_at: new Date(now - 9 * MINUTE).toISOString(),
    last_line_at: new Date(now - 5 * MINUTE).toISOString(),
    lines: 153,
    bytes: 15300,
    stored_bytes: 3300,
    truncated: false,
    ...over,
  }
}

const run = (over: Partial<LogArchiveEntry> = {}) => entry({ kind: 'run', replica: 0, job: 'nightly-report', run_id: 12, container: 'shipwick_web_job_nightly-report_12', reason: 'succeeded', exit_code: 0, ...over })

describe('how a container ended', () => {
  it('says each way a replica ends as the rest of a sentence about it', () => {
    expect(endedBecause(entry())).toBe('crashed (exit 1)')
    expect(endedBecause(entry({ reason: 'exited', exit_code: 0 }))).toBe('exited by itself (exit 0)')
    expect(endedBecause(entry({ reason: 'oom_killed', exit_code: 137, oom_killed: true }))).toBe('ran out of memory (exit 137)')
    expect(endedBecause(entry({ reason: 'unhealthy', exit_code: null }))).toBe('was restarted for failing its health check')
    expect(endedBecause(entry({ reason: 'stopped', exit_code: 0 }))).toBe('was stopped with the application')
    expect(endedBecause(entry({ reason: 'replaced', exit_code: 0 }))).toBe('was replaced by a newer deployment')
    expect(endedBecause(entry({ reason: 'removed', exit_code: null }))).toBe('was removed as a leftover')
    expect(endedBecause(entry({ reason: 'restarted', exit_code: null }))).toBe('was restarted; how it ended was not seen')
  })

  it('says whether the replica of a failed deployment died by itself', () => {
    expect(endedBecause(entry({ reason: 'deployment_failed', exit_code: 127 }))).toBe('exited (exit 127) and failed its deployment')
    expect(endedBecause(entry({ reason: 'deployment_failed', exit_code: 137, oom_killed: true }))).toBe('ran out of memory (exit 137) and failed its deployment')
    expect(endedBecause(entry({ reason: 'deployment_failed', exit_code: null }))).toBe('was removed when its deployment failed')
  })

  it('calls a crash that was a death by memory by its cause', () => {
    expect(endedBecause(entry({ reason: 'crashed', exit_code: 137, oom_killed: true }))).toBe('ran out of memory (exit 137)')
  })

  it('says a run by its status', () => {
    expect(endedBecause(run())).toBe('succeeded')
    expect(endedBecause(run({ reason: 'failed', exit_code: 3 }))).toBe('failed (exit 3)')
    expect(endedBecause(run({ reason: 'timed_out', exit_code: null }))).toBe('timed out and was stopped')
    expect(endedBecause(run({ reason: 'interrupted', exit_code: null }))).toBe('was interrupted by a restart of the agent')
  })

  it('passes a reason it does not know through', () => {
    expect(endedBecause(entry({ reason: 'evicted' }))).toBe('evicted')
  })

  it('counts as a death what somebody looking for a crash is looking for', () => {
    for (const reason of ['crashed', 'oom_killed', 'unhealthy']) expect(endedBadly(entry({ reason })), reason).toBe(true)
    for (const reason of ['exited', 'stopped', 'replaced', 'removed', 'restarted']) expect(endedBadly(entry({ reason, exit_code: 0 })), reason).toBe(false)
    expect(endedBadly(entry({ reason: 'deployment_failed', exit_code: 1 }))).toBe(true)
    // Still running when its deployment failed: it did nothing wrong itself.
    expect(endedBadly(entry({ reason: 'deployment_failed', exit_code: null }))).toBe(false)
    expect(endedBadly(run({ reason: 'failed', exit_code: 1 }))).toBe(true)
    expect(endedBadly(run({ reason: 'timed_out' }))).toBe(true)
    expect(endedBadly(run())).toBe(false)
    expect(endedBadly(run({ reason: 'interrupted' }))).toBe(false)
  })
})

describe('whose output an entry is', () => {
  it('names a replica by its deployment and number, and a run by its number and job', () => {
    expect(outputOf(entry())).toBe('#3 replica 2')
    expect(outputOf(entry({ deployment: 0, deployment_id: null }))).toBe('replica 2')
    expect(outputOf(run())).toBe('run 12 of nightly-report')
    expect(outputOf(run({ job: 'run' }))).toBe('run 12, a command')
    expect(outputOf(run({ job: 'pre-deploy' }))).toBe('run 12, the pre-deploy command')
  })

  it('starts a sentence with it', () => {
    expect(entrySubject(entry())).toBe('Replica 2 of #3 (1.4.2)')
    expect(entrySubject(entry({ deployment: 0, version: '' }))).toBe('Replica 2')
    expect(entrySubject(run())).toBe('Run 12 of nightly-report')
  })

  it('says how much is kept', () => {
    expect(linesKept(entry())).toBe('153 lines')
    expect(linesKept(entry({ lines: 1 }))).toBe('1 line')
    expect(linesKept(entry({ lines: 2000, truncated: true }))).toBe('last 2,000 lines')
    expect(linesKept(entry({ lines: 0 }))).toBe('no output')
  })

  it('puts an entry in one line', () => {
    expect(entryHeadline(entry(), now)).toBe('Replica 2 of #3 (1.4.2) · crashed (exit 1) · 5m ago · 153 lines')
  })
})

describe('chooseLogView', () => {
  const web = { name: 'web', containers: 3 }

  it('opens on the live output when nothing is kept', () => {
    expect(chooseLogView({ ...web, status: 'HEALTHY' }, null, now)).toEqual({ view: 'live', note: '' })
    expect(chooseLogView({ ...web, status: 'CRASH_LOOP' }, null, now)).toEqual({ view: 'live', note: '' })
  })

  it('opens on the crash when the application is not healthy and its last container to end died', () => {
    for (const status of ['DEGRADED', 'DOWN', 'CRASH_LOOP', 'FAILED']) {
      const choice = chooseLogView({ ...web, status }, entry({ reason: 'oom_killed', exit_code: 137, oom_killed: true }), now)
      expect(choice.view, status).toBe('previous')
      expect(choice.note).toBe('web is not healthy, and replica 2 of #3 (1.4.2) ran out of memory (exit 137) 5m ago. This is what it wrote last.')
    }
  })

  it('stays on the live output of an application that is not healthy when the last container to end was only replaced', () => {
    expect(chooseLogView({ ...web, status: 'DEGRADED' }, entry({ reason: 'replaced', exit_code: 0 }), now)).toEqual({ view: 'live', note: '' })
  })

  it('opens on what the last container wrote when none runs', () => {
    const choice = chooseLogView({ name: 'docs', status: 'STOPPED', containers: 0 }, entry({ reason: 'stopped', exit_code: 0 }), now)
    expect(choice).toEqual({ view: 'previous', note: 'docs has no containers now. This is what the last one wrote before it ended.' })
  })

  it('mentions a crash of the last day above the live output of an application that is healthy again', () => {
    expect(chooseLogView({ ...web, status: 'HEALTHY' }, entry(), now)).toEqual({ view: 'live', note: 'Replica 2 of #3 (1.4.2) crashed (exit 1) 5m ago.' })
  })

  it('says nothing of a crash that is older than a day', () => {
    const old = entry({ ended_at: new Date(now - 25 * 60 * MINUTE).toISOString() })
    expect(chooseLogView({ ...web, status: 'HEALTHY' }, old, now)).toEqual({ view: 'live', note: '' })
  })

  it('opens the page of a deploying application on its live output', () => {
    expect(chooseLogView({ ...web, status: 'DEPLOYING' }, entry(), now).view).toBe('live')
  })
})

describe('the last output of a replica', () => {
  const died = { restarts: 0, crash_loop: false, oom_killed: false, exit_code: 0, state: 'running' }

  it('is offered for a replica that restarted, keeps crashing or lies dead', () => {
    expect(hasDied(died)).toBe(false)
    expect(hasDied({ ...died, restarts: 2 })).toBe(true)
    expect(hasDied({ ...died, crash_loop: true })).toBe(true)
    expect(hasDied({ ...died, oom_killed: true, state: 'exited', exit_code: 137 })).toBe(true)
    expect(hasDied({ ...died, state: 'exited', exit_code: 1 })).toBe(true)
    // Stopped on request: exit 0.
    expect(hasDied({ ...died, state: 'exited' })).toBe(false)
    // On its way out after a rollout: not a replica any more.
    expect(hasDied({ ...died, restarts: 3, stopping: true })).toBe(false)
  })

  it('is the newest entry of that container that is a death of its own', () => {
    const entries = [
      entry({ id: 48, reason: 'oom_killed' }),
      entry({ id: 46, reason: 'oom_killed' }),
      entry({ id: 50, reason: 'replaced' }),
      entry({ id: 49, container: 'shipwick_web_3_1' }),
    ]
    expect(lastOutputEntry(entries, 'shipwick_web_3_2')?.id).toBe(48)
    expect(lastOutputEntry(entries, 'shipwick_web_3_1')?.id).toBe(49)
    expect(lastOutputEntry(entries, 'shipwick_web_3_3')).toBeNull()
    expect(lastOutputEntry([entry({ reason: 'stopped' })], 'shipwick_web_3_2')).toBeNull()
  })
})

describe('groupMatches', () => {
  const line = (container: string, archive: number | null, second: number, over: Partial<LogMatch> = {}): LogMatch => ({
    replica: 2, container, stream: 'stdout', time: `2026-10-04T08:00:${String(second).padStart(2, '0')}Z`, message: `line ${second}`, archive_id: archive, deployment_id: 7, deployment: 3, job: '', run_id: null, ...over,
  })

  it('groups by source in the order sources arrive, each group in reading order', () => {
    const groups = groupMatches([
      line('shipwick_web_3_2', null, 30),
      line('shipwick_web_3_2', null, 20),
      line('shipwick_web_3_2', 364, 10),
      line('shipwick_web_3_2', 364, 5),
    ])
    expect(groups.map(g => g.key)).toEqual(['c:shipwick_web_3_2', 'a:364'])
    expect(groups[0]!.lines.map(l => l.message)).toEqual(['line 20', 'line 30'])
    expect(groups[1]!.lines.map(l => l.message)).toEqual(['line 5', 'line 10'])
    expect(groups[1]!.archiveId).toBe(364)
  })

  it('carries a source on when the next page continues it', () => {
    const first = [line('c', 363, 40), line('c', 363, 30)]
    const second = [line('c', 363, 20), line('d', 362, 10)]
    const groups = groupMatches([...first, ...second])
    expect(groups).toHaveLength(2)
    expect(groups[0]!.lines.map(l => l.message)).toEqual(['line 20', 'line 30', 'line 40'])
  })

  it('names what a group is the output of', () => {
    expect(groupSource({ deployment: 3, replica: 2, job: '', runId: null })).toBe('replica 2 of #3')
    expect(groupSource({ deployment: 0, replica: 1, job: '', runId: null })).toBe('replica 1')
    expect(groupSource({ deployment: 3, replica: 0, job: 'nightly-report', runId: 12 })).toBe('run 12 of nightly-report')
  })

  it('is nothing for nothing', () => {
    expect(groupMatches([])).toEqual([])
  })
})

describe('a search', () => {
  it('turns a window into a since, and a custom one into what was typed', () => {
    expect(searchBounds('', '', '', now)).toEqual({ problem: '' })
    expect(searchBounds('1h', '', '', now)).toEqual({ since: '2026-10-04T08:00:00.000Z', problem: '' })
    expect(searchBounds('7d', '', '', now)).toEqual({ since: '2026-09-27T09:00:00.000Z', problem: '' })
    const custom = searchBounds('custom', '2026-10-03T10:00', '2026-10-03T12:00', now)
    expect(custom.problem).toBe('')
    expect(Date.parse(custom.until!) - Date.parse(custom.since!)).toBe(2 * 60 * MINUTE)
    expect(searchBounds('custom', '2026-10-03T10:00', '', now).until).toBeUndefined()
  })

  it('refuses a custom window that is empty or ends before it starts', () => {
    expect(searchBounds('custom', '', '', now).problem).toBe('Choose where the search starts, where it ends, or both.')
    expect(searchBounds('custom', '2026-10-03T12:00', '2026-10-03T10:00', now).problem).toBe('The end is before the start.')
  })

  it('holds the text to what the agent takes', () => {
    expect(searchTextProblem('connection refused')).toBe('')
    expect(searchTextProblem('')).toBe('')
    expect(searchTextProblem('two\nlines')).toBe('One line only: a search looks inside lines.')
    expect(searchTextProblem('x'.repeat(257))).toBe('At most 256 bytes.')
    // Bytes, not characters.
    expect(searchTextProblem('ü'.repeat(129))).toBe('At most 256 bytes.')
  })

  it('says what it found once', () => {
    expect(searchSummary(42, 3, false)).toBe('42 lines in 3 containers')
    expect(searchSummary(1, 1, true)).toBe('1 line in 1 container, and there is more to search')
    expect(searchSummary(0, 0, false)).toBe('No line matches')
    expect(searchSummary(0, 0, true)).toBe('Nothing found yet')
  })
})

describe('the address of a view', () => {
  it('reads a view and a number out of the address, and nothing else', () => {
    expect(logViewOf('previous')).toBe('previous')
    expect(logViewOf(['search', 'live'])).toBe('search')
    expect(logViewOf('everything')).toBeNull()
    expect(logViewOf(undefined)).toBeNull()
    expect(positiveInt('364')).toBe(364)
    for (const bad of ['0', '-1', '1.5', 'abc', '', undefined, '12abc']) expect(positiveInt(bad), String(bad)).toBeNull()
  })

  it('leaves out what is not set', () => {
    expect(logsPath('my-api', { view: 'archive', deployment: 12, replica: null, kind: '' })).toEqual({ path: '/applications/my-api/logs', query: { view: 'archive', deployment: '12' } })
    expect(logsPath('my-api')).toEqual({ path: '/applications/my-api/logs', query: {} })
  })
})

describe('the archive on the server', () => {
  it('is there when the agent says so', () => {
    expect(archiveSupported({ log_archive: { enabled: true, entries: 0, bytes: 0, max_bytes: 1, retention_days: 14 } })).toBe(true)
    expect(archiveSupported({ log_archive: { enabled: false, entries: 0, bytes: 0, max_bytes: 0, retention_days: 14 } })).toBe(true)
    expect(archiveSupported({})).toBe(false)
    expect(archiveSupported(null)).toBe(false)
  })

  it('says what it holds, or that it is off', () => {
    expect(describeLogArchive({ enabled: true, entries: 278, bytes: 63974240, max_bytes: 1073741824, retention_days: 14 })).toBe('61 MB of 1 GB, 278 entries, kept 14 days')
    expect(describeLogArchive({ enabled: true, entries: 1, bytes: 123, max_bytes: 500 * 1024 ** 2, retention_days: 1 })).toBe('123 B of 500 MB, 1 entry, kept 1 day')
    expect(describeLogArchive({ enabled: false, entries: 0, bytes: 0, max_bytes: 0, retention_days: 14 })).toBe('Off: nothing is kept of a container that ended')
  })
})
