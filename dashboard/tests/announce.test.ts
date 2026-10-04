import { describe, expect, it } from 'vitest'
import type { BackupRun, Promotion } from '../app/types/api'
import { REPEAT_AFTER_MS, backupAnnouncement, progressAnnouncement, promotionAnnouncement, shouldAnnounce } from '../app/utils/announce'
import type { DeploymentProgress, ProgressStep } from '../app/utils/deploymentProgress'

const at = Date.parse('2026-03-01T10:00:00Z')

describe('what is said, and what is not said again', () => {
  it('says a sentence the first time', () => {
    expect(shouldAnnounce(null, 'Copied', at)).toBe(true)
  })

  it('does not say nothing', () => {
    expect(shouldAnnounce(null, '', at)).toBe(false)
    expect(shouldAnnounce(null, '   ', at)).toBe(false)
  })

  it('does not repeat a sentence that every poll would repeat', () => {
    const last = { text: 'Agent unreachable. Showing the last data, retrying.', at }
    expect(shouldAnnounce(last, last.text, at + 3000)).toBe(false)
    expect(shouldAnnounce(last, last.text, at + REPEAT_AFTER_MS - 1)).toBe(false)
  })

  it('says it again once enough time has passed for it to be news', () => {
    const last = { text: 'Agent unreachable. Showing the last data, retrying.', at }
    expect(shouldAnnounce(last, last.text, at + REPEAT_AFTER_MS)).toBe(true)
  })

  it('says another sentence at once', () => {
    expect(shouldAnnounce({ text: 'Copied', at }, 'Connected again: the data is live.', at + 10)).toBe(true)
  })
})

type Seen = Pick<DeploymentProgress, 'deploymentId' | 'steps' | 'phase' | 'error'>

function step(id: number, message: string, kind: ProgressStep['kind'] = 'done'): ProgressStep {
  return { id, message, kind, at: '2026-03-01T10:00:01Z' }
}

function seen(steps: ProgressStep[], over: Partial<Seen> = {}): Seen {
  return { deploymentId: 7, steps, phase: 'running', error: '', ...over }
}

describe('a followed deployment, for a screen reader', () => {
  it('says the headline when the deployment starts to be followed', () => {
    expect(progressAnnouncement(null, seen([]), 'Deploying 1.5.0')).toBe('Deploying 1.5.0')
  })

  it('says the headline for another deployment than the one before', () => {
    expect(progressAnnouncement(seen([step(1, 'Pulled image')]), seen([], { deploymentId: 8 }), 'Deploying 1.6.0')).toBe('Deploying 1.6.0')
  })

  it('says nothing for a poll that brought nothing new: the clock is not news', () => {
    const state = seen([step(1, 'Pulled image')])
    expect(progressAnnouncement(state, seen([step(1, 'Pulled image')]), 'Deploying 1.5.0')).toBe('')
  })

  it('says each new step once, in order', () => {
    const before = seen([step(1, 'Pulled image')])
    const after = seen([step(1, 'Pulled image'), step(2, 'Started 1 container'), step(3, 'Replica 1 passed health checks')])
    expect(progressAnnouncement(before, after, 'Deploying 1.5.0')).toBe('Started 1 container. Replica 1 passed health checks')
  })

  it('names a warning and a failed step as such, since their color is not heard', () => {
    const before = seen([])
    const after = seen([step(1, 'Using the local copy of the image', 'warning'), step(2, 'Replica 1 exited', 'error')])
    expect(progressAnnouncement(before, after, 'Deploying 1.5.0')).toBe('Warning: Using the local copy of the image. Failed: Replica 1 exited')
  })

  it('says how it ended', () => {
    const before = seen([step(1, 'Pulled image')])
    const after = seen([step(1, 'Pulled image'), step(2, 'Deployment successful')], { phase: 'succeeded' })
    expect(progressAnnouncement(before, after, 'Deployed 1.5.0 in 12s')).toBe('Deployment successful. Deployed 1.5.0 in 12s')
  })

  it('says the failure and its cause when the agent reports it, also while a rollback still runs', () => {
    const before = seen([step(1, 'Pulled image')])
    const after = seen([step(1, 'Pulled image')], { error: 'replica 2 exited with code 1' })
    expect(progressAnnouncement(before, after, 'Deployment of 1.5.0 failed — rolling back')).toBe('Deployment of 1.5.0 failed — rolling back. replica 2 exited with code 1')
  })

  it('does not repeat the cause when the rollback is over', () => {
    const before = seen([], { error: 'replica 2 exited with code 1' })
    const after = seen([], { error: 'replica 2 exited with code 1', phase: 'rolled_back' })
    expect(progressAnnouncement(before, after, 'Failed — rolled back to 1.4.2')).toBe('Failed — rolled back to 1.4.2')
  })
})

