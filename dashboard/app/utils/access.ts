import type { AccessRule, AuditEntry, TokenIdentity } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { formatRelativeTime } from '~/utils/format'
import type { StatusDisplay, Tone } from '~/utils/status'

/**
 * Who may do what, beyond the three roles: a deploy token limited to some
 * applications, a token that expires, and the audit trail. Pure, so the rules
 * and the wording are unit-tested. The agent decides for real on every
 * request; this only chooses what to offer and how to explain a refusal.
 */

const DAY_MS = 24 * 60 * 60 * 1000

/** The agent's own threshold (pkg/api, ExpiryWarning): from here on an expiry is worth a warning. */
export const EXPIRY_WARNING_MS = 14 * DAY_MS

/** The agent refuses a longer list. */
export const MAX_TOKEN_APPLICATIONS = 50

const APPLICATION_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** The applications a token is limited to; [] when it is not, and for an agent that does not say. */
export function tokenLimits(token: Pick<TokenIdentity, 'applications'> | null | undefined): string[] {
  return token?.applications ?? []
}

/**
 * Whether the token may deploy, roll back, stop, start, run and back up
 * `application` — also one that does not exist yet. The agent's rule:
 * admin always, deploy when it is not limited or the application is on its
 * list. A token not known yet gates nothing.
 */
export function canDeploy(token: Pick<TokenIdentity, 'role' | 'applications'> | null, application: string): boolean {
  if (!token) return true
  if (token.role === 'admin') return true
  if (token.role !== 'deploy') return false
  const limits = tokenLimits(token)
  return limits.length === 0 || limits.includes(application)
}

/** "my-api", "my-api and web", "my-api, web and 3 more". */
export function listNames(names: readonly string[], show = 2): string {
  if (names.length === 0) return ''
  if (names.length === 1) return names[0]!
  if (names.length <= show) return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
  return `${names.slice(0, show).join(', ')} and ${names.length - show} more`
}

/** What the signed-in token allows, in a few words, for the sidebar. */
export function accessSummary(token: Pick<TokenIdentity, 'role' | 'applications'>): string {
  if (token.role === 'read') return 'can look, not change'
  if (token.role === 'admin') return 'can do everything'
  const limits = tokenLimits(token)
  return limits.length === 0 ? 'can deploy, stop and start' : `can deploy ${listNames(limits)}; looks at the rest`
}

/**
 * Why nothing on an application's page can be changed, said once in words:
 * a disabled button's tooltip is out of reach on a phone. "" when the token
 * may deploy it.
 */
export function readOnlyReason(token: Pick<TokenIdentity, 'role' | 'applications'> | null, application: string): string {
  if (!token || canDeploy(token, application)) return ''
  if (token.role === 'deploy') {
    return `You may change only ${listNames(tokenLimits(token), 4)}: you can see ${application} and change nothing here. An admin can widen that under Access.`
  }
  return 'You are signed in with a read token: you can see everything here and change nothing. Deploying, stopping and starting need the deploy or admin role.'
}

/** The tooltip of an action the token may not take on `application`; undefined when it may. */
export function deployHint(token: Pick<TokenIdentity, 'role' | 'applications'> | null, application: string): string | undefined {
  if (!token || canDeploy(token, application)) return undefined
  if (token.role === 'deploy') return `You may change only ${listNames(tokenLimits(token), 4)}`
  return 'Deploying needs the deploy or admin role'
}

/** The agent's sentence for a 403 TOKEN_LIMITED, capitalized; "" for any other error. */
export function limitedExplanation(error: Pick<AgentError, 'code' | 'message'>): string {
  if (error.code !== 'TOKEN_LIMITED') return ''
  return error.message.charAt(0).toUpperCase() + error.message.slice(1)
}

/** "9 days", "5 hours", "3 months": a span until something, rounded down to its largest unit. */
export function spanInWords(ms: number): string {
  const unit = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 1) return 'less than a minute'
  if (minutes < 60) return unit(minutes, 'minute')
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return unit(hours, 'hour')
  const days = Math.floor(hours / 24)
  if (days < 60) return unit(days, 'day')
  if (days < 365) return unit(Math.floor(days / 30), 'month')
  return unit(Math.floor(days / 365), 'year')
}

export interface ExpiryDisplay {
  tone: Tone
  /** "in 9 days", "expired 2d ago". */
  label: string
  expired: boolean
  /** Within the warning period, or past. */
  soon: boolean
}

