import type { AccessRule, AuditEntry, AuditPage, Token, TokenIdentity, UpdateTokenRequest } from '~/types/api'
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
    if (error.details.reason === 'sign_in_changed') return 'How people sign in to this server was changed. Sign in again.'
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

/** A rule's subject as people write it: "ada@example.com", "group:developers", "*@example.com", "name:svc-deploy". */
export function ruleSubject(rule: Pick<AccessRule, 'kind' | 'subject'>): string {
  if (rule.kind === 'group') return `group:${rule.subject}`
  if (rule.kind === 'domain') return `*@${rule.subject}`
  if (rule.kind === 'name') return `name:${rule.subject}`
  return rule.subject
}

/** Why a subject cannot be granted a role, or "" when it can; the agent checks again. */
export function ruleSubjectProblem(kind: string, subject: string): string {
  const value = subject.trim()
  if (value === '') return ''
  if (kind === 'email') return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value) ? '' : 'Not an e-mail address; use one like ada@example.com.'
  if (kind === 'domain') return /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(value) ? '' : 'Use the part after the @, e.g. example.com.'
  if (kind === 'name') return /^[A-Za-z0-9._%+'@|:=#~-]{1,254}$/.test(value) ? '' : 'A name as the provider issues it: letters, digits and . _ % + \' @ | : = # ~ - without spaces, at most 254.'
  return value.length > 256 ? 'At most 256 characters.' : ''
}

/**
 * A subject as it is sent: an address and a domain in lowercase, without the
 * `*@` a domain is often typed with; a group and a name exactly as typed, for
 * the provider's own spelling is what is compared.
 */
export function cleanRuleSubject(kind: string, raw: string): string {
  const value = raw.trim()
  return kind === 'group' || kind === 'name' ? value : value.toLowerCase().replace(/^\*?@/, '')
}

/**
 * The claim the agent names people by, when it is not their address: "" for
 * `email`, for an agent that does not say (before 0.7), and without sign-in.
 */
export function otherNameClaim(signIn: { configured?: boolean, name_claim?: string } | null | undefined): string {
  const claim = signIn?.name_claim ?? ''
  return claim === '' || claim === 'email' ? '' : claim
}

export interface RuleKindChoice {
  value: 'email' | 'group' | 'domain' | 'name'
  /** In the select: "One person", "A group", … */
  label: string
  /** The field's label. */
  field: string
  placeholder: string
  help: string
}

/**
 * The kinds of rule the form offers, the one for a single person first. Where
 * people are named by another claim than their address that is a rule by
 * name, and rules by address and by domain say whom they reach; an agent
 * before 0.7 knows no rule by name.
 */
export function ruleKinds(claim: string, supportsNames: boolean): RuleKindChoice[] {
  const group: RuleKindChoice = { value: 'group', label: 'A group', field: 'Group', placeholder: 'developers', help: 'A group of the provider\'s, written exactly as the provider sends it.' }
  if (claim !== '') {
    const reach = `This server names people by the ${claim} claim: the rule applies to those whose ${claim} is an address.`
    return [
      { value: 'name', label: 'One person', field: 'Name', placeholder: 'ada', help: `The person's ${claim}, exactly as the provider issues it. Upper and lower case count.` },
      group,
      { value: 'email', label: 'One person, by address', field: 'Address', placeholder: 'ada@example.com', help: reach },
      { value: 'domain', label: 'Everyone at a domain', field: 'Domain', placeholder: 'example.com', help: `${reach} The domain is the part after the @.` },
    ]
  }
  return [
    { value: 'email', label: 'One person', field: 'Address', placeholder: 'ada@example.com', help: 'The address the provider names for the account.' },
    group,
    { value: 'domain', label: 'Everyone at a domain', field: 'Domain', placeholder: 'example.com', help: 'Every address at the domain: the part after the @.' },
    ...(supportsNames
      ? [{ value: 'name' as const, label: 'One person, by name', field: 'Name', placeholder: 'ada@example.com', help: 'This server names people by their e-mail address: a rule by name matches an address written exactly as the agent keeps it, in lowercase. A rule for the address itself says the same more plainly.' }]
      : []),
  ]
}

