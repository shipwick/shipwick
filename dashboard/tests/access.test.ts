import { describe, expect, it } from 'vitest'
import type { Token, TokenIdentity } from '../app/types/api'
import { AgentError } from '../app/utils/agentError'
import {
  AUDIT_FAMILIES,
  NO_AUDIT_FILTERS,
  accessSummary,
  auditActionLabel,
  auditActionProblem,
  auditDetailLink,
  auditFiltersIgnored,
  auditFrom,
  auditHasOlder,
  auditMore,
  auditOutcomeDisplay,
  auditQuery,
  auditSubject,
  canDeploy,
  cleanRuleSubject,
  deployHint,
  editExpiryChoices,
  expiryFromChoice,
  issuerHost,
  limitedExplanation,
  listNames,
  newAuditFilters,
  otherNameClaim,
  parseApplications,
  readOnlyReason,
  ruleKinds,
  ruleReach,
  ruleSubject,
  ruleSubjectProblem,
  sessionEndedMessage,
  sessionExpiryWarning,
  signedInUntil,
  spanInWords,
  tokenChanges,
  tokenExpiryDisplay,
} from '../app/utils/access'
import { accessTabs, activeTab } from '../app/utils/tabs'

const DAY = 24 * 60 * 60 * 1000
const now = Date.parse('2026-10-03T12:00:00Z')

const token = (over: Partial<TokenIdentity>): TokenIdentity => ({ name: 'ci', role: 'deploy', kind: 'token', applications: [], expires_at: null, ...over })

describe('canDeploy', () => {
  it('lets an admin deploy anything and a read token nothing', () => {
    expect(canDeploy(token({ role: 'admin' }), 'worker')).toBe(true)
    expect(canDeploy(token({ role: 'read' }), 'worker')).toBe(false)
  })

  it('lets a deploy token that is not limited deploy anything', () => {
    expect(canDeploy(token({}), 'worker')).toBe(true)
    // An agent before 0.6 sends a name and a role only.
    expect(canDeploy({ role: 'deploy' }, 'worker')).toBe(true)
  })

  it('lets a limited token deploy what is on its list, an application that does not exist yet included, and nothing else', () => {
    const limited = token({ applications: ['my-api', 'brand-new'] })
    expect(canDeploy(limited, 'my-api')).toBe(true)
    expect(canDeploy(limited, 'brand-new')).toBe(true)
    expect(canDeploy(limited, 'worker')).toBe(false)
  })

  it('gates nothing before the token is known', () => {
    expect(canDeploy(null, 'worker')).toBe(true)
  })
})

describe('what the token allows, in words', () => {
  it('sums the token up for the sidebar', () => {
    expect(accessSummary(token({ role: 'read' }))).toBe('can look, not change')
    expect(accessSummary(token({ role: 'admin' }))).toBe('can do everything')
    expect(accessSummary(token({}))).toBe('can deploy, stop and start')
    expect(accessSummary(token({ applications: ['my-api', 'web'] }))).toBe('can deploy my-api and web; looks at the rest')
  })

  it('explains per application why nothing can be changed, and says nothing when it can', () => {
    expect(readOnlyReason(token({ applications: ['my-api'] }), 'my-api')).toBe('')
    expect(readOnlyReason(token({ applications: ['my-api'] }), 'worker')).toContain('You may change only my-api: you can see worker and change nothing here')
    expect(readOnlyReason(token({ role: 'read' }), 'worker')).toContain('read token')
    expect(deployHint(token({ applications: ['my-api', 'web'] }), 'worker')).toBe('You may change only my-api and web')
    expect(deployHint(token({ role: 'read' }), 'worker')).toBe('Deploying needs the deploy or admin role')
    expect(deployHint(token({}), 'worker')).toBeUndefined()
  })

  it('lists names, and counts the rest of a long list', () => {
    expect(listNames([])).toBe('')
    expect(listNames(['a'])).toBe('a')
    expect(listNames(['a', 'b'])).toBe('a and b')
    expect(listNames(['a', 'b', 'c', 'd'])).toBe('a, b and 2 more')
    expect(listNames(['a', 'b', 'c'], 3)).toBe('a, b and c')
  })

  it('shows the agent\'s own sentence for a refusal of a limited token', () => {
    const error = new AgentError(403, 'TOKEN_LIMITED', 'this token is limited to my-api and web; it can read worker and not change it', { applications: ['my-api', 'web'], application: 'worker' })
    expect(limitedExplanation(error)).toBe('This token is limited to my-api and web; it can read worker and not change it')
    expect(limitedExplanation(new AgentError(403, 'FORBIDDEN', 'x'))).toBe('')
  })
})

