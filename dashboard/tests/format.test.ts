import { describe, expect, it } from 'vitest'
import {
  durationBetween,
  formatAbsoluteUtc,
  formatBytes,
  formatCores,
  formatDuration,
  formatLogTime,
  formatMemoryUsage,
  formatPercent,
  formatRelativeTime,
  pluralize,
  splitImage,
} from '../app/utils/format'

describe('formatBytes', () => {
  it('uses 1024-based units labelled like deploy.yaml', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1024 ** 3)).toBe('1 GB')
    expect(formatBytes(432013312)).toBe('412 MB')
    expect(formatBytes(1.5 * 1024 ** 3)).toBe('1.5 GB')
    expect(formatBytes(8 * 1024 ** 3 - 212 * 1024 ** 2)).toBe('7.8 GB')
  })

  it('carries into the next unit instead of printing 1024 MB', () => {
    expect(formatBytes(1024 ** 3 - 1)).toBe('1 GB')
    expect(formatBytes(1024 ** 2 - 1)).toBe('1 MB')
  })

  it('keeps one decimal only below 10', () => {
    expect(formatBytes(9.96 * 1024 ** 2)).toBe('10 MB')
    expect(formatBytes(9.5 * 1024 ** 2)).toBe('9.5 MB')
    expect(formatBytes(10.4 * 1024 ** 2)).toBe('10 MB')
  })

  it('renders missing and invalid values as a dash', () => {
    expect(formatBytes(null)).toBe('—')
    expect(formatBytes(undefined)).toBe('—')
    expect(formatBytes(Number.NaN)).toBe('—')
    expect(formatBytes(-1)).toBe('—')
  })
})

describe('formatMemoryUsage', () => {
  it('shows the limit when there is one', () => {
    expect(formatMemoryUsage(432013312, 1024 ** 3)).toBe('412 MB / 1 GB')
  })
  it('omits an unlimited (0) limit', () => {
    expect(formatMemoryUsage(432013312, 0)).toBe('412 MB')
    expect(formatMemoryUsage(432013312, undefined)).toBe('412 MB')
  })
  it('is a dash without a sample', () => {
    expect(formatMemoryUsage(undefined, 1024)).toBe('—')
  })
})

describe('formatPercent', () => {
  it('keeps a decimal below 10 and rounds above', () => {
    expect(formatPercent(0)).toBe('0%')
    expect(formatPercent(4.26)).toBe('4.3%')
    expect(formatPercent(42.1)).toBe('42%')
    expect(formatPercent(200)).toBe('200%')
    expect(formatPercent(9.96)).toBe('10%')
  })
  it('is a dash for missing values', () => {
    expect(formatPercent(null)).toBe('—')
    expect(formatPercent(undefined)).toBe('—')
    expect(formatPercent(Number.POSITIVE_INFINITY)).toBe('—')
  })
})

describe('formatCores', () => {
  it('formats limits and the lack of one', () => {
    expect(formatCores(undefined)).toBe('unlimited')
    expect(formatCores(0)).toBe('unlimited')
    expect(formatCores(1)).toBe('1 core')
    expect(formatCores(0.5)).toBe('0.5 cores')
    expect(formatCores(2)).toBe('2 cores')
  })
})

describe('formatDuration', () => {
  it('picks the unit by magnitude', () => {
    expect(formatDuration(0)).toBe('0ms')
    expect(formatDuration(850)).toBe('850ms')
    expect(formatDuration(1000)).toBe('1s')
    expect(formatDuration(6100)).toBe('6.1s')
    expect(formatDuration(59_900)).toBe('59.9s')
    expect(formatDuration(60_000)).toBe('1m')
    expect(formatDuration(72_000)).toBe('1m 12s')
    expect(formatDuration(2 * 3600_000 + 5 * 60_000)).toBe('2h 5m')
    expect(formatDuration(3 * 86_400_000 + 4 * 3600_000)).toBe('3d 4h')
    expect(formatDuration(86_400_000)).toBe('1d')
  })
  it('never prints 60s', () => {
    expect(formatDuration(59_960)).toBe('1m')
  })
  it('is a dash for missing or negative input', () => {
    expect(formatDuration(null)).toBe('—')
    expect(formatDuration(-5)).toBe('—')
  })
})

