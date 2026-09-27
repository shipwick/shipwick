import { describe, expect, it } from 'vitest'
import { AgentError } from '../app/utils/agentError'
import { forbiddenExplanation, isRole, roleCovers, roleHint } from '../app/utils/roles'

describe('roleCovers', () => {
  it('lets a role use its own endpoints and those of the roles below it', () => {
    expect(roleCovers('read', 'read')).toBe(true)
    expect(roleCovers('deploy', 'read')).toBe(true)
    expect(roleCovers('admin', 'read')).toBe(true)
    expect(roleCovers('admin', 'deploy')).toBe(true)
    expect(roleCovers('admin', 'admin')).toBe(true)
  })

  it('refuses everything above the role', () => {
    expect(roleCovers('read', 'deploy')).toBe(false)
    expect(roleCovers('read', 'admin')).toBe(false)
    expect(roleCovers('deploy', 'admin')).toBe(false)
  })

  it('recognizes the three roles and nothing else', () => {
    expect(isRole('read')).toBe(true)
    expect(isRole('admin')).toBe(true)
    expect(isRole('owner')).toBe(false)
    expect(isRole(undefined)).toBe(false)
  })
})

describe('forbiddenExplanation', () => {
  it('builds the sentence from the details, in the agent\'s words', () => {
    const error = new AgentError(403, 'FORBIDDEN', 'this token has the read role; deploying needs deploy or admin', { role: 'read', required: 'deploy' })
    expect(forbiddenExplanation(error)).toBe('This token has the read role; deploying needs deploy or admin')
    const admin = new AgentError(403, 'FORBIDDEN', 'x', { role: 'deploy', required: 'admin' })
    expect(forbiddenExplanation(admin)).toBe('This token has the deploy role; this needs admin')
  })

  it('falls back to the message when the details are missing', () => {
    expect(forbiddenExplanation(new AgentError(403, 'FORBIDDEN', 'not allowed'))).toBe('Not allowed')
  })

  it('is empty for any other error', () => {
    expect(forbiddenExplanation(new AgentError(409, 'DEPLOYMENT_IN_PROGRESS', 'busy'))).toBe('')
  })
})

describe('roleHint', () => {
  it('says which role an action needs', () => {
    expect(roleHint('deploy')).toBe('Deploying needs the deploy or admin role')
    expect(roleHint('admin')).toBe('This needs the admin role')
  })
})