describe('a token that expires', () => {
  it('reads "never" as nothing to show', () => {
    expect(tokenExpiryDisplay(null, now)).toBeNull()
    expect(tokenExpiryDisplay(undefined, now)).toBeNull()
  })

  it('is plain while far off, a warning within 14 days, and an error once past', () => {
    expect(tokenExpiryDisplay(new Date(now + 90 * DAY).toISOString(), now)).toEqual({ tone: 'muted', label: 'in 3 months', expired: false, soon: false })
    expect(tokenExpiryDisplay(new Date(now + 9 * DAY).toISOString(), now)).toEqual({ tone: 'warn', label: 'in 9 days', expired: false, soon: true })
    expect(tokenExpiryDisplay(new Date(now + 14 * DAY).toISOString(), now)?.soon).toBe(true)
    expect(tokenExpiryDisplay(new Date(now + 15 * DAY).toISOString(), now)?.soon).toBe(false)
    expect(tokenExpiryDisplay(new Date(now - 2 * DAY).toISOString(), now)).toEqual({ tone: 'danger', label: 'expired 2d ago', expired: true, soon: true })
  })

  it('words a span by its largest unit', () => {
    expect(spanInWords(30_000)).toBe('less than a minute')
    expect(spanInWords(5 * 60_000)).toBe('5 minutes')
    expect(spanInWords(60 * 60_000)).toBe('1 hour')
    expect(spanInWords(3 * DAY)).toBe('3 days')
    expect(spanInWords(400 * DAY)).toBe('1 year')
  })

  it('warns about the session\'s own token within 14 days only, and never about a person\'s session', () => {
    expect(sessionExpiryWarning(token({ expires_at: new Date(now + 9 * DAY).toISOString() }), now)).toBe('This token expires in 9 days')
    expect(sessionExpiryWarning(token({ expires_at: new Date(now + 60 * DAY).toISOString() }), now)).toBe('')
    expect(sessionExpiryWarning(token({}), now)).toBe('')
    expect(sessionExpiryWarning(token({ kind: 'user', expires_at: new Date(now + 3_600_000).toISOString() }), now)).toBe('')
  })

  it('says until when a person is signed in, and nothing for a token', () => {
    const at = new Date(now + 3_600_000).toISOString()
    expect(signedInUntil(token({ kind: 'user', expires_at: at }), d => d.toISOString().slice(11, 16))).toBe('signed in until 13:00')
    expect(signedInUntil(token({ expires_at: at }))).toBe('')
  })
})

describe('the token form', () => {
  it('sends no expiry for "never", and a time that many days ahead for a number of days', () => {
    expect(expiryFromChoice('never', '', now)).toEqual({ problem: '' })
    expect(expiryFromChoice('30', '', now)).toEqual({ value: '2026-11-02T12:00:00Z', problem: '' })
    expect(expiryFromChoice('365', '', now).value).toBe('2027-10-03T12:00:00Z')
  })

  it('reads a date as the end of that day in UTC, as the CLI does', () => {
    expect(expiryFromChoice('date', '2027-01-31', now)).toEqual({ value: '2027-01-31T23:59:59Z', problem: '' })
    // Today still counts: the day is not over.
    expect(expiryFromChoice('date', '2026-10-03', now).value).toBe('2026-10-03T23:59:59Z')
  })

  it('refuses a day that is over, a missing day and something that is not a date', () => {
    expect(expiryFromChoice('date', '2026-10-02', now).problem).toContain('That day is over')
    expect(expiryFromChoice('date', '', now).problem).not.toBe('')
    expect(expiryFromChoice('date', '31.01.2027', now).problem).toContain('Not a date')
    expect(expiryFromChoice('date', '2027-02-31', now).value).toBeUndefined()
  })

  it('takes application names separated by commas or spaces, each once, sorted', () => {
    expect(parseApplications('web, my-api  web\nbrand-new')).toEqual({ names: ['brand-new', 'my-api', 'web'], problem: '' })
    expect(parseApplications('')).toEqual({ names: [], problem: '' })
  })

  it('names the first thing that cannot be an application\'s name, and refuses more than 50', () => {
    expect(parseApplications('web, My_Api').problem).toContain('"my_api"')
    expect(parseApplications(Array.from({ length: 51 }, (_, i) => `app-${i}`).join(' ')).problem).toContain('at most 50')
  })
})