/** How a token's expiry reads; null for a token that does not expire. */
export function tokenExpiryDisplay(expiresAt: string | null | undefined, now: number = Date.now()): ExpiryDisplay | null {
  if (!expiresAt) return null
  const at = Date.parse(expiresAt)
  if (Number.isNaN(at)) return null
  if (at <= now) return { tone: 'danger', label: `expired ${formatRelativeTime(expiresAt, now)}`, expired: true, soon: true }
  const soon = at - now <= EXPIRY_WARNING_MS
  return { tone: soon ? 'warn' : 'muted', label: `in ${spanInWords(at - now)}`, expired: false, soon }
}

/**
 * "This token expires in 9 days": the warning for the session's own token; ""
 * while there is nothing to warn about. A person's session always ends within
 * hours, so it is never warned about: its end is said plainly instead.
 */
export function sessionExpiryWarning(token: Pick<TokenIdentity, 'expires_at' | 'kind'> | null, now: number = Date.now()): string {
  if (token?.kind === 'user') return ''
  const display = tokenExpiryDisplay(token?.expires_at, now)
  if (!display || !display.soon || display.expired) return ''
  return `This token expires ${display.label}`
}

/** "signed in until 19:00", in the browser's time: when a person's session ends; "" for a token. */
export function signedInUntil(token: Pick<TokenIdentity, 'expires_at' | 'kind'> | null, format: (date: Date) => string = clockTime): string {
  if (token?.kind !== 'user' || !token.expires_at) return ''
  const end = Date.parse(token.expires_at)
  return Number.isNaN(end) ? '' : `signed in until ${format(new Date(end))}`
}

function clockTime(date: Date): string {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

/**
 * Why a session is over, for the sign-in page: the agent answered 401 with a
 * code that says more than "wrong token". "" for a plain 401.
 */
export function sessionEndedMessage(error: Pick<AgentError, 'code' | 'message' | 'details'> | null | undefined): string {
  if (!error) return ''
  const said = sentence(error.message)
  if (error.code === 'TOKEN_EXPIRED') return said
  if (error.code === 'SESSION_EXPIRED') return 'Your session ended after ten hours. Sign in again.'
  if (error.code === 'SESSION_ENDED') {
    if (error.details.reason === 'access_changed') return 'What you may do on this server was changed. Sign in again to continue.'
    if (error.details.reason === 'signed_out') return 'An admin ended your session. Sign in again.'
    return said
  }
  return ''
}

function sentence(text: string): string {
  const trimmed = text.trim()
  if (trimmed === '') return ''
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

// --- who may sign in ------------------------------------------------------------

/** A rule's subject as people write it: "ada@example.com", "group:developers", "*@example.com". */
export function ruleSubject(rule: Pick<AccessRule, 'kind' | 'subject'>): string {
  if (rule.kind === 'group') return `group:${rule.subject}`
  if (rule.kind === 'domain') return `*@${rule.subject}`
  return rule.subject
}

/** Why a subject cannot be granted a role, or "" when it can; the agent checks again. */
export function ruleSubjectProblem(kind: string, subject: string): string {
  const value = subject.trim()
  if (value === '') return ''
  if (kind === 'email') return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value) ? '' : 'Not an e-mail address; use one like ada@example.com.'
  if (kind === 'domain') return /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(value) ? '' : 'Use the part after the @, e.g. example.com.'
  return value.length > 256 ? 'At most 256 characters.' : ''
}

/** The host of the provider's issuer, for the sign-in button: "accounts.example.com"; the text itself when it is not a URL. */
export function issuerHost(issuer: string): string {
  try {
    return new URL(issuer).host
  }
  catch {
    return issuer
  }
}

export type ExpiryChoice = 'never' | '30' | '90' | '365' | 'date'

export const EXPIRY_CHOICES: readonly { value: ExpiryChoice, label: string }[] = [
  { value: 'never', label: 'Never' },
  { value: '30', label: 'In 30 days' },
  { value: '90', label: 'In 90 days' },
  { value: '365', label: 'In a year' },
  { value: 'date', label: 'On a date…' },
]

/**
 * `expires_at` for the token form's choice, RFC 3339: a number of days from
 * now, or the end of a chosen day in UTC, as `shipwick token create --expires
 * 2027-01-31` reads a date. `value` is undefined for a token that never
 * expires; `problem` says why a date cannot be used.
 */
export function expiryFromChoice(choice: ExpiryChoice, date: string, now: number = Date.now()): { value?: string, problem: string } {
  if (choice === 'never') return { problem: '' }
  if (choice !== 'date') return { value: rfc3339(now + Number(choice) * DAY_MS), problem: '' }
  if (date === '') return { problem: 'Choose the day the token stops working.' }
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || Number.isNaN(Date.parse(`${date}T23:59:59Z`))) return { problem: 'Not a date: use the form 2027-01-31.' }
  const end = Date.parse(`${date}T23:59:59Z`)
  // A day the calendar does not have (February 31st) is read as one that follows it: not what was typed.
  if (new Date(end).toISOString().slice(0, 10) !== date) return { problem: 'Not a date: use the form 2027-01-31.' }
  if (end <= now) return { problem: 'That day is over: a token that has expired already would be of no use.' }
  return { value: rfc3339(end), problem: '' }
}