function promotion(statuses: Record<string, string>, completed: string | null = null): Promotion {
  return {
    id: 1,
    status: completed ? 'succeeded' : 'running',
    started_at: '2026-03-01T10:00:00Z',
    completed_at: completed,
    records: [],
    applications: Object.entries(statuses).map(([name, status]) => ({ name, status, message: '' })),
  }
}

describe('a promotion, for a screen reader', () => {
  it('says where a running promotion is when the page opens on it', () => {
    expect(promotionAnnouncement(null, promotion({ postgres: 'starting', shop: 'pending' }))).toBe('Promoting: starting postgres (1 of 2)')
  })

  it('says nothing about a promotion that was over when the page opened', () => {
    expect(promotionAnnouncement(null, promotion({ postgres: 'running' }, '2026-03-01T10:01:00Z'))).toBe('')
  })

  it('says nothing for a poll in which no application moved', () => {
    const state = promotion({ postgres: 'starting', shop: 'pending' })
    expect(promotionAnnouncement(state, promotion({ postgres: 'starting', shop: 'pending' }))).toBe('')
  })

  it('says each application whose state moved, and no other', () => {
    const before = promotion({ postgres: 'starting', shop: 'pending', docs: 'pending' })
    const after = promotion({ postgres: 'running', shop: 'starting', docs: 'pending' })
    expect(promotionAnnouncement(before, after)).toBe('postgres: Running. shop: Starting')
  })

  it('says how the promotion ended', () => {
    const before = promotion({ postgres: 'running', shop: 'starting' })
    const after = promotion({ postgres: 'running', shop: 'started' }, '2026-03-01T10:02:00Z')
    expect(promotionAnnouncement(before, after)).toBe('shop: Started, not ready yet. Promotion over: 1 running, 1 not ready yet')
  })
})

function backup(over: Partial<BackupRun> = {}): BackupRun {
  return {
    id: 12,
    trigger: 'manual',
    status: 'succeeded',
    started_at: '2026-03-01T03:00:00Z',
    completed_at: '2026-03-01T03:00:41Z',
    volumes: [],
    destinations: ['local'],
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

describe('the backups of an application, for a screen reader', () => {
  it('says nothing while the list stays as it was', () => {
    expect(backupAnnouncement([backup()], [backup()])).toBe('')
    const running = backup({ status: 'running', completed_at: null })
    expect(backupAnnouncement([running], [running])).toBe('')
  })

  it('says nothing about a backup that has just begun: the button that began it said so', () => {
    expect(backupAnnouncement([], [backup({ status: 'running', completed_at: null })])).toBe('')
  })

  it('says a backup that was being taken and is done', () => {
    expect(backupAnnouncement([backup({ status: 'running', completed_at: null })], [backup()])).toBe('Backup 12 taken')
  })

  it('says a backup that failed, with the reason', () => {
    const before = [backup({ status: 'running', completed_at: null })]
    expect(backupAnnouncement(before, [backup({ status: 'failed', error: 'backups.before exited 2' })])).toBe('Backup 12 failed: backups.before exited 2')
  })

  it('says how a verification ended', () => {
    const before = [backup({ activity: 'verify' })]
    expect(backupAnnouncement(before, [backup({ verified_at: '2026-03-01T04:00:00Z' })])).toBe('Backup 12 verified: it restores')
    expect(backupAnnouncement(before, [backup({ verify_error: 'the container exited 1' })])).toBe('Backup 12 did not restore: the container exited 1')
  })

  it('says how a restore ended', () => {
    const before = [backup({ activity: 'restore' })]
    expect(backupAnnouncement(before, [backup({ restored_at: '2026-03-01T04:00:00Z' })])).toBe('Backup 12 restored')
    expect(backupAnnouncement(before, [backup({ restore_error: 'no space left on device' })])).toBe('Restoring backup 12 failed: no space left on device')
  })

  it('names each backup that finished, and none that was at rest', () => {
    const before = [backup({ id: 14, status: 'running', completed_at: null }), backup({ id: 13, activity: 'verify' }), backup({ id: 12 })]
    const after = [backup({ id: 14 }), backup({ id: 13, verified_at: '2026-03-01T04:00:00Z' }), backup({ id: 12 })]
    expect(backupAnnouncement(before, after)).toBe('Backup 14 taken. Backup 13 verified: it restores')
  })
})