describe('why a session is over', () => {
  it('passes on the agent\'s sentence for a token that expired', () => {
    const error = new AgentError(401, 'TOKEN_EXPIRED', 'token ci expired on 2026-10-02 at 12:00 UTC; an admin creates a new one with: shipwick token create', { name: 'ci' })
    expect(sessionEndedMessage(error)).toBe('Token ci expired on 2026-10-02 at 12:00 UTC; an admin creates a new one with: shipwick token create.')
  })

  it('has its own sentence for a session that ran out, was changed or was ended by an admin', () => {
    expect(sessionEndedMessage(new AgentError(401, 'SESSION_EXPIRED', 'the session of ada@example.com expired on …'))).toBe('Your session ended after ten hours. Sign in again.')
    expect(sessionEndedMessage(new AgentError(401, 'SESSION_ENDED', 'what ada@example.com may do on this server was changed. Sign in again', { reason: 'access_changed' }))).toBe('What you may do on this server was changed. Sign in again to continue.')
    expect(sessionEndedMessage(new AgentError(401, 'SESSION_ENDED', 'an admin ended this session. Sign in again', { reason: 'signed_out' }))).toBe('An admin ended your session. Sign in again.')
  })

  it('shows the agent\'s sentence when the rule is gone: it names the address and what an admin runs', () => {
    const message = 'no rule on this server gives ada@example.com a role any more. An admin grants one with: shipwick access grant ada@example.com --role read'
    expect(sessionEndedMessage(new AgentError(401, 'SESSION_ENDED', message, { reason: 'rule_removed' }))).toBe(`No rule on this server gives ada@example.com a role any more. An admin grants one with: shipwick access grant ada@example.com --role read.`)
  })

  it('says nothing special for a plain 401', () => {
    expect(sessionEndedMessage(new AgentError(401, 'UNAUTHORIZED', 'missing or invalid API token'))).toBe('')
    expect(sessionEndedMessage(undefined)).toBe('')
  })
})

describe('who may sign in', () => {
  it('writes a rule\'s subject as people do', () => {
    expect(ruleSubject({ kind: 'email', subject: 'ada@example.com' })).toBe('ada@example.com')
    expect(ruleSubject({ kind: 'group', subject: 'developers' })).toBe('group:developers')
    expect(ruleSubject({ kind: 'domain', subject: 'example.com' })).toBe('*@example.com')
  })

  it('checks an address and a domain before the agent does, and lets any group name through', () => {
    expect(ruleSubjectProblem('email', 'ada@example.com')).toBe('')
    expect(ruleSubjectProblem('email', 'ada')).toContain('Not an e-mail address')
    expect(ruleSubjectProblem('domain', 'example.com')).toBe('')
    expect(ruleSubjectProblem('domain', '*.example.com')).toContain('after the @')
    expect(ruleSubjectProblem('group', 'Team / Developers')).toBe('')
    expect(ruleSubjectProblem('group', '')).toBe('')
  })

  it('names the provider by its host', () => {
    expect(issuerHost('https://accounts.example.com/realms/company')).toBe('accounts.example.com')
    expect(issuerHost('not a url')).toBe('not a url')
  })
})

