import { describe, expect, it } from 'vitest'
import type { TokenIdentity } from '../app/types/api'
import { AgentError } from '../app/utils/agentError'
import {
  accessSummary,
  auditActionLabel,
  auditDetailLink,
  auditFrom,
  auditHasOlder,
  auditOutcomeDisplay,
  auditSubject,
  canDeploy,
  deployHint,
  expiryFromChoice,
  issuerHost,
  limitedExplanation,
  listNames,
  parseApplications,
  readOnlyReason,
  ruleSubject,
  ruleSubjectProblem,
  sessionEndedMessage,
  sessionExpiryWarning,
  signedInUntil,
  spanInWords,
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

describe('accessTabs', () => {
  it('has a tab for tokens, for sign-in and for the trail, each with its address', () => {
    const tabs = accessTabs()
    expect(tabs.map(t => t.label)).toEqual(['API tokens', 'Sign-in', 'Audit trail'])
    expect(activeTab(tabs, '/settings/access')?.key).toBe('tokens')
    expect(activeTab(tabs, '/settings/access/sign-in')?.key).toBe('sign-in')
    expect(activeTab(tabs, '/settings/access/audit')?.key).toBe('audit')
  })
})