/** Under a rule in the list: whom a rule by address or domain reaches where people are named by another claim; "" otherwise. */
export function ruleReach(rule: Pick<AccessRule, 'kind'>, claim: string): string {
  return claim !== '' && (rule.kind === 'email' || rule.kind === 'domain') ? 'applies to names that are addresses' : ''
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

/** What the form that changes a token does with its end: leave it, take it away, or move it. */
export type EditExpiryChoice = 'keep' | ExpiryChoice

/** The choices for a token's end when it is changed; the first leaves it as it is, in words that say what that is. */
export function editExpiryChoices(token: Pick<Token, 'expires_at'>, now: number = Date.now()): { value: EditExpiryChoice, label: string }[] {
  const display = tokenExpiryDisplay(token.expires_at, now)
  const keep = !display ? 'Leave it: no end' : display.expired ? `Leave it: ${display.label}` : `Leave it: ends ${display.label}`
  return [
    { value: 'keep', label: keep },
    // Nothing to take away from a token that has no end.
    ...(token.expires_at ? [{ value: 'never' as const, label: 'No end' }] : []),
    { value: '30', label: 'In 30 days' },
    { value: '90', label: 'In 90 days' },
    { value: '365', label: 'In a year' },
    { value: 'date', label: 'On a date…' },
  ]
}

export interface TokenEdit {
  /** The chosen applications; [] for all. Only a deploy token's are looked at. */
  applications: readonly string[]
  expiry: EditExpiryChoice
  /** For `expiry: 'date'`. */
  date: string
}

/**
 * The body of PUT /tokens/:name for what the form changed, and only that: the
 * agent replaces what it is sent. `body` is null when nothing differs from
 * the token as it is; `problem` says why a date cannot be used.
 */
export function tokenChanges(token: Pick<Token, 'role' | 'applications' | 'expires_at'>, edit: TokenEdit, now: number = Date.now()): { body: UpdateTokenRequest | null, problem: string } {
  const body: UpdateTokenRequest = {}
  if (token.role === 'deploy') {
    const before = [...(token.applications ?? [])].sort()
    const after = [...new Set(edit.applications)].sort()
    if (before.join('\n') !== after.join('\n')) body.applications = after
  }
  if (edit.expiry === 'never') {
    if (token.expires_at) body.never_expires = true
  }
  else if (edit.expiry !== 'keep') {
    const end = expiryFromChoice(edit.expiry, edit.date, now)
    if (end.problem) return { body: null, problem: end.problem }
    if (end.value) body.expires_at = end.value
  }
  return { body: Object.keys(body).length > 0 ? body : null, problem: '' }
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
  'token.update': 'Change token',
  'token.revoke': 'Revoke token',
  'audit.export': 'Export audit trail',
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

/**
 * Whether older entries match: what the agent says (`more`, from 0.7 on), and
 * for an agent that does not say, whether the page came back full.
 */
export function auditMore(page: AuditPage, limit: number): boolean {
  return page.more ?? auditHasOlder(page.data, limit)
}

/** Groups of actions to look for at once. A value ending in a dot is the agent's own word for a family: `token.` is every action on tokens. */
export const AUDIT_FAMILIES: readonly { value: string, label: string, actions: string }[] = [
  { value: '', label: 'Everything', actions: '' },
  { value: 'deployments', label: 'Deployments', actions: 'deploy,redeploy,rollback,image.,static.' },
  { value: 'running', label: 'Stop, start and delete', actions: 'stop,start,application.' },
  { value: 'runs', label: 'Jobs and commands', actions: 'run,job.' },
  { value: 'backups', label: 'Backups and volumes', actions: 'backup.,volume.,server.' },
  { value: 'values', label: 'Secrets, registries and certificates', actions: 'secret.,registry.,certificate.,key.' },
  { value: 'tokens', label: 'Tokens', actions: 'token.' },
  { value: 'access', label: 'Sign-ins and access', actions: 'signin,signout,access.' },
  { value: 'transfer', label: 'Exports, imports and the standby', actions: 'export.,import,standby.,audit.' },
]

const AUDIT_ACTION = /^[a-z]{1,20}(\.[a-z]{1,20})?$|^[a-z]{1,20}\.$/

/** Why a typed action cannot be searched for; "" when it can, and for an empty field. */
export function auditActionProblem(text: string): string {
  const value = text.trim()
  if (value === '' || AUDIT_ACTION.test(value)) return ''
  return 'An action as the trail names it, or the start of a family with its dot: deploy, token.create, backup.'
}

export interface AuditFilters {
  application: string
  /** A token's name or a person's, matched exactly. */
  actor: string
  /** '' for both, `token`, or `user` for people. */
  actorKind: string
  /** One of AUDIT_FAMILIES' values. */
  family: string
  /** One action or family typed by hand; it replaces the chosen family. */
  action: string
  /** Of ok, refused, failed; none chosen means all. */
  outcomes: readonly string[]
  since: string
}

export const NO_AUDIT_FILTERS: AuditFilters = { application: '', actor: '', actorKind: '', family: '', action: '', outcomes: [], since: '' }

/** The query of GET /audit and of its export for a set of filters: only what is set. */
export function auditQuery(filters: AuditFilters): Record<string, string> {
  const typed = filters.action.trim()
  const action = typed !== '' ? typed : AUDIT_FAMILIES.find(f => f.value === filters.family)?.actions ?? ''
  // All three chosen is the same as none.
  const outcomes = filters.outcomes.length === 3 ? [] : filters.outcomes
  const query: Record<string, string> = {}
  if (filters.application) query.application = filters.application
  if (filters.actor.trim()) query.actor = filters.actor.trim()
  if (filters.actorKind) query.actor_kind = filters.actorKind
  if (action) query.action = action
  if (outcomes.length > 0) query.outcome = outcomes.join(',')
  if (filters.since) query.since = filters.since
  return query
}

/** The filters an agent before 0.7 does not know and ignores, among those that are set: in words, for the notice that says so. */
export function newAuditFilters(query: Record<string, string>): string[] {
  return [query.action ? 'the action' : '', query.outcome ? 'the result' : '', query.actor_kind ? 'tokens or people' : ''].filter(word => word !== '')
}

/**
 * An answer without `more` to a question that used a filter 0.7 added came
 * from an older agent, which ignored the filter: the list is not what was
 * asked for, and the page says so instead of showing it.
 */
export function auditFiltersIgnored(page: AuditPage, query: Record<string, string>): boolean {
  return page.more === undefined && newAuditFilters(query).length > 0
}
