import { describe, expect, it } from 'vitest'
import {
  certificateHostnameProblem,
  certificateRemovalConsequence,
  chainProblem,
  describeIssued,
  expiryDisplay,
  formatDate,
  hostnameCertificateDisplay,
  keyProblem,
  replacesCertificate,
  sortByExpiry,
} from '../app/utils/certificates'

const DAY = 24 * 60 * 60 * 1000
const now = Date.parse('2026-03-01T12:00:00Z')
const CHAIN = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n'
const KEY = '-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n'

describe('hostnameCertificateDisplay', () => {
  it('needs no badge for a certificate that is in order', () => {
    expect(hostnameCertificateDisplay({ status: 'ok', message: '' })).toBeNull()
  })

  it('names every other state, and tells an expired certificate from an expiring one', () => {
    expect(hostnameCertificateDisplay({ status: 'waiting_for_dns', message: 'does not resolve yet' })).toEqual({ tone: 'warn', label: 'Waiting for DNS' })
    expect(hostnameCertificateDisplay({ status: 'obtaining', message: 'the proxy has no certificate for it yet' })).toEqual({ tone: 'warn', label: 'Obtaining certificate' })
    expect(hostnameCertificateDisplay({ status: 'expiring', message: 'expires in 9 days, on 2026-03-10' })).toEqual({ tone: 'warn', label: 'Certificate expiring' })
    expect(hostnameCertificateDisplay({ status: 'expiring', message: 'expired on 2026-02-27' })).toEqual({ tone: 'danger', label: 'Certificate expired' })
    expect(hostnameCertificateDisplay({ status: 'unknown', message: 'not checked yet' })).toEqual({ tone: 'muted', label: 'Certificate unknown' })
  })

  it('shows a status it does not know rather than hiding it', () => {
    expect(hostnameCertificateDisplay({ status: 'on_hold', message: '' })).toEqual({ tone: 'muted', label: 'on hold' })
  })
})

describe('describeIssued', () => {
  it('names the issuer and the last day', () => {
    expect(describeIssued({ issuer: 'Let\'s Encrypt E7', not_after: '2026-05-20T08:00:00Z' })).toBe('Let\'s Encrypt E7, valid until 2026-05-20')
    expect(describeIssued({ issuer: '', not_after: null })).toBe('')
    expect(formatDate('2026-05-20T23:59:59Z')).toBe('2026-05-20')
    expect(formatDate(null)).toBe('—')
  })
})

describe('expiryDisplay', () => {
  it('marks the last 30 days and what is past', () => {
    expect(expiryDisplay(new Date(now + 12 * DAY + 1000).toISOString(), now)).toEqual({ tone: 'warn', label: 'expires in 12 days' })
    expect(expiryDisplay(new Date(now + 1 * DAY + 1000).toISOString(), now)).toEqual({ tone: 'warn', label: 'expires in 1 day' })
    expect(expiryDisplay(new Date(now + 3600_000).toISOString(), now)).toEqual({ tone: 'warn', label: 'expires today' })
    expect(expiryDisplay(new Date(now - 4 * DAY).toISOString(), now)).toEqual({ tone: 'danger', label: 'expired 4 days ago' })
    expect(expiryDisplay(new Date(now - 3600_000).toISOString(), now)).toEqual({ tone: 'danger', label: 'expired today' })
  })

  it('is just the date for a certificate with time left', () => {
    expect(expiryDisplay('2027-09-01T00:00:00Z', now)).toEqual({ tone: 'muted', label: '2027-09-01' })
    expect(expiryDisplay(new Date(now + 30 * DAY).toISOString(), now).tone).toBe('muted')
  })

  it('puts what needs replacing first', () => {
    const list = [
      { hostname: 'b.example.com', not_after: '2027-01-01T00:00:00Z' },
      { hostname: 'a.example.com', not_after: '2026-02-01T00:00:00Z' },
      { hostname: 'c.example.com', not_after: '2026-03-10T00:00:00Z' },
    ]
    expect(sortByExpiry(list).map(c => c.hostname)).toEqual(['a.example.com', 'c.example.com', 'b.example.com'])
  })
})

describe('the form', () => {
  it('takes a hostname as in deploy.yaml, or a wildcard', () => {
    expect(certificateHostnameProblem('example.com')).toBe('')
    expect(certificateHostnameProblem('*.example.com')).toBe('')
    expect(certificateHostnameProblem('')).toBe('')
    expect(certificateHostnameProblem('https://example.com')).not.toBe('')
    expect(certificateHostnameProblem('*.*.example.com')).not.toBe('')
    expect(certificateHostnameProblem('a.*.example.com')).not.toBe('')
    expect(certificateHostnameProblem('localhost')).not.toBe('')
  })

  it('catches the two files being swapped', () => {
    expect(chainProblem(CHAIN)).toBe('')
    expect(chainProblem(KEY)).toContain('private key')
    expect(chainProblem(CHAIN + KEY)).toContain('private key')
    expect(chainProblem('hello')).toContain('BEGIN CERTIFICATE')
    expect(keyProblem(KEY)).toBe('')
    expect(keyProblem('-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----')).toBe('')
    expect(keyProblem(CHAIN)).toContain('BEGIN PRIVATE KEY')
  })

  it('refuses a key behind a passphrase, which the proxy cannot enter', () => {
    expect(keyProblem('-----BEGIN ENCRYPTED PRIVATE KEY-----\nx\n-----END ENCRYPTED PRIVATE KEY-----')).toContain('passphrase')
    expect(keyProblem('-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nx')).toContain('passphrase')
  })

  it('never quotes the key in a message', () => {
    const secret = 'c2VjcmV0LWtleS1tYXRlcmlhbA'
    expect(keyProblem(`not a key ${secret}`)).not.toContain(secret)
    expect(chainProblem(`-----BEGIN PRIVATE KEY-----\n${secret}`)).not.toContain(secret)
  })

  it('holds both to the agent\'s 64 KB', () => {
    expect(chainProblem(CHAIN + 'A'.repeat(64 * 1024))).toContain('64 KB')
    expect(keyProblem(KEY + 'A'.repeat(64 * 1024))).toContain('64 KB')
    expect(chainProblem('')).toBe('')
    expect(keyProblem('  ')).toBe('')
  })

  it('says when a name is already stored', () => {
    expect(replacesCertificate('example.com', [{ hostname: 'example.com' }])).toBe(true)
    expect(replacesCertificate('other.example.com', [{ hostname: 'example.com' }])).toBe(false)
    expect(replacesCertificate('', [{ hostname: '' }])).toBe(false)
  })
})

describe('certificateRemovalConsequence', () => {
  it('says the hostnames go back to automatic certificates', () => {
    const text = certificateRemovalConsequence({ subjects: ['example.com', 'www.example.com'] }, false)
    expect(text).toContain('example.com, www.example.com')
    expect(text).toContain('certificates the proxy obtains by itself')
    expect(text).not.toContain('wildcard')
  })

  it('warns that a wildcard is no longer served without a DNS challenge, and only then', () => {
    expect(certificateRemovalConsequence({ subjects: ['*.example.com'] }, false)).toContain('no longer served')
    expect(certificateRemovalConsequence({ subjects: ['*.example.com'] }, true)).not.toContain('no longer served')
  })
})
