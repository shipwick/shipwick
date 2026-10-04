import { createHash } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import {
  FAILURE_NOT_STARTED_HERE,
  authorizationUrl,
  codeChallenge,
  describeFailure,
  parseFailure,
  parsePending,
  parseSignInConfig,
  providerRefusal,
  randomValue,
  sameValue,
  sessionMaxAge,
} from '../server/utils/signin'

const config = {
  configured: true,
  issuer: 'https://accounts.example.com/realms/company',
  authorization_endpoint: 'https://accounts.example.com/realms/company/auth?kc_idp_hint=corp',
  client_id: 'shipwick',
  scopes: ['openid', 'email', 'profile'],
  redirect_uri: 'https://dashboard.example.com/auth/callback',
}

describe('the values a sign-in begins with', () => {
  it('are 43 characters of base64url, and different every time', () => {
    const a = randomValue()
    expect(a).toMatch(/^[A-Za-z0-9_-]{43}$/)
    expect(randomValue()).not.toBe(a)
  })

  it('derives the S256 challenge of a verifier', () => {
    expect(codeChallenge('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM')
    const verifier = randomValue()
    expect(codeChallenge(verifier)).toBe(createHash('sha256').update(verifier).digest('base64url'))
  })
})

describe('parseSignInConfig', () => {
  it('takes what a configured agent answers', () => {
    expect(parseSignInConfig(config)?.client_id).toBe('shipwick')
  })

  it('is null for an agent without a provider and for an answer that is not one', () => {
    expect(parseSignInConfig({ configured: false, issuer: '', authorization_endpoint: '', client_id: '', scopes: [], redirect_uri: '' })).toBeNull()
    expect(parseSignInConfig(null)).toBeNull()
    expect(parseSignInConfig({ ...config, scopes: 'openid' })).toBeNull()
  })

  it('refuses an authorization endpoint the browser must not be sent to', () => {
    expect(parseSignInConfig({ ...config, authorization_endpoint: 'javascript:alert(1)' })).toBeNull()
    expect(parseSignInConfig({ ...config, authorization_endpoint: '/relative' })).toBeNull()
  })
})

describe('authorizationUrl', () => {
  it('adds the request to the endpoint and keeps the endpoint\'s own query', () => {
    const pending = { state: 's'.repeat(43), nonce: 'n'.repeat(43), verifier: 'v'.repeat(43) }
    const url = new URL(authorizationUrl(parseSignInConfig(config)!, pending))
    expect(url.origin + url.pathname).toBe('https://accounts.example.com/realms/company/auth')
    expect(Object.fromEntries(url.searchParams)).toEqual({
      kc_idp_hint: 'corp',
      response_type: 'code',
      client_id: 'shipwick',
      redirect_uri: 'https://dashboard.example.com/auth/callback',
      scope: 'openid email profile',
      state: pending.state,
      nonce: pending.nonce,
      code_challenge: codeChallenge(pending.verifier),
      code_challenge_method: 'S256',
    })
    // The verifier itself never leaves the dashboard's server before the exchange.
    expect(url.toString()).not.toContain(pending.verifier)
  })
})

describe('parsePending', () => {
  const pending = { state: randomValue(), nonce: randomValue(), verifier: randomValue(), server: 'default' }

  it('reads back what the cookie was given', () => {
    expect(parsePending(JSON.stringify(pending))).toEqual(pending)
  })

  it('is null for a missing cookie and for one that was altered', () => {
    expect(parsePending(undefined)).toBeNull()
    expect(parsePending('not json')).toBeNull()
    expect(parsePending(JSON.stringify({ ...pending, state: 'short' }))).toBeNull()
    expect(parsePending(JSON.stringify({ ...pending, server: '../other' }))).toBeNull()
    expect(parsePending(JSON.stringify({ ...pending, verifier: undefined }))).toBeNull()
  })
})

describe('sameValue', () => {
  it('is true only for equal values', () => {
    expect(sameValue('abc', 'abc')).toBe(true)
    expect(sameValue('abc', 'abd')).toBe(false)
    expect(sameValue('abc', 'abcd')).toBe(false)
    expect(sameValue('', 'abc')).toBe(false)
  })
})

describe('what the sign-in page says when a sign-in did not complete', () => {
  it('passes on the agent\'s sentence for somebody without a rule, to be copied', () => {
    const message = 'grace@example.com signed in, and no rule on this server gives that address a role. An admin grants one with: shipwick access grant grace@example.com --role read'
    expect(describeFailure(403, 'ACCESS_NOT_GRANTED', message, { email: 'grace@example.com' })).toEqual({ title: 'You signed in, and this server has no role for you yet.', message, copy: true })
  })

  it('asks to sign in again when the code or the nonce was not accepted, without the agent\'s detail', () => {
    for (const reason of ['code_rejected', 'nonce_mismatch', 'nonce_reused']) {
      expect(describeFailure(401, 'SIGN_IN_FAILED', 'whatever the agent said', { reason })).toEqual({ title: 'That sign-in could not be completed. Sign in again.', message: '', copy: false })
    }
  })

  it('says why the provider\'s answer was not accepted, and what is wrong with the account', () => {
    expect(describeFailure(401, 'SIGN_IN_FAILED', 'the sign-in provider\'s ID token was not accepted: it has expired', { reason: 'invalid_id_token' }).message).toContain('it has expired')
    expect(describeFailure(401, 'SIGN_IN_FAILED', 'the provider says the address ada@example.com has not been verified', { reason: 'email_not_verified' }).message).toContain('has not been verified')
  })

  it('passes on what is wrong with an account that has no name, or belongs to a tenant that may not sign in, to be forwarded', () => {
    const missing = 'the provider\'s ID token has no preferred_username claim Shipwick can name this account by'
    expect(describeFailure(401, 'SIGN_IN_FAILED', missing, { reason: 'name_missing', claim: 'preferred_username' })).toEqual({ title: 'The provider did not say who you are in a way this server can use.', message: missing, copy: true })
    const tenant = 'this account belongs to a Microsoft Entra tenant that may not sign in here. An operator adds the tenant\'s id to SHIPWICK_OIDC_TENANTS on the agent'
    expect(describeFailure(401, 'SIGN_IN_FAILED', tenant, { reason: 'tenant_not_allowed' })).toEqual({ title: 'Your account belongs to an organization that may not sign in to this server.', message: tenant, copy: true })
  })

  it('has a sentence for a provider that is down, an agent without one, a rate limit and a redirect that does not match', () => {
    expect(describeFailure(502, 'SIGN_IN_UNAVAILABLE', 'the sign-in provider could not be reached').title).toBe('The sign-in provider cannot be used right now.')
    expect(describeFailure(409, 'SIGN_IN_NOT_CONFIGURED', '').title).toBe('This server takes API tokens only.')
    expect(describeFailure(429, 'RATE_LIMITED', '').title).toBe('Too many failed attempts from this address; try again in a minute.')
    expect(describeFailure(400, 'INVALID_REQUEST', 'redirect_uri must be https://dashboard.example.com/auth/callback').title).toBe('The dashboard and the agent disagree about where this dashboard lives.')
  })

  it('shows only the name of an error the provider sent back', () => {
    expect(providerRefusal('access_denied').title).toBe('The provider did not sign you in (access_denied).')
    expect(providerRefusal('<img src=x onerror=alert(1)>').title).toBe('The provider did not sign you in (error).')
  })

  it('survives the cookie it travels in, and nothing else does', () => {
    expect(parseFailure(JSON.stringify(FAILURE_NOT_STARTED_HERE))).toEqual(FAILURE_NOT_STARTED_HERE)
    expect(parseFailure('{"title": 3}')).toBeNull()
    expect(parseFailure(undefined)).toBeNull()
  })
})

describe('sessionMaxAge', () => {
  const now = Date.parse('2026-10-03T12:00:00Z')
  const week = 7 * 24 * 60 * 60

  it('lets the cookie live as long as the session', () => {
    expect(sessionMaxAge('2026-10-03T22:00:00Z', week, now)).toBe(10 * 60 * 60)
  })

  it('is never longer than a token\'s cookie, never shorter than a minute, and a week when the agent says nothing', () => {
    expect(sessionMaxAge('2027-10-03T22:00:00Z', week, now)).toBe(week)
    expect(sessionMaxAge('2026-10-03T12:00:05Z', week, now)).toBe(60)
    expect(sessionMaxAge(undefined, week, now)).toBe(week)
  })
})