function rfc3339(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/**
 * The applications typed into the token form, one name per comma or space:
 * each once, sorted as the agent answers them. `problem` names the first that
 * cannot be an application's name.
 */
export function parseApplications(text: string): { names: string[], problem: string } {
  const names = [...new Set(text.split(/[\s,]+/).map(n => n.trim().toLowerCase()).filter(n => n !== ''))].sort()
  const bad = names.find(n => !APPLICATION_NAME.test(n))
  if (bad) return { names, problem: `"${bad}" cannot be an application's name: lowercase letters, digits and dashes.` }
  if (names.length > MAX_TOKEN_APPLICATIONS) return { names, problem: `A token can be limited to at most ${MAX_TOKEN_APPLICATIONS} applications.` }
  return { names, problem: '' }
}

// --- the audit trail ------------------------------------------------------------

const ACTION_LABEL: Record<string, string> = {
  'deploy': 'Deploy',
  'redeploy': 'Redeploy',
  'rollback': 'Roll back',
  'stop': 'Stop',
  'start': 'Start',
  'application.delete': 'Delete application',
  'run': 'Run command',
  'job.run': 'Run job',
  'image.upload': 'Upload image',
  'static.upload': 'Upload folder',
  'backup.create': 'Back up',
  'backup.verify': 'Verify backup',
  'backup.restore': 'Restore backup',
  'backup.download': 'Download backup',
  'backup.delete': 'Remove backup',
  'backup.adopt': 'Adopt backups',
  'server.backup': 'Back up agent state',
  'volume.download': 'Download volume',
  'volume.restore': 'Restore volume',
  'volume.delete': 'Remove volume',
  'secret.set': 'Store secret',
  'secret.delete': 'Remove secret',
  'registry.login': 'Registry login',
  'registry.logout': 'Registry logout',
  'certificate.set': 'Store certificate',
  'certificate.delete': 'Remove certificate',
  'token.create': 'Create token',
  'token.revoke': 'Revoke token',
  'key.rotate': 'Rotate encryption key',
  'export.download': 'Download export',
  'export.create': 'Export to backups',
  'import': 'Import',
  'standby.pull': 'Fetch export',
  'standby.promote': 'Promote standby',
  'signin': 'Sign in',
  'signout': 'Sign out',
  'access.grant': 'Grant access',
  'access.revoke': 'Revoke access',
  'access.signout': 'Sign a person out',
}

/** "Roll back", "Create token"; an action this dashboard does not know reads as the agent names it. */
export function auditActionLabel(action: string): string {
  return ACTION_LABEL[action] ?? action
}

/** What the action was on: "my-api", "my-api · nightly-report", "ci"; "the server" for what names neither. */
export function auditSubject(entry: Pick<AuditEntry, 'application' | 'target'>): string {
  if (entry.application && entry.target) return `${entry.application} · ${entry.target}`
  return entry.application || entry.target || 'the server'
}

/** How it was answered, with the error code of a refusal or failure. */
export function auditOutcomeDisplay(entry: Pick<AuditEntry, 'outcome' | 'code'>): StatusDisplay {
  if (entry.outcome === 'ok') return { tone: 'ok', label: 'Done' }
  if (entry.outcome === 'refused') return { tone: 'warn', label: 'Refused' }
  if (entry.outcome === 'failed') return { tone: 'danger', label: 'Failed' }
  return { tone: 'muted', label: entry.outcome }
}

/** The address the request came from: what the proxy in front reported, else what the agent saw. */
export function auditFrom(entry: Pick<AuditEntry, 'address' | 'forwarded_for'>): string {
  return entry.forwarded_for || entry.address
}

/** The page a detail such as "deployment 12" leads to; null for one that names nothing with a page. */
export function auditDetailLink(detail: string): string | null {
  const deployment = /^deployment (\d+)$/.exec(detail)
  return deployment ? `/deployments/${deployment[1]}` : null
}

/** Whether a page of the trail was full, so that older entries may exist. */
export function auditHasOlder(page: readonly unknown[], limit: number): boolean {
  return page.length >= limit
}