describe('durationBetween', () => {
  it('returns milliseconds between two timestamps', () => {
    expect(durationBetween('2026-03-01T10:00:00Z', '2026-03-01T10:00:06.100Z')).toBe(6100)
  })
  it('is null while a deployment is unfinished or a timestamp is invalid', () => {
    expect(durationBetween('2026-03-01T10:00:00Z', null)).toBeNull()
    expect(durationBetween('nonsense', '2026-03-01T10:00:00Z')).toBeNull()
  })
  it('never goes negative', () => {
    expect(durationBetween('2026-03-01T10:00:01Z', '2026-03-01T10:00:00Z')).toBe(0)
  })
})

describe('formatRelativeTime', () => {
  const now = Date.parse('2026-03-01T12:00:00Z')
  const at = (msAgo: number) => new Date(now - msAgo).toISOString()

  it('formats the past', () => {
    expect(formatRelativeTime(at(0), now)).toBe('just now')
    expect(formatRelativeTime(at(4_000), now)).toBe('just now')
    expect(formatRelativeTime(at(34_000), now)).toBe('34s ago')
    expect(formatRelativeTime(at(2 * 60_000), now)).toBe('2m ago')
    expect(formatRelativeTime(at(3 * 3600_000), now)).toBe('3h ago')
    expect(formatRelativeTime(at(5 * 86_400_000), now)).toBe('5d ago')
    expect(formatRelativeTime(at(65 * 86_400_000), now)).toBe('2mo ago')
    expect(formatRelativeTime(at(800 * 86_400_000), now)).toBe('2y ago')
  })
  it('tolerates clock skew between agent and browser', () => {
    expect(formatRelativeTime(at(-20_000), now)).toBe('just now')
    expect(formatRelativeTime(at(-10 * 60_000), now)).toBe('in 10m')
  })
  it('is a dash for missing or invalid input', () => {
    expect(formatRelativeTime(null, now)).toBe('—')
    expect(formatRelativeTime('nope', now)).toBe('—')
  })
})

describe('absolute timestamps', () => {
  it('formats UTC regardless of the local time zone', () => {
    expect(formatAbsoluteUtc('2026-03-01T10:00:00Z')).toBe('2026-03-01 10:00:00 UTC')
    expect(formatAbsoluteUtc('2026-03-01T12:30:05+02:00')).toBe('2026-03-01 10:30:05 UTC')
    expect(formatAbsoluteUtc(null)).toBe('—')
  })
  it('formats log times with milliseconds', () => {
    expect(formatLogTime('2026-03-01T10:00:00.12Z')).toBe('10:00:00.120')
    expect(formatLogTime('')).toBe('')
  })
})

describe('splitImage', () => {
  it('separates repository and tag', () => {
    expect(splitImage('ghcr.io/acme/my-api:1.4.2')).toEqual({ repository: 'ghcr.io/acme/my-api', tag: '1.4.2' })
    expect(splitImage('nginx')).toEqual({ repository: 'nginx', tag: '' })
  })
  it('does not mistake a registry port for a tag', () => {
    expect(splitImage('registry.example.com:5000/acme/worker')).toEqual({ repository: 'registry.example.com:5000/acme/worker', tag: '' })
    expect(splitImage('registry.example.com:5000/acme/worker:0.9.1')).toEqual({ repository: 'registry.example.com:5000/acme/worker', tag: '0.9.1' })
  })
  it('ignores a digest when looking for the tag', () => {
    expect(splitImage('nginx:1.27@sha256:abc').tag).toBe('1.27')
  })
})

describe('pluralize', () => {
  it('handles one and many', () => {
    expect(pluralize(1, 'route')).toBe('1 route')
    expect(pluralize(3, 'route')).toBe('3 routes')
    expect(pluralize(0, 'replica')).toBe('0 replicas')
  })
})