describe('the audit trail', () => {
  it('words the actions, and leaves one it does not know as the agent names it', () => {
    expect(auditActionLabel('rollback')).toBe('Roll back')
    expect(auditActionLabel('token.create')).toBe('Create token')
    expect(auditActionLabel('signin')).toBe('Sign in')
    expect(auditActionLabel('something.new')).toBe('something.new')
  })

  it('says what an entry was about', () => {
    expect(auditSubject({ application: 'my-api', target: '' })).toBe('my-api')
    expect(auditSubject({ application: 'my-api', target: 'nightly-report' })).toBe('my-api · nightly-report')
    expect(auditSubject({ application: '', target: 'ci' })).toBe('ci')
    expect(auditSubject({ application: '', target: '' })).toBe('the server')
  })

  it('tells done, refused and failed apart', () => {
    expect(auditOutcomeDisplay({ outcome: 'ok', code: '' })).toEqual({ tone: 'ok', label: 'Done' })
    expect(auditOutcomeDisplay({ outcome: 'refused', code: 'TOKEN_LIMITED' }).tone).toBe('warn')
    expect(auditOutcomeDisplay({ outcome: 'failed', code: 'NOT_DEPLOYED' }).tone).toBe('danger')
  })

  it('prefers the address the proxy reported to the one the agent saw', () => {
    expect(auditFrom({ address: '172.18.0.3', forwarded_for: '203.0.113.40' })).toBe('203.0.113.40')
    expect(auditFrom({ address: '172.18.0.3', forwarded_for: '' })).toBe('172.18.0.3')
  })

  it('links a deployment named in the detail and nothing else', () => {
    expect(auditDetailLink('deployment 12')).toBe('/deployments/12')
    expect(auditDetailLink('run 3')).toBeNull()
    expect(auditDetailLink('role deploy, limited to a b')).toBeNull()
  })

  it('offers older entries only while a page came back full', () => {
    expect(auditHasOlder(Array.from({ length: 50 }), 50)).toBe(true)
    expect(auditHasOlder(Array.from({ length: 49 }), 50)).toBe(false)
  })
})

describe('the audit trail, searched', () => {
  it('asks only for what is set', () => {
    expect(auditQuery(NO_AUDIT_FILTERS)).toEqual({})
    expect(auditQuery({ ...NO_AUDIT_FILTERS, application: 'web', actor: ' ci ', actorKind: 'token', since: '7d' })).toEqual({ application: 'web', actor: 'ci', actor_kind: 'token', since: '7d' })
  })

  it('turns a family into the agent\'s actions, and lets a typed action replace it', () => {
    expect(auditQuery({ ...NO_AUDIT_FILTERS, family: 'tokens' })).toEqual({ action: 'token.' })
    expect(auditQuery({ ...NO_AUDIT_FILTERS, family: 'deployments' }).action).toBe('deploy,redeploy,rollback,image.,static.')
    expect(auditQuery({ ...NO_AUDIT_FILTERS, family: 'tokens', action: ' secret.set ' })).toEqual({ action: 'secret.set' })
  })

  it('names only actions and families the agent accepts, and at most twenty at once', () => {
    const pattern = /^[a-z]{1,20}(\.[a-z]{1,20})?$|^[a-z]{1,20}\.$/
    for (const family of AUDIT_FAMILIES) {
      const actions = family.actions === '' ? [] : family.actions.split(',')
      expect(actions.length, family.value).toBeLessThanOrEqual(20)
      for (const action of actions) expect(pattern.test(action), `${family.value}: ${action}`).toBe(true)
    }
  })

  it('joins the outcomes, and asks for none when all three are ticked', () => {
    expect(auditQuery({ ...NO_AUDIT_FILTERS, outcomes: ['refused', 'failed'] })).toEqual({ outcome: 'refused,failed' })
    expect(auditQuery({ ...NO_AUDIT_FILTERS, outcomes: ['ok', 'refused', 'failed'] })).toEqual({})
  })

  it('holds a typed action to the agent\'s pattern', () => {
    for (const good of ['', 'deploy', 'token.create', 'backup.', ' token. ']) expect(auditActionProblem(good), good).toBe('')
    for (const bad of ['Token.create', 'token.create.x', '.create', 'a b', 'token*']) expect(auditActionProblem(bad), bad).not.toBe('')
  })

  it('believes the agent about older entries, and a full page where it does not say', () => {
    const page = (n: number) => Array.from({ length: n }, () => ({})) as never[]
    expect(auditMore({ data: page(50), more: false }, 50)).toBe(false)
    expect(auditMore({ data: page(3), more: true }, 50)).toBe(true)
    expect(auditMore({ data: page(50) }, 50)).toBe(true)
    expect(auditMore({ data: page(49) }, 50)).toBe(false)
  })

  it('notices an agent before 0.7, which ignores what it does not know', () => {
    expect(auditFiltersIgnored({ data: [] }, { action: 'token.' })).toBe(true)
    expect(auditFiltersIgnored({ data: [] }, { outcome: 'refused', actor_kind: 'user' })).toBe(true)
    // It knows these three.
    expect(auditFiltersIgnored({ data: [] }, { application: 'web', actor: 'ci', since: '7d' })).toBe(false)
    expect(auditFiltersIgnored({ data: [], more: false }, { action: 'token.' })).toBe(false)
    expect(newAuditFilters({ action: 'token.', outcome: 'ok', actor_kind: 'user', since: '7d' })).toEqual(['the action', 'the result', 'tokens or people'])
  })

  it('has words for what 0.7 records', () => {
    expect(auditActionLabel('token.update')).toBe('Change token')
    expect(auditActionLabel('audit.export')).toBe('Export audit trail')
  })
})

