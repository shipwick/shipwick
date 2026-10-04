import { describe, expect, it } from 'vitest'
import { ARCHIVE_ANSWER_TIMEOUT_MS, DOWNLOAD_CLAIM_MS, EXPORT_ANSWER_TIMEOUT_MS, HEADERS_TIMEOUT_MS, IMPORT_ANSWER_TIMEOUT_MS, PROMOTE_ANSWER_TIMEOUT_MS, STOP_ANSWER_TIMEOUT_MS, answerTimeoutMs, isSafeSegment, streamsBody } from '../server/utils/timeouts'
import { attachmentName } from '../server/utils/downloads'

const MINUTE = 60_000

describe('answerTimeoutMs', () => {
  it('gives stop and delete longer than the longest stop_timeout, which they wait for', () => {
    expect(answerTimeoutMs('POST', ['applications', 'web', 'stop'])).toBe(STOP_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('DELETE', ['applications', 'web'])).toBe(STOP_ANSWER_TIMEOUT_MS)
    expect(STOP_ANSWER_TIMEOUT_MS).toBeGreaterThan(10 * MINUTE)
  })

  it('keeps the usual minute for everything that answers at once', () => {
    expect(answerTimeoutMs('GET', ['server'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('GET', ['applications', 'web'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['applications', 'web', 'start'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['applications', 'web', 'redeploy'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['applications', 'web', 'backups', '12', 'restore'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['server', 'rotate-key'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('DELETE', ['secrets', 'STRIPE_KEY'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('DELETE', ['applications', 'web', 'backups', '12'])).toBe(HEADERS_TIMEOUT_MS)
    expect(HEADERS_TIMEOUT_MS).toBe(MINUTE)
  })

  it('waits for a promotion, which answers when the last application is ready', () => {
    expect(answerTimeoutMs('POST', ['standby', 'promote'])).toBe(PROMOTE_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['standby', 'pull'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('GET', ['standby'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['exports'])).toBe(HEADERS_TIMEOUT_MS)
  })

  it('is not fooled by an application named stop', () => {
    expect(answerTimeoutMs('GET', ['applications', 'stop'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['applications', 'stop', 'start'])).toBe(HEADERS_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['applications', 'stop', 'stop'])).toBe(STOP_ANSWER_TIMEOUT_MS)
  })

  it('waits for an archive: an upload being extracted, a backup being fetched and decrypted', () => {
    expect(answerTimeoutMs('PUT', ['applications', 'postgres', 'volumes', 'data', 'archive'])).toBe(ARCHIVE_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('GET', ['applications', 'postgres', 'volumes', 'data', 'archive'])).toBe(ARCHIVE_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('GET', ['applications', 'postgres', 'backups', '12', 'volumes', 'data', 'archive'])).toBe(ARCHIVE_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('PUT', ['certificates', 'example.com'])).toBe(ARCHIVE_ANSWER_TIMEOUT_MS)
  })
})

describe('an export and an import', () => {
  it('waits for an import from its last byte as long as for a promotion: the applications at the end of the file are still being deployed', () => {
    expect(answerTimeoutMs('POST', ['import'])).toBe(IMPORT_ANSWER_TIMEOUT_MS)
    expect(IMPORT_ANSWER_TIMEOUT_MS).toBeGreaterThanOrEqual(30 * MINUTE)
    expect(answerTimeoutMs('GET', ['import'])).toBe(HEADERS_TIMEOUT_MS)
  })

  it('waits for the first byte of an export as for an archive', () => {
    expect(answerTimeoutMs('POST', ['export'])).toBe(EXPORT_ANSWER_TIMEOUT_MS)
    expect(EXPORT_ANSWER_TIMEOUT_MS).toBe(ARCHIVE_ANSWER_TIMEOUT_MS)
    expect(answerTimeoutMs('POST', ['exports'])).toBe(HEADERS_TIMEOUT_MS)
  })

  it('passes on as it arrives every PUT, and of the POSTs only the import', () => {
    expect(streamsBody('PUT', ['applications', 'postgres', 'volumes', 'data', 'archive'])).toBe(true)
    expect(streamsBody('PUT', ['secrets', 'STRIPE_KEY'])).toBe(true)
    expect(streamsBody('POST', ['import'])).toBe(true)
    for (const segments of [['export'], ['exports'], ['applications'], ['validate'], ['applications', 'import'], ['import', 'x'], ['standby', 'pull']]) {
      expect(streamsBody('POST', segments), segments.join('/')).toBe(false)
    }
    expect(streamsBody('GET', ['import'])).toBe(false)
    expect(streamsBody('DELETE', ['applications', 'import'])).toBe(false)
  })

  it('holds a started export for less than the minute after which the agent gives a stalled write up', () => {
    expect(DOWNLOAD_CLAIM_MS).toBeLessThan(MINUTE)
  })

  it('shows the file name the agent chose, and none that is not a plain name', () => {
    expect(attachmentName('attachment; filename="shipwick-export-20261004-005656.swexport"')).toBe('shipwick-export-20261004-005656.swexport')
    expect(attachmentName('attachment; filename="../../etc/passwd"')).toBe('')
    expect(attachmentName('attachment; filename="<script>.swexport"')).toBe('')
    expect(attachmentName(undefined)).toBe('')
  })
})

describe('isSafeSegment', () => {
  it('lets names, numbers and fixed words through', () => {
    for (const segment of ['applications', 'my-api', '12', 'DATABASE_PASSWORD', 'shipwick_postgres_data', 'rotate-key', 'latest', 'ghcr.io']) {
      expect(isSafeSegment(segment), segment).toBe(true)
    }
  })

  it('lets a registry carry a port and a certificate be a wildcard', () => {
    expect(isSafeSegment('registry.example.com:5000')).toBe(true)
    expect(isSafeSegment('*.example.com')).toBe(true)
  })

  it('refuses everything that could leave the path or the API', () => {
    for (const segment of ['.', '..', '', 'a/b', 'a\\b', 'a b', 'a?b', 'a#b', 'a%2Fb', '*', '*.', '**.example.com', 'a.*.example.com', 'host:port', 'host:', ':5000', 'a:1:2', 'https://ghcr.io', 'a\nb']) {
      expect(isSafeSegment(segment), JSON.stringify(segment)).toBe(false)
    }
  })
})
