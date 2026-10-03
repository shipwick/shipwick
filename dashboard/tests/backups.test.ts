import { describe, expect, it } from 'vitest'
import type { BackupRun, BackupStatus } from '../app/types/api'
import {
  backupBusy,
  backupSize,
  backupStatusDisplay,
  backupUsable,
  describeBackupPlan,
  describeBackups,
  describeDestination,
  describeDestinations,
  describeSchedule,
  stateBackupDisplay,
  verificationDisplay,
} from '../app/utils/backups'

const HOUR = 60 * 60 * 1000
const now = Date.parse('2026-03-01T08:00:00Z')

function run(over: Partial<BackupRun> = {}): BackupRun {
  return {
    id: 12,
    trigger: 'schedule',
    status: 'succeeded',
    started_at: '2026-03-01T03:00:00Z',
    completed_at: '2026-03-01T03:00:41Z',
    volumes: [{ volume: 'data', size_bytes: 2 * 1024 ** 3 }, { volume: 'uploads', size_bytes: 512 * 1024 ** 2 }],
    destinations: ['local', 's3'],
    encrypted: true,
    error: '',
    activity: '',
    verified_at: null,
    verify_error: '',
    restored_at: null,
    restore_error: '',
    ...over,
  }
}

const failed = run({ id: 13, status: 'failed', started_at: '2026-03-01T07:00:00Z', completed_at: '2026-03-01T07:00:02Z', volumes: [], destinations: [], error: 'backups.before exited 2; nothing was archived' })

describe('describeSchedule', () => {
  it('says the common schedules in words, like the CLI', () => {
    expect(describeSchedule('0 3 * * *')).toBe('daily at 03:00 UTC')
    expect(describeSchedule('30 14 * * *')).toBe('daily at 14:30 UTC')
    expect(describeSchedule('15 * * * *')).toBe('hourly at :15')
  })

  it('repeats the expression for the rest', () => {
    expect(describeSchedule('0 3 * * 1')).toBe('on 0 3 * * 1 (UTC)')
    expect(describeSchedule('*/15 * * * *')).toBe('on */15 * * * * (UTC)')
  })
})

describe('describeBackupPlan', () => {
  it('names the schedule, what is kept, the command before and the stop', () => {
    expect(describeBackupPlan({ schedule: '0 3 * * *', keep: 7 })).toBe('daily at 03:00 UTC, 7 kept')
    expect(describeBackupPlan({ schedule: '0 3 * * *', keep: 3, before: ['psql', '-c', 'CHECKPOINT'], stop: true }))
      .toBe('daily at 03:00 UTC, 3 kept, after psql -c CHECKPOINT, with the application stopped')
  })
})

describe('a backup row', () => {
  it('adds the archives up', () => {
    expect(backupSize(run())).toBe(2 * 1024 ** 3 + 512 * 1024 ** 2)
    expect(backupSize(failed)).toBe(0)
  })

  it('names where it is kept, and nothing for one that kept nothing', () => {
    expect(describeDestinations(run())).toBe('local + s3')
    expect(describeDestinations(run({ destinations: ['local'] }))).toBe('local')
    expect(describeDestinations(failed)).toBe('—')
  })

  it('shows what is being done to it over its status', () => {
    expect(backupStatusDisplay(run())).toEqual({ tone: 'ok', label: 'Succeeded' })
    expect(backupStatusDisplay(run({ activity: 'verify' }))).toEqual({ tone: 'warn', label: 'Verifying…' })
    expect(backupStatusDisplay(run({ activity: 'restore' }))).toEqual({ tone: 'warn', label: 'Restoring…' })
    expect(backupStatusDisplay(run({ status: 'running', completed_at: null }))).toEqual({ tone: 'warn', label: 'Running' })
    expect(backupStatusDisplay(failed)).toEqual({ tone: 'danger', label: 'Failed' })
  })

  it('is busy while it is taken, verified or restored, and usable only when it succeeded', () => {
    expect(backupBusy(run())).toBe(false)
    expect(backupBusy(run({ activity: 'verify' }))).toBe(true)
    expect(backupBusy(run({ status: 'running' }))).toBe(true)
    expect(backupUsable(run())).toBe(true)
    expect(backupUsable(failed)).toBe(false)
    expect(backupUsable(run({ status: 'running' }))).toBe(false)
  })

  it('says whether it has proven to restore', () => {
    expect(verificationDisplay(run(), now)).toBeNull()
    expect(verificationDisplay(run({ verified_at: '2026-03-01T06:00:00Z' }), now)).toEqual({ tone: 'ok', label: 'Verified 2h ago' })
    expect(verificationDisplay(run({ verify_error: 'the container exited with code 1 on the restored data' }), now)).toEqual({ tone: 'danger', label: 'Did not restore' })
  })
})