describe('a token that is changed', () => {
  const ci: Pick<Token, 'role' | 'applications' | 'expires_at'> = { role: 'deploy', applications: ['my-api'], expires_at: null }
  const keep = { applications: ['my-api'], expiry: 'keep' as const, date: '' }

  it('sends nothing when nothing was changed', () => {
    expect(tokenChanges(ci, keep, now)).toEqual({ body: null, problem: '' })
    expect(tokenChanges(ci, { ...keep, expiry: 'never' }, now).body).toBeNull()
  })

  it('sends only what was changed', () => {
    expect(tokenChanges(ci, { ...keep, applications: ['web', 'my-api'] }, now).body).toEqual({ applications: ['my-api', 'web'] })
    expect(tokenChanges(ci, { ...keep, expiry: '30' }, now).body).toEqual({ expires_at: '2026-11-02T12:00:00Z' })
  })

  it('lifts the limit with an empty list', () => {
    expect(tokenChanges(ci, { ...keep, applications: [] }, now).body).toEqual({ applications: [] })
  })

  it('takes the end away from a token that has one', () => {
    expect(tokenChanges({ ...ci, expires_at: '2026-10-12T12:00:00Z' }, { ...keep, expiry: 'never' }, now).body).toEqual({ never_expires: true })
  })

  it('gives an expired token a new end', () => {
    const expired = { ...ci, expires_at: '2026-09-18T12:00:00Z' }
    expect(tokenChanges(expired, { ...keep, expiry: 'date', date: '2027-01-31' }, now).body).toEqual({ expires_at: '2027-01-31T23:59:59Z' })
  })

  it('says why a date cannot be used, and sends nothing then', () => {
    expect(tokenChanges(ci, { ...keep, expiry: 'date', date: '2026-01-01' }, now)).toEqual({ body: null, problem: 'That day is over: a token that has expired already would be of no use.' })
    expect(tokenChanges(ci, { ...keep, expiry: 'date', date: '' }, now).problem).toBe('Choose the day the token stops working.')
  })

  it('never sends applications for a token that cannot be limited', () => {
    expect(tokenChanges({ role: 'read', applications: [], expires_at: null }, { applications: ['web'], expiry: 'keep', date: '' }, now).body).toBeNull()
    expect(tokenChanges({ role: 'admin', applications: [], expires_at: null }, { applications: ['web'], expiry: '90', date: '' }, now).body).toEqual({ expires_at: '2027-01-01T12:00:00Z' })
  })

  it('offers to leave the end as it is, in words that say what that is', () => {
    expect(editExpiryChoices({ expires_at: null }, now).map(c => c.label)).toEqual(['Leave it: no end', 'In 30 days', 'In 90 days', 'In a year', 'On a date…'])
    expect(editExpiryChoices({ expires_at: '2026-10-12T12:00:00Z' }, now).slice(0, 2).map(c => c.label)).toEqual(['Leave it: ends in 9 days', 'No end'])
    expect(editExpiryChoices({ expires_at: '2026-09-18T12:00:00Z' }, now)[0]!.label).toBe('Leave it: expired 15d ago')
  })
})

