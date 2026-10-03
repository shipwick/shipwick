import type { Certificate, HostnameCertificate } from '~/types/api'
import { pluralize } from '~/utils/format'
import { isWildcard } from '~/utils/spec'
import type { StatusDisplay } from '~/utils/status'

/**
 * Display rules for certificates: the one the proxy presents for each of an
 * application's hostnames, and the ones the operator supplied. Pure, so the
 * wording and the form's checks are unit-tested; the agent checks everything
 * again and is the one that reads the PEM.
 */

const DAY = 24 * 60 * 60 * 1000

/** "2026-05-20": a certificate's date, in UTC, the way the agent's messages write it. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  return Number.isNaN(t) ? '—' : new Date(t).toISOString().slice(0, 10)
}

/**
 * The badge next to a hostname, for anything that is not in order; null for
 * `ok`, which needs no badge. An expired certificate reads red, the rest amber.
 */
export function hostnameCertificateDisplay(certificate: Pick<HostnameCertificate, 'status' | 'message'>): StatusDisplay | null {
  switch (certificate.status) {
    case 'ok': return null
    case 'expiring':
      return certificate.message.startsWith('expired')
        ? { tone: 'danger', label: 'Certificate expired' }
        : { tone: 'warn', label: 'Certificate expiring' }
    case 'obtaining': return { tone: 'warn', label: 'Obtaining certificate' }
    case 'waiting_for_dns': return { tone: 'warn', label: 'Waiting for DNS' }
    case 'unknown': return { tone: 'muted', label: 'Certificate unknown' }
    default: return { tone: 'muted', label: certificate.status.replace(/_/g, ' ') }
  }
}

/** "Let's Encrypt E7, valid until 2026-05-20"; "" before a certificate has been seen. */
export function describeIssued(certificate: Pick<HostnameCertificate, 'issuer' | 'not_after'>): string {
  if (!certificate.not_after) return certificate.issuer
  const until = `valid until ${formatDate(certificate.not_after)}`
  return certificate.issuer ? `${certificate.issuer}, ${until}` : until
}

/** A supplied certificate is marked in its last 30 days, as `shipwick cert ls` and `doctor` do: nothing renews it by itself. */
export const EXPIRY_WARNING_DAYS = 30

/** How a supplied certificate's end stands: "expired 3 days ago" (red), "expires in 12 days" (amber), or the plain date. */
export function expiryDisplay(notAfter: string, now: number = Date.now()): StatusDisplay {
  const end = Date.parse(notAfter)
  if (Number.isNaN(end)) return { tone: 'muted', label: '—' }
  if (end <= now) {
    const days = Math.floor((now - end) / DAY)
    return { tone: 'danger', label: days === 0 ? 'expired today' : `expired ${pluralize(days, 'day')} ago` }
  }
  const days = Math.floor((end - now) / DAY)
  if (days < EXPIRY_WARNING_DAYS) return { tone: 'warn', label: days === 0 ? 'expires today' : `expires in ${pluralize(days, 'day')}` }
  return { tone: 'muted', label: formatDate(notAfter) }
}

/** Expired ones first, then the soonest to expire: what needs replacing is at the top. */
export function sortByExpiry<T extends Pick<Certificate, 'hostname' | 'not_after'>>(certificates: readonly T[]): T[] {
  return [...certificates].sort((a, b) => Date.parse(a.not_after) - Date.parse(b.not_after) || a.hostname.localeCompare(b.hostname))
}

/** The agent's limit on the chain, and on the key. */
export const MAX_PEM_BYTES = 64 * 1024

const HOSTNAME = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/

/** Why a certificate cannot be stored under this name, or "" when it can. One leading `*.` is a wildcard. */
export function certificateHostnameProblem(hostname: string): string {
  if (hostname === '') return ''
  const name = isWildcard(hostname) ? hostname.slice(2) : hostname
  if (!HOSTNAME.test(name)) return 'A hostname as in deploy.yaml, e.g. example.com, or *.example.com for a wildcard certificate.'
  return ''
}

/** Why this cannot be the chain, or "" when it may be. The agent parses it; this only catches the two files being swapped. */
export function chainProblem(pem: string): string {
  if (pem.trim() === '') return ''
  if (/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(pem)) return 'This holds a private key: give the key in the other field, and only certificates here.'
  if (!pem.includes('-----BEGIN CERTIFICATE-----')) return 'Expected one or more -----BEGIN CERTIFICATE----- blocks, the hostname\'s own certificate first (fullchain.pem).'
  if (new TextEncoder().encode(pem).length > MAX_PEM_BYTES) return 'The chain must be at most 64 KB.'
  return ''
}

/** Why this cannot be the key, or "" when it may be. Never quotes the key. */
export function keyProblem(pem: string): string {
  if (pem.trim() === '') return ''
  if (pem.includes('-----BEGIN ENCRYPTED PRIVATE KEY-----') || pem.includes('Proc-Type: 4,ENCRYPTED')) return 'The key is protected by a passphrase; the proxy cannot ask for one. Remove it first.'
  if (!/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(pem)) return 'Expected a -----BEGIN PRIVATE KEY----- block (privkey.pem).'
  if (new TextEncoder().encode(pem).length > MAX_PEM_BYTES) return 'The key must be at most 64 KB.'
  return ''
}

/** The stored certificate a hostname would replace, if any: PUT creates and replaces alike. */
export function replacesCertificate(hostname: string, stored: readonly Pick<Certificate, 'hostname'>[]): boolean {
  return hostname !== '' && stored.some(c => c.hostname === hostname)
}

/** What removing a supplied certificate does, for the confirmation. */
export function certificateRemovalConsequence(certificate: Pick<Certificate, 'subjects'>, dnsChallenge: boolean): string {
  const names = certificate.subjects.join(', ')
  const wildcard = certificate.subjects.some(isWildcard) && !dnsChallenge
    ? ' A wildcard hostname among them is no longer served: without a DNS challenge the proxy cannot obtain a certificate for it.'
    : ''
  return `The hostnames it covers (${names}) go back to certificates the proxy obtains by itself, and to waiting for DNS.${wildcard}`
}
