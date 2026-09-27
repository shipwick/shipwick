import { describe, expect, it } from 'vitest'
import { AgentError, RATE_LIMITED_MESSAGE, errorFromResponse } from '../app/utils/agentError'

function response(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

describe('errorFromResponse', () => {
  it('reads the agent\'s envelope', async () => {
    const error = await errorFromResponse(response(409, { error: { code: 'APPLICATION_RUNNING', message: 'the application is running; stop it first with: shipwick stop', details: {} } }))
    expect(error).toBeInstanceOf(AgentError)
    expect(error.status).toBe(409)
    expect(error.code).toBe('APPLICATION_RUNNING')
    expect(error.message).toBe('the application is running; stop it first with: shipwick stop')
  })

  it('keeps the FORBIDDEN details', async () => {
    const error = await errorFromResponse(response(403, { error: { code: 'FORBIDDEN', message: 'x', details: { role: 'read', required: 'deploy' } } }))
    expect(error.details).toEqual({ role: 'read', required: 'deploy' })
  })

  it('tolerates a body that is not an envelope', async () => {
    const error = await errorFromResponse(new Response('<html>', { status: 502 }))
    expect(error.code).toBe('BAD_RESPONSE')
    expect(error.message).toBe('Request failed with status 502')
  })
})

describe('rate limiting', () => {
  it('is recognized by status or code and worded for the person, not as a wrong token', async () => {
    const error = await errorFromResponse(response(429, { error: { code: 'RATE_LIMITED', message: 'too many failed authentications from this address; try again in a minute', details: {} } }))
    expect(error.rateLimited).toBe(true)
    expect(error.status).not.toBe(401)
    expect(error.displayMessage).toBe(RATE_LIMITED_MESSAGE)
    expect(new AgentError(429, 'BAD_RESPONSE', 'Request failed with status 429').rateLimited).toBe(true)
  })

  it('leaves every other message alone', () => {
    const error = new AgentError(409, 'STATIC_APPLICATION', 'this application is a folder served by the proxy; it has no containers')
    expect(error.rateLimited).toBe(false)
    expect(error.displayMessage).toBe(error.message)
    const inUse = new AgentError(409, 'VOLUME_IN_USE', 'the volume belongs to application postgres; delete the application first — its data stays until the volume is removed', { application: 'postgres' })
    expect(inUse.displayMessage).toBe(inUse.message)
    expect(inUse.details.application).toBe('postgres')
  })
})

describe('AgentError.fields', () => {
  it('passes field names through unchanged, indexed and nested ones included', () => {
    const error = new AgentError(400, 'INVALID_CONFIG', 'invalid deploy.yaml', {
      fields: [
        { field: 'aliases[1]', message: 'already served by application "web"' },
        { field: 'redirects[0]', message: 'is empty' },
        { field: 'publish[0].host', message: 'already published by application "postgres"' },
        { field: 'health.command[1]', message: 'must not be empty', expected: '["pg_isready", "-U", "postgres"]' },
        { field: 'logging.options.gelf-address', message: 'must be udp:// or tcp://' },
      ],
    })
    expect(error.fields.map(f => f.field)).toEqual(['aliases[1]', 'redirects[0]', 'publish[0].host', 'health.command[1]', 'logging.options.gelf-address'])
    expect(error.fields[3]?.expected).toBe('["pg_isready", "-U", "postgres"]')
  })

  it('drops entries that are not field problems, and is empty without details', () => {
    const error = new AgentError(400, 'INVALID_CONFIG', 'x', { fields: [{ message: 'no field' }, 'junk', { field: 'domain', message: 'taken' }] })
    expect(error.fields.map(f => f.field)).toEqual(['domain'])
    expect(new AgentError(500, 'INTERNAL_ERROR', 'x').fields).toEqual([])
  })
})