describe('people who are named by another claim than their address', () => {
  it('knows the claim only when it is not the address', () => {
    expect(otherNameClaim({ configured: true, name_claim: 'preferred_username' })).toBe('preferred_username')
    expect(otherNameClaim({ configured: true, name_claim: 'email' })).toBe('')
    expect(otherNameClaim({ configured: false, name_claim: '' })).toBe('')
    // An agent before 0.7 does not say: it names people by their address.
    expect(otherNameClaim({ configured: true })).toBe('')
    expect(otherNameClaim(null)).toBe('')
  })

  it('offers a rule by name first, and says whom a rule by address reaches', () => {
    const kinds = ruleKinds('preferred_username', true)
    expect(kinds.map(k => k.value)).toEqual(['name', 'group', 'email', 'domain'])
    expect(kinds[0]).toMatchObject({ label: 'One person', field: 'Name' })
    expect(kinds[2]!.help).toBe('This server names people by the preferred_username claim: the rule applies to those whose preferred_username is an address.')
  })

  it('offers a rule by address first where people are named by it, and a rule by name with what it then means', () => {
    const kinds = ruleKinds('', true)
    expect(kinds.map(k => k.value)).toEqual(['email', 'group', 'domain', 'name'])
    expect(kinds[3]!.help).toContain('a rule by name matches an address written exactly as the agent keeps it')
  })

  it('offers no rule by name to an agent that knows none', () => {
    expect(ruleKinds('', false).map(k => k.value)).toEqual(['email', 'group', 'domain'])
  })

  it('writes a rule by name as the CLI does', () => {
    expect(ruleSubject({ kind: 'name', subject: 'svc-deploy' })).toBe('name:svc-deploy')
  })

  it('keeps a name and a group as typed, and lowercases an address and a domain', () => {
    expect(cleanRuleSubject('name', ' Ada.Lovelace ')).toBe('Ada.Lovelace')
    expect(cleanRuleSubject('group', ' Developers ')).toBe('Developers')
    expect(cleanRuleSubject('email', ' Ada@Example.com ')).toBe('ada@example.com')
    expect(cleanRuleSubject('domain', '*@Example.com')).toBe('example.com')
  })

  it('holds a name to what a claim can hold', () => {
    expect(ruleSubjectProblem('name', 'svc-deploy')).toBe('')
    expect(ruleSubjectProblem('name', '248289761001')).toBe('')
    expect(ruleSubjectProblem('name', 'auth0|5f7c8ec7c33c6c004bbafe82')).toBe('')
    expect(ruleSubjectProblem('name', 'ada lovelace')).not.toBe('')
    expect(ruleSubjectProblem('name', 'x'.repeat(255))).not.toBe('')
  })

  it('says under a rule by address or domain whom it reaches', () => {
    expect(ruleReach({ kind: 'domain' }, 'sub')).toBe('applies to names that are addresses')
    expect(ruleReach({ kind: 'email' }, 'sub')).toBe('applies to names that are addresses')
    expect(ruleReach({ kind: 'name' }, 'sub')).toBe('')
    expect(ruleReach({ kind: 'domain' }, '')).toBe('')
  })

  it('says why a session ended when the way people sign in was changed', () => {
    expect(sessionEndedMessage(new AgentError(401, 'SESSION_ENDED', 'how people sign in to this server was changed. Sign in again', { reason: 'sign_in_changed' }))).toBe('How people sign in to this server was changed. Sign in again.')
  })
})

describe('accessTabs', () => {
  it('has a tab for tokens, for sign-in and for the trail, each with its address', () => {
    const tabs = accessTabs()
    expect(tabs.map(t => t.label)).toEqual(['API tokens', 'Sign-in', 'Audit trail'])
    expect(activeTab(tabs, '/settings/access')?.key).toBe('tokens')
    expect(activeTab(tabs, '/settings/access/sign-in')?.key).toBe('sign-in')
    expect(activeTab(tabs, '/settings/access/audit')?.key).toBe('audit')
  })
})