describe('describeBackups', () => {
  const plan = { schedule: '0 3 * * *', keep: 7 }

  it('reads like shipwick status when all is well', () => {
    const older = run({ id: 11, started_at: '2026-02-28T03:00:00Z' })
    expect(describeBackups(plan, [run(), older], now)).toEqual({ tone: 'ok', text: 'daily at 03:00 UTC, last 5h ago (2.5 GB), 2 kept' })
  })

  it('leads with the failure when the last one failed, and names the last good one', () => {
    const summary = describeBackups(plan, [failed, run()], now)
    expect(summary.tone).toBe('danger')
    expect(summary.text).toBe('daily at 03:00 UTC, last one failed 1h ago: backups.before exited 2; nothing was archived (last good one 5h ago)')
  })

  it('does not count a backup that is still running', () => {
    const running = run({ id: 14, status: 'running', completed_at: null, volumes: [], destinations: [], started_at: '2026-03-01T07:59:50Z' })
    expect(describeBackups(plan, [running, run()], now).text).toBe('daily at 03:00 UTC, last 5h ago (2.5 GB), 1 kept')
  })

  it('says so when nothing has been taken yet, with and without a schedule', () => {
    expect(describeBackups(plan, [], now)).toEqual({ tone: 'muted', text: 'daily at 03:00 UTC, none taken yet' })
    expect(describeBackups(undefined, [], now).tone).toBe('warn')
    expect(describeBackups(undefined, [run({ trigger: 'manual' })], now).text).toBe('none scheduled, last 5h ago (2.5 GB), 1 kept')
  })
})

describe('stateBackupDisplay', () => {
  const status = (over: Partial<BackupStatus> = {}): BackupStatus => ({ destination: 's3', encrypted: true, state_last_at: new Date(now - 3 * HOUR).toISOString(), state_error: '', ...over })

  it('is fine when the state was backed up to the bucket', () => {
    expect(stateBackupDisplay(status(), now)).toMatchObject({ tone: 'ok', label: 'Backed up 3h ago', possible: true })
  })

  it('points at the bucket when backups stay on the server\'s own disk', () => {
    const display = stateBackupDisplay(status({ destination: 'local' }), now)
    expect(display.tone).toBe('ok')
    expect(display.detail).toContain('SHIPWICK_BACKUP_S3_*')
  })

  it('warns, in doctor\'s words, that without a passphrase the key exists only on the server', () => {
    const display = stateBackupDisplay(status({ encrypted: false, state_last_at: null, state_error: 'the agent\'s state is not backed up: SHIPWICK_BACKUP_PASSPHRASE is not set' }), now)
    expect(display.tone).toBe('warn')
    expect(display.possible).toBe(false)
    expect(display.detail).toContain('SHIPWICK_BACKUP_PASSPHRASE')
    expect(display.detail).toContain('losing it loses every secret')
  })

  it('shows why the last attempt failed and how old the last good one is', () => {
    const display = stateBackupDisplay(status({ state_error: 'put _agent/9/shipwick.db.enc: 403 AccessDenied' }), now)
    expect(display).toMatchObject({ tone: 'danger', label: 'Last backup failed', possible: true })
    expect(display.detail).toBe('Put _agent/9/shipwick.db.enc: 403 AccessDenied. The last good one is from 3h ago.')
    expect(stateBackupDisplay(status({ state_last_at: null, state_error: 'bucket unreachable' }), now).label).toBe('Never backed up')
  })

  it('waits for the first backup of an agent that just started', () => {
    expect(stateBackupDisplay(status({ state_last_at: null }), now)).toMatchObject({ tone: 'warn', label: 'Not backed up yet' })
  })

  it('names the destination', () => {
    expect(describeDestination('s3')).toContain('S3 bucket')
    expect(describeDestination('local')).toBe('The server\'s own disk')
    expect(describeDestination('none')).toBe('Nowhere')
  })
})
