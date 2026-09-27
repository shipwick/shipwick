import { describe, expect, it } from 'vitest'
import type { VolumeInfo } from '../app/types/api'
import { formatVolumeSize, removable, sortVolumes, volumeRemovalConsequence, volumeStatusDisplay } from '../app/utils/volumes'

const inUse: VolumeInfo = { name: 'shipwick_postgres_data', application: 'postgres', volume: 'data', size_bytes: 2684354560, orphan: false }
const orphan: VolumeInfo = { name: 'shipwick_pgtest_data', application: 'pgtest', volume: 'data', size_bytes: 13631488, orphan: true }
const unsized: VolumeInfo = { name: 'shipwick_redis_cache', application: 'redis', volume: 'cache', size_bytes: -1, orphan: false }

describe('volumeStatusDisplay', () => {
  it('reads "in use" for a live application and "application deleted" for an orphan', () => {
    expect(volumeStatusDisplay(inUse)).toEqual({ tone: 'ok', label: 'in use' })
    expect(volumeStatusDisplay(orphan)).toEqual({ tone: 'muted', label: 'application deleted' })
  })
})

describe('formatVolumeSize', () => {
  it('formats bytes and calls the daemon\'s -1 "unknown"', () => {
    expect(formatVolumeSize(2684354560)).toBe('2.5 GB')
    expect(formatVolumeSize(13631488)).toBe('13 MB')
    expect(formatVolumeSize(0)).toBe('0 B')
    expect(formatVolumeSize(-1)).toBe('unknown')
  })
})

describe('removable', () => {
  it('allows only volumes whose application was deleted', () => {
    expect(removable(orphan)).toBe(true)
    expect(removable(inUse)).toBe(false)
    expect(removable(unsized)).toBe(false)
  })
})

describe('volumeRemovalConsequence', () => {
  it('confirms with the size and names the deleted application', () => {
    const text = volumeRemovalConsequence(orphan)
    expect(text).toContain('The 13 MB in it is deleted')
    expect(text).toContain('belonged to pgtest')
    expect(text).toContain('cannot be undone')
  })

  it('does not invent a size the daemon did not report', () => {
    expect(volumeRemovalConsequence({ ...orphan, size_bytes: -1 })).toContain('Everything in it is deleted')
  })
})

describe('sortVolumes', () => {
  it('puts orphans first, then sorts by name like the agent', () => {
    expect(sortVolumes([unsized, inUse, orphan]).map(v => v.name)).toEqual(['shipwick_pgtest_data', 'shipwick_postgres_data', 'shipwick_redis_cache'])
  })

  it('leaves the input alone', () => {
    const input = [inUse, orphan]
    sortVolumes(input)
    expect(input.map(v => v.name)).toEqual(['shipwick_postgres_data', 'shipwick_pgtest_data'])
  })
})
